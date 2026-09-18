// Package safeio provides atomic, permission-restricted file writes.
//
// Every write goes through a temp file in the same directory, is fsynced,
// chmod'd to 0600, and then renamed into place. Readers (including the bot's
// background reloader and a concurrently running manage_commands.sh) can only
// ever observe either the old or the new content, never a partial write.
package safeio

import (
	"fmt"
	"os"
	"path/filepath"
)

const DefaultPerm = 0o600

// WriteFileAtomic writes data to path atomically with the given permissions.
// The parent directory is created with mode 0700 when missing.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}
