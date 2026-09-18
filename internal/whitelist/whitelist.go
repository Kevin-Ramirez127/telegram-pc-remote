// Package whitelist implements the user and chat allowlist (fail-closed).
//
// The whitelist is the bot's primary security gate: an update is processed
// only if BOTH the sending user AND the chat are on the allowlist. Everyone
// else is dropped without any reply, so the bot never confirms its existence
// to unauthorized parties.
//
// Whether the file is missing, corrupt, or simply empty, the bot operates in
// deny-all mode — a mistake on disk can never grant access.
package whitelist

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

type file struct {
	Users []int64 `json:"users"`
	Chats []int64 `json:"chats"`
	Note  string  `json:"note,omitempty"`
}

type Whitelist struct {
	mu    sync.RWMutex
	path  string
	users map[int64]struct{}
	chats map[int64]struct{}
}

func New(path string) (*Whitelist, error) {
	w := &Whitelist{path: path}
	if err := w.Load(); err != nil {
		return nil, err
	}
	return w, nil
}

// Load re-reads the allowlist. On failure the previous allowlist is kept, so
// a transient disk error or an edited-but-invalid file cannot lock anyone out
// (nor accidentally let anyone in).
func (w *Whitelist) Load() error {
	data, err := os.ReadFile(w.path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("whitelist file %s not found (fail closed: create it and add your IDs)", w.path)
		}
		return fmt.Errorf("read whitelist %s: %w", w.path, err)
	}

	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("whitelist %s is corrupt (fail closed): %w", w.path, err)
	}

	users := make(map[int64]struct{}, len(f.Users))
	for _, id := range f.Users {
		if id == 0 {
			return fmt.Errorf("whitelist %s contains user id 0", w.path)
		}
		users[id] = struct{}{}
	}
	chats := make(map[int64]struct{}, len(f.Chats))
	for _, id := range f.Chats {
		if id == 0 {
			return fmt.Errorf("whitelist %s contains chat id 0", w.path)
		}
		chats[id] = struct{}{}
	}

	w.mu.Lock()
	w.users = users
	w.chats = chats
	w.mu.Unlock()
	return nil
}

// IsAllowed reports whether a user in a chat may interact with the bot.
// Both must be allowlisted.
func (w *Whitelist) IsAllowed(userID, chatID int64) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, uOK := w.users[userID]
	_, cOK := w.chats[chatID]
	return uOK && cOK
}

func (w *Whitelist) LenUsers() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.users)
}

func (w *Whitelist) LenChats() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.chats)
}
