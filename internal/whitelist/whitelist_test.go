package whitelist

import (
	"os"
	"path/filepath"
	"testing"
)

const valid = `{
  "note": "test whitelist",
  "users": [111, 222],
  "chats": [333]
}`

func TestIsAllowedRequiresBoth(t *testing.T) {
	p := filepath.Join(t.TempDir(), "whitelist.json")
	if err := os.WriteFile(p, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	if !w.IsAllowed(111, 333) {
		t.Fatal("whitelisted user in whitelisted chat must be allowed")
	}
	if w.IsAllowed(999, 333) {
		t.Fatal("non-whitelisted user must be denied")
	}
	if w.IsAllowed(111, 999) {
		t.Fatal("non-whitelisted chat must be denied")
	}
}

func TestMissingFileFailsClosed(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("missing whitelist file must be an error (fail closed)")
	}
}

func TestCorruptFileFailsClosed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "whitelist.json")
	if err := os.WriteFile(p, []byte(`{oops`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(p); err == nil {
		t.Fatal("corrupt whitelist must be an error (fail closed)")
	}
}

func TestReloadKeepsPreviousAllowlistOnFailure(t *testing.T) {
	p := filepath.Join(t.TempDir(), "whitelist.json")
	if err := os.WriteFile(p, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	// A corrupt edit must not lock anyone out — nor let anyone new in.
	if err := os.WriteFile(p, []byte(`{"users":[555],"chats":[666]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.Load(); err == nil {
		t.Fatal("reload of corrupt file must fail")
	}
	if !w.IsAllowed(111, 333) {
		t.Fatal("previous valid allowlist must stay in effect")
	}
	if w.IsAllowed(555, 666) {
		t.Fatal("corrupt content must not apply")
	}
}

func TestEmptyAllowlistDeniesEveryone(t *testing.T) {
	p := filepath.Join(t.TempDir(), "whitelist.json")
	if err := os.WriteFile(p, []byte(`{"users":[],"chats":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	if w.IsAllowed(1, 2) {
		t.Fatal("empty allowlist must deny everyone")
	}
}
