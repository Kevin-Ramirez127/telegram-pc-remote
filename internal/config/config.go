// Package config loads all runtime configuration for the bot.
//
// Security notes:
//   - The bot token is read from the TELEGRAM_BOT_TOKEN environment variable
//     (or an optional .env file) and is never logged.
//   - An optional .env file is supported for local development convenience,
//     but it MUST never be committed and should be chmod 600.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Token string
	// DataDir is the absolute path holding commands.json and whitelist.json.
	DataDir       string
	CommandsFile  string
	WhitelistFile string
	// CommandsDir is the absolute path holding the user-authored .sh scripts.
	CommandsDir string

	// ReloadEvery is how often the command and whitelist files are checked
	// for external changes (mtime/size based).
	ReloadEvery time.Duration
	// MaxConcurrent caps how many updates are processed at the same time,
	// which bounds resource usage from slow scripts.
	MaxConcurrent int
	// MaxCommandDuration is a hard cap for a single command's execution,
	// independent of each command's own timeout_sec.
	MaxCommandDuration time.Duration
}

func Load() (*Config, error) {
	if err := loadDotEnv(); err != nil {
		return nil, fmt.Errorf("loading .env: %w", err)
	}

	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("TELEGRAM_BOT_TOKEN is not set (get one from @BotFather)")
	}

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "data"
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("bad DATA_DIR %q: %w", dataDir, err)
	}

	commandsDir := os.Getenv("COMMANDS_DIR")
	if commandsDir == "" {
		commandsDir = "commands"
	}
	cmdsAbs, err := filepath.Abs(commandsDir)
	if err != nil {
		return nil, fmt.Errorf("bad COMMANDS_DIR %q: %w", commandsDir, err)
	}

	reloadEvery := envDuration("BOT_RELOAD_SECONDS", 5*time.Second)
	maxConcurrent := envInt("BOT_MAX_CONCURRENT", 4)
	if maxConcurrent < 1 {
		return nil, fmt.Errorf("BOT_MAX_CONCURRENT must be >= 1")
	}
	maxDuration := envDuration("BOT_MAX_COMMAND_SECONDS", 10*time.Minute)

	return &Config{
		Token:              token,
		DataDir:            abs,
		CommandsFile:       filepath.Join(abs, "commands.json"),
		WhitelistFile:      filepath.Join(abs, "whitelist.json"),
		CommandsDir:        cmdsAbs,
		ReloadEvery:        reloadEvery,
		MaxConcurrent:      maxConcurrent,
		MaxCommandDuration: maxDuration,
	}, nil
}

// loadDotEnv applies an optional .env file. Existing environment variables
// always win, so a service manager's EnvironmentFile cannot be overridden by
// a stray .env on disk.
func loadDotEnv() error {
	data, err := os.ReadFile(".env")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(strings.Trim(value, `"'`))
		if key != "" && os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
	return nil
}

func envInt(name string, def int) int {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n := 0
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

func envDuration(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
