// Package actions executes a registered command script and converts its
// output into a deliverable result.
//
// A command runs the registered .sh file with `sh <script>` (argv form — the
// script path comes from the operator-approved store, never from chat input,
// so there is no shell-injection surface). The reply is either:
//
//   - text:   the script's stdout (optionally wrapped by a template with
//     ${output} placeholders), or
//   - image:  for --img commands, the stdout is a path to an image file which
//     is read and sent as a photo (with the template as caption).
//
// Security model:
//   - Scripts are written and registered in advance by the operator; they are
//     as trusted as the operator.
//   - Every script gets its own timeout_sec plus a global hard cap, a
//     locked-down environment (only a minimal PATH), and its own process
//     group so that on timeout the whole subtree is killed, not just "sh".
//   - Image paths are validated: the file must exist, be a regular file,
//     stay under the size cap, and its magic bytes must identify a known
//     image format (no arbitrary binaries are ever sent to the chat).
//   - Output is truncated so a chat can never be flooded past Telegram's
//     message limit.
package actions

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"telegram-pc-remote/internal/store"
)

// ExecuteOpts carries the resolved execution environment for a command.
type ExecuteOpts struct {
	// ScriptPath is the absolute path of the script to run.
	ScriptPath string
	// WorkDir is the working directory for the script (the commands dir),
	// so relative image paths and relative file access behave predictably.
	WorkDir string
	// Option is the menu option's value (or label). When non-empty it is
	// passed to the script as its first argument ($1) and as TPR_OPTION.
	Option string
	// OptionLabel is the menu option's button label, passed as
	// TPR_OPTION_LABEL (empty for plain commands).
	OptionLabel string
}

// Result is what the bot sends back to the chat.
type Result struct {
	// Text is the message body, or the photo caption when Photo is set.
	Text string
	// Photo, when non-nil, means the response is an image.
	Photo []byte
}

// syncBuffer is a bytes.Buffer safe for concurrent writes (exec writes stdout
// and stderr through two goroutines).
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// minimalEnv strips all inherited environment variables from scripts (no
// HOME, no secrets, no arbitrary PATH entries).
var minimalEnv = []string{
	"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"LANG=C",
}

// imageSignatures maps magic bytes to a human-readable name. Only if a file
// matches one of these is it sent as a photo.
var imageSignatures = []struct {
	name string
	mask []byte
}{
	// \x89PNG\r\n\x1a\n
	{"PNG", []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}},
	// \xFF\xD8\xFF
	{"JPEG", []byte{0xff, 0xd8, 0xff}},
	// GIF87a / GIF89a
	{"GIF", []byte{'G', 'I', 'F', '8'}},
	// RIFF....WEBP
	{"WebP", []byte{'R', 'I', 'F', 'F'}},
	// BM
	{"BMP", []byte{'B', 'M'}},
}

func commandTimeout(cmd store.Command) time.Duration {
	d := time.Duration(cmd.TimeoutSec) * time.Second
	if d <= 0 {
		d = store.DefaultTimeout
	}
	return d
}

// Execute runs the command's script and returns the result for the chat.
func Execute(ctx context.Context, cmd store.Command, opts ExecuteOpts) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := commandTimeout(cmd)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"sh", opts.ScriptPath}
	var extraEnv []string
	if opts.Option != "" {
		args = append(args, opts.Option)
		extraEnv = append(extraEnv, "TPR_OPTION="+opts.Option)
	}
	if opts.OptionLabel != "" {
		extraEnv = append(extraEnv, "TPR_OPTION_LABEL="+opts.OptionLabel)
	}

	out, err := run(ctx, args, opts.WorkDir, extraEnv, timeout)
	out = truncate(out, store.MaxOutputBytes)
	if err != nil {
		return Result{Text: out}, err
	}

	if cmd.Img {
		return imageResult(out, opts.WorkDir, cmd)
	}
	if cmd.Template != "" {
		return Result{Text: renderTemplate(cmd.Template, out)}, nil
	}
	return Result{Text: out}, nil
}

// run executes args (argv form) with the hardened settings and a deadline
// taken from ctx. On error the partial output is still returned.
func run(ctx context.Context, args []string, dir string, extraEnv []string, timeout time.Duration) (string, error) {
	buf := &syncBuffer{}
	// CommandContext kills the direct child when ctx expires; a process group
	// kill below catches everything else in the subtree.
	c := exec.CommandContext(ctx, args[0], args[1:]...)
	c.Dir = dir
	c.Env = append(append([]string{}, minimalEnv...), extraEnv...)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	c.Stdout = buf
	c.Stderr = buf

	if err := c.Start(); err != nil {
		return "", err
	}

	done := make(chan error, 1)
	go func() { done <- c.Wait() }()

	var err error
	select {
	case err = <-done:
		// Command finished; err is nil on success.
	case <-ctx.Done():
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		<-done
		if ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("timed out after %s", timeout)
		} else {
			err = ctx.Err()
		}
	}

	text := truncate(buf.String(), store.MaxOutputBytes)
	if err != nil {
		return text, err
	}
	return text, nil
}

// imageResult interprets the script output as a path to an image file,
// validates it, and loads it for sending.
func imageResult(out string, workDir string, cmd store.Command) (Result, error) {
	pathStr := strings.TrimSpace(out)
	pathStr = strings.TrimLeft(pathStr, "\n")
	if pathStr == "" {
		return Result{}, fmt.Errorf("--img command %q printed no image path", cmd.Text)
	}
	p := pathStr
	if !filepath.IsAbs(p) {
		p = filepath.Join(workDir, p)
	}
	p = filepath.Clean(p)

	fi, err := os.Stat(p)
	if err != nil {
		return Result{}, fmt.Errorf("image %q not accessible: %v", p, err)
	}
	if !fi.Mode().IsRegular() {
		return Result{}, fmt.Errorf("image %q is not a regular file", p)
	}
	if fi.Size() > store.MaxImageBytes {
		return Result{}, fmt.Errorf("image %q is %d bytes, over the %d MiB limit", p, fi.Size(), store.MaxImageBytes>>20)
	}

	data, err := os.ReadFile(p)
	if err != nil {
		return Result{}, fmt.Errorf("image %q could not be read: %v", p, err)
	}
	if name := sniffImage(data); name == "" {
		return Result{}, fmt.Errorf("image %q is not a recognized image (PNG/JPEG/GIF/WebP/BMP)", p)
	}

	caption := ""
	if cmd.Template != "" {
		caption = truncate(renderTemplate(cmd.Template, strings.TrimSpace(out)), store.MaxCaptionBytes)
	}
	return Result{Photo: data, Text: caption}, nil
}

func sniffImage(data []byte) string {
	for _, sig := range imageSignatures {
		if bytes.HasPrefix(data, sig.mask) {
			return sig.name
		}
	}
	return ""
}

// renderTemplate substitutes ${output} and expands \n / \t escapes so
// multi-line replies are easy to author on a single line.
func renderTemplate(template, output string) string {
	s := ExpandEscapes(template)
	s = strings.ReplaceAll(s, "${output}", output)
	return s
}

// ExpandEscapes turns \n / \t into real newlines/tabs (shared by templates
// and menu prompts).
func ExpandEscapes(s string) string {
	s = strings.ReplaceAll(s, `\n`, "\n")
	s = strings.ReplaceAll(s, `\t`, "\t")
	return s
}

func truncate(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) > limit {
		return s[:limit] + "\n…[output truncated]"
	}
	return s
}
