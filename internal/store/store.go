// Package store manages the bot's command definitions.
//
// A "command" is a button label on the inline keyboard backed by a
// user-authored shell script in the commands directory. Pressing the button
// (or typing the exact label) runs the registered script; the script's
// stdout becomes the reply. A command may declare:
//
//	script    – the .sh file to run (relative to the commands directory)
//	template  – optional response template; ${output} is replaced by the
//	            script's output, \n and \t become real newlines/tabs
//	img       – the script's output is a path to an image file; the bot sends
//	            that image as a photo (with the template as caption)
//	timeout_sec – execution timeout (default 30s, max 300s)
//
// The file format is validated strictly: script paths must stay inside the
// commands directory, must be regular files ending in .sh, templates may only
// reference ${output}, and every load only ever swaps in fully-validated
// content.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// MaxTextBytes matches Telegram's inline button callback_data limit.
	MaxTextBytes = 64
	// MaxReplyBytes is Telegram's limit for a single text message.
	MaxReplyBytes = 4096
	// MaxCaptionBytes is Telegram's photo caption limit.
	MaxCaptionBytes = 1024
	// MaxOutputBytes caps script output sent back to the chat.
	MaxOutputBytes = 3500
	// MaxImageBytes caps images the bot will send (Telegram allows 10 MiB).
	MaxImageBytes = 9 << 20 // 9 MiB
	// MaxScriptPathBytes caps a registered script path.
	MaxScriptPathBytes = 512
	// DefaultTimeout applies when a command has no timeout_sec.
	DefaultTimeout = 30 * time.Second

	CurrentVersion = 2
)

var templateRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Command is one registered button + backing script.
type Command struct {
	Text   string `json:"text"`
	Script string `json:"script"`

	// Template optionally shapes the reply. ${output} is replaced by the
	// script's output; \n / \t become real newlines/tabs.
	Template string `json:"template,omitempty"`

	// Img marks image commands: the script's output is a path to an image
	// file, which is sent as a photo (template becomes the caption).
	Img bool `json:"img,omitempty"`

	TimeoutSec int `json:"timeout_sec,omitempty"`
}

type file struct {
	Version  int       `json:"version"`
	Commands []Command `json:"commands"`
}

type Store struct {
	mu          sync.RWMutex
	path        string
	scriptsRoot string
	commands    []Command
}

func New(path, scriptsRoot string) (*Store, error) {
	s := &Store{path: path, scriptsRoot: scriptsRoot}
	if err := s.Load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Load reads and validates the commands file. On any error the previous
// in-memory state is kept untouched (and callers should log and continue).
func (s *Store) Load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("commands file %s not found (create it with scripts/manage_commands.sh add ...)", s.path)
		}
		return fmt.Errorf("read %s: %w", s.path, err)
	}

	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("commands file %s is not valid JSON: %w", s.path, err)
	}
	if err := s.validate(&f); err != nil {
		return fmt.Errorf("commands file %s invalid: %w", s.path, err)
	}

	cmds := make([]Command, len(f.Commands))
	copy(cmds, f.Commands)

	s.mu.Lock()
	s.commands = cmds
	s.mu.Unlock()
	return nil
}

func (s *Store) validate(f *file) error {
	if f.Version != CurrentVersion {
		return fmt.Errorf("unsupported version %d (want %d)", f.Version, CurrentVersion)
	}
	seen := make(map[string]struct{}, len(f.Commands))
	for _, c := range f.Commands {
		if c.Text == "" {
			return fmt.Errorf("command with empty text")
		}
		if strings.TrimSpace(c.Text) != c.Text {
			return fmt.Errorf("command text %q must not have leading/trailing whitespace", c.Text)
		}
		if len(c.Text) > MaxTextBytes {
			return fmt.Errorf("command text %q exceeds %d bytes", c.Text, MaxTextBytes)
		}
		if strings.IndexFunc(c.Text, func(r rune) bool {
			return r < 0x20 || r == 0x7f
		}) >= 0 {
			return fmt.Errorf("command text %q contains control characters", c.Text)
		}
		if _, dup := seen[c.Text]; dup {
			return fmt.Errorf("duplicate command text %q", c.Text)
		}
		seen[c.Text] = struct{}{}

		if err := s.validateScript(c.Script); err != nil {
			return fmt.Errorf("command %q: %w", c.Text, err)
		}
		if err := validateTemplate(c); err != nil {
			return err
		}
		if c.TimeoutSec != 0 && (c.TimeoutSec < 1 || c.TimeoutSec > 300) {
			return fmt.Errorf("command %q: timeout_sec must be 1..300", c.Text)
		}
	}
	return nil
}

// validateScript enforces that a registered script is a relative path inside
// the commands directory and currently exists as a regular .sh file.
func (s *Store) validateScript(script string) error {
	if script == "" {
		return fmt.Errorf("empty script path")
	}
	if len(script) > MaxScriptPathBytes {
		return fmt.Errorf("script path exceeds %d bytes", MaxScriptPathBytes)
	}
	if filepath.IsAbs(script) {
		return fmt.Errorf("script %q must be relative to the commands directory", script)
	}
	if !strings.HasSuffix(script, ".sh") {
		return fmt.Errorf("script %q must end in .sh", script)
	}

	joined := filepath.Join(s.scriptsRoot, filepath.Clean(script))
	rel, err := filepath.Rel(s.scriptsRoot, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("script %q escapes the commands directory", script)
	}

	fi, err := os.Stat(joined)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("script %q registered but the file does not exist in %s", script, s.scriptsRoot)
		}
		return fmt.Errorf("script %q cannot be accessed: %v", script, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("script %q is not a regular file", script)
	}
	return nil
}

// validateTemplate only allows the ${output} placeholder.
func validateTemplate(c Command) error {
	if c.Template == "" {
		return nil
	}
	if len(c.Template) > MaxReplyBytes {
		return fmt.Errorf("command %q: template exceeds %d bytes", c.Text, MaxReplyBytes)
	}
	for _, m := range templateRefRe.FindAllStringSubmatch(c.Template, -1) {
		if m[1] != "output" {
			return fmt.Errorf("command %q: template may only reference ${output}, got ${%s}", c.Text, m[1])
		}
	}
	return nil
}

// Lookup returns the command whose text matches exactly (extra whitespace on
// the input is trimmed, but stored text is matched exactly).
func (s *Store) Lookup(text string) (Command, bool) {
	text = strings.TrimSpace(text)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.commands {
		if c.Text == text {
			return c, true
		}
	}
	return Command{}, false
}

// List returns a copy of all commands sorted by text.
func (s *Store) List() []Command {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Command, len(s.commands))
	copy(out, s.commands)
	sort.Slice(out, func(i, j int) bool { return out[i].Text < out[j].Text })
	return out
}

// Count returns the number of configured commands.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.commands)
}

// ResolveScript returns the absolute path of a registered script, re-checking
// containment as defense in depth (a later edit cannot smuggle in an escape).
func (s *Store) ResolveScript(c Command) (string, error) {
	if err := s.validateScript(c.Script); err != nil {
		return "", err
	}
	return filepath.Join(s.scriptsRoot, filepath.Clean(c.Script)), nil
}

// ValidText reports whether text is acceptable as a command label.
func ValidText(text string) bool {
	if text == "" || strings.TrimSpace(text) != text {
		return false
	}
	if len(text) > MaxTextBytes {
		return false
	}
	for _, r := range text {
		if r < 0x20 || r == 0x7f || unicode.IsControl(r) {
			return false
		}
	}
	return true
}
