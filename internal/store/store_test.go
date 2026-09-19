package store

import (
	"os"
	"path/filepath"
	"testing"
)

// setup creates a commands dir with the given scripts and returns both paths.
func setup(t *testing.T, scripts map[string]string) (filesDir, scriptsRoot string) {
	t.Helper()
	base := t.TempDir()
	filesDir = filepath.Join(base, "data")
	scriptsRoot = filepath.Join(base, "commands")
	if err := os.MkdirAll(filesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scriptsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range scripts {
		p := filepath.Join(scriptsRoot, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return filesDir, scriptsRoot
}

func writeCommands(t *testing.T, dir, content string) string {
	t.Helper()
	p := filepath.Join(dir, "commands.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const validCommands = `{
  "version": 2,
  "commands": [
    {"text": "Status", "script": "status.sh", "timeout_sec": 15},
    {"text": "Photo",  "script": "photo.sh",  "img": true,
     "template": "Shot: ${output}"},
    {"text": "Hello",  "script": "hello.sh"}
  ]
}`

func TestLoadValid(t *testing.T) {
	dir, root := setup(t, map[string]string{
		"status.sh": "echo up\n",
		"photo.sh":  "echo /tmp/x.png\n",
		"hello.sh":  "echo hi\n",
	})
	p := writeCommands(t, dir, validCommands)
	s, err := New(p, root)
	if err != nil {
		t.Fatal(err)
	}
	if s.Count() != 3 {
		t.Fatalf("want 3 commands, got %d", s.Count())
	}
	cmd, ok := s.Lookup("Status")
	if !ok || cmd.Script != "status.sh" || cmd.TimeoutSec != 15 {
		t.Fatalf("lookup failed: %+v %v", cmd, ok)
	}
	photo, _ := s.Lookup("Photo")
	if !photo.Img || photo.Template == "" {
		t.Fatalf("img/template not loaded: %+v", photo)
	}
	if _, ok := s.Lookup("  Status  "); !ok {
		t.Fatal("lookup should trim surrounding whitespace")
	}
	if _, ok := s.Lookup("nope"); ok {
		t.Fatal("unknown text must not match")
	}
	if p, err := s.ResolveScript(cmd); err != nil || filepath.Base(p) != "status.sh" {
		t.Fatalf("ResolveScript: %v %v", p, err)
	}
}

func TestLoadRejectsCorrupt(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.sh": "echo a\n"})
	p := writeCommands(t, dir, `{not json`)
	if _, err := New(p, root); err == nil {
		t.Fatal("corrupt file must be rejected")
	}
}

func TestLoadRejectsDuplicates(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.sh": "echo a\n"})
	p := writeCommands(t, dir, `{
	  "version": 2,
	  "commands": [
	    {"text": "A", "script": "a.sh"},
	    {"text": "A", "script": "a.sh"}
	  ]
	}`)
	if _, err := New(p, root); err == nil {
		t.Fatal("duplicate command texts must be rejected")
	}
}

func TestLoadRejectsToolongText(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.sh": "echo a\n"})
	long := string(make([]byte, 65))
	p := writeCommands(t, dir, `{
	  "version": 2,
	  "commands": [{"text": "`+long+`", "script": "a.sh"}]
	}`)
	if _, err := New(p, root); err == nil {
		t.Fatal("text longer than 64 bytes must be rejected")
	}
}

func TestLoadRejectsMissingScript(t *testing.T) {
	dir, root := setup(t, map[string]string{})
	p := writeCommands(t, dir, `{
	  "version": 2,
	  "commands": [{"text": "A", "script": "ghost.sh"}]
	}`)
	if _, err := New(p, root); err == nil {
		t.Fatal("a registered script that does not exist must be rejected")
	}
}

func TestLoadRejectsNonShScript(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.py": "print(1)\n"})
	p := writeCommands(t, dir, `{
	  "version": 2,
	  "commands": [{"text": "A", "script": "a.py"}]
	}`)
	if _, err := New(p, root); err == nil {
		t.Fatal("scripts must end in .sh")
	}
}

func TestLoadRejectsAbsoluteScript(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.sh": "echo a\n"})
	p := writeCommands(t, dir, `{
	  "version": 2,
	  "commands": [{"text": "A", "script": "/etc/evil.sh"}]
	}`)
	if _, err := New(p, root); err == nil {
		t.Fatal("absolute script paths must be rejected")
	}
}

func TestLoadRejectsEscapingScript(t *testing.T) {
	dir, root := setup(t, map[string]string{"../../evil.sh": ""})
	// even though the file exists outside the root, traversal must be rejected
	p := writeCommands(t, dir, `{
	  "version": 2,
	  "commands": [{"text": "A", "script": "../evil.sh"}]
	}`)
	if _, err := New(p, root); err == nil {
		t.Fatal("scripts escaping the commands dir must be rejected")
	}
}

func TestLoadRejectsTemplateWithUnknownPlaceholder(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.sh": "echo a\n"})
	p := writeCommands(t, dir, `{
	  "version": 2,
	  "commands": [{"text": "A", "script": "a.sh", "template": "load: ${load}"}]
	}`)
	if _, err := New(p, root); err == nil {
		t.Fatal("template may only reference ${output}")
	}
}

func TestStoreAcceptsImgFlag(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.sh": "echo x\n"})
	p := writeCommands(t, dir, `{
	  "version": 2,
	  "commands": [{"text": "A", "script": "a.sh", "img": true}]
	}`)
	s, err := New(p, root)
	if err != nil {
		t.Fatal(err)
	}
	cmd, _ := s.Lookup("A")
	if !cmd.Img {
		t.Fatal("img flag must be persisted")
	}
}

func TestLoadKeepsPreviousStateOnError(t *testing.T) {
	dir, root := setup(t, map[string]string{
		"status.sh": "echo up\n",
		"photo.sh":  "echo /tmp/x.png\n",
		"hello.sh":  "echo hi\n",
	})
	p := writeCommands(t, dir, validCommands)
	s, err := New(p, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{corrupt`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Load(); err == nil {
		t.Fatal("reload of corrupt file must fail")
	}
	if s.Count() == 0 {
		t.Fatal("previous state must be kept on reload failure")
	}
}

func TestValidText(t *testing.T) {
	bad := []string{"", "  spaced  ", string(byte(7)), "a\nb", "x\x7f", string(make([]byte, 65))}
	for _, b := range bad {
		if ValidText(b) {
			t.Errorf("ValidText(%q) = true, want false", b)
		}
	}
	good := []string{"System Status", "/shutdown", "🔒 Lock PC", "A1-b2_c3"}
	for _, g := range good {
		if !ValidText(g) {
			t.Errorf("ValidText(%q) = false, want true", g)
		}
	}
}

const validMenuCommands = `{
  "version": 2,
  "commands": [
    {"text": "Status", "script": "status.sh"},
    {"text": "Change Workspace", "menu": {
      "id": "change-workspace",
      "prompt": "Select Workspace:",
      "script": "workspace.sh",
      "options": [
        {"label": "1"},
        {"label": "3"},
        {"label": "Custom", "value": "9", "script": "ws-custom.sh"}
      ]
    }, "timeout_sec": 10}
  ]
}`

func TestLoadMenusValid(t *testing.T) {
	dir, root := setup(t, map[string]string{
		"status.sh":    "echo up\n",
		"workspace.sh": "echo ws $1\n",
		"ws-custom.sh": "echo custom\n",
	})
	p := writeCommands(t, dir, validMenuCommands)
	s, err := New(p, root)
	if err != nil {
		t.Fatal(err)
	}
	cmd, ok := s.Lookup("Change Workspace")
	if !ok || cmd.Menu == nil {
		t.Fatalf("menu command not found: %+v", cmd)
	}
	if cmd.Menu.ID != "change-workspace" || len(cmd.Menu.Options) != 3 {
		t.Fatalf("menu not loaded correctly: %+v", cmd.Menu)
	}
	byID, ok := s.LookupMenu("change-workspace")
	if !ok || byID.Text != "Change Workspace" {
		t.Fatalf("LookupMenu failed: %+v %v", byID, ok)
	}
	if _, ok := s.LookupMenu("nope"); ok {
		t.Fatal("unknown menu id must not match")
	}
	// menu command resolves as script command only via its own paths
	plain, _ := s.Lookup("Status")
	if p, err := s.ResolveScript(plain); err != nil || filepath.Base(p) != "status.sh" {
		t.Fatalf("ResolveScript: %v %v", p, err)
	}
	if p, err := s.ResolveScriptPath("workspace.sh"); err != nil || filepath.Base(p) != "workspace.sh" {
		t.Fatalf("ResolveScriptPath: %v %v", p, err)
	}
}

func TestMenuRequiresSharedHandlerOrPerOptionScripts(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.sh": "echo a\n"})
	p := writeCommands(t, dir, `{
	  "version": 2,
	  "commands": [{"text": "M", "menu": {
	    "id": "m",
	    "prompt": "Pick:",
	    "options": [{"label": "A"}]
	  }}]
	}`)
	if _, err := New(p, root); err == nil {
		t.Fatal("menu without handler and an option without own script must be rejected")
	}
}

func TestMenuRejectsDuplicateIDs(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.sh": "echo a\n"})
	p := writeCommands(t, dir, `{
	  "version": 2,
	  "commands": [
	    {"text": "M1", "menu": {"id": "same", "options": [{"label": "A", "script": "a.sh"}]}},
	    {"text": "M2", "menu": {"id": "same", "options": [{"label": "B", "script": "a.sh"}]}}
	  ]
	}`)
	if _, err := New(p, root); err == nil {
		t.Fatal("duplicate menu ids must be rejected")
	}
}

func TestMenuAcceptsEmptyOptions(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.sh": "echo a\n"})
	p := writeCommands(t, dir, `{
	  "version": 2,
	  "commands": [{"text": "M", "menu": {"id": "m", "script": "a.sh", "options": []}}]
	}`)
	if _, err := New(p, root); err != nil {
		t.Fatalf("menu with no options yet (mid-construction) should load fine: %v", err)
	}
}

func TestMenuRejectsDuplicateOptionLabels(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.sh": "echo a\n"})
	p := writeCommands(t, dir, `{
	  "version": 2,
	  "commands": [{"text": "M", "menu": {
	    "id": "m", "script": "a.sh",
	    "options": [{"label": "A"}, {"label": "A"}]
	  }}]
	}`)
	if _, err := New(p, root); err == nil {
		t.Fatal("duplicate option labels must be rejected")
	}
}

func TestMenuRejectsBadID(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.sh": "echo a\n"})
	for _, id := range []string{"Upper", "with space", ""} {
		p := writeCommands(t, dir, `{
		  "version": 2,
		  "commands": [{"text": "M", "menu": {
		    "id": "`+id+`", "script": "a.sh",
		    "options": [{"label": "A"}]
		  }}]
		}`)
		if _, err := New(p, root); err == nil {
			t.Fatalf("menu id %q must be rejected", id)
		}
	}
}

func TestMenuRejectsBothScriptAndMenu(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.sh": "echo a\n"})
	p := writeCommands(t, dir, `{
	  "version": 2,
	  "commands": [{"text": "M", "script": "a.sh", "menu": {
	    "id": "m", "script": "a.sh",
	    "options": [{"label": "A"}]
	  }}]
	}`)
	if _, err := New(p, root); err == nil {
		t.Fatal("command with both script and menu must be rejected")
	}
}

func TestMenuRejectsTemplateAndImg(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.sh": "echo a\n"})
	for _, extra := range []string{`"template": "x: ${output}"`, `"img": true`} {
		p := writeCommands(t, dir, `{
		  "version": 2,
		  "commands": [{"text": "M", `+extra+`, "menu": {
		    "id": "m", "script": "a.sh",
		    "options": [{"label": "A"}]
		  }}]
		}`)
		if _, err := New(p, root); err == nil {
			t.Fatalf("menu command with %s must be rejected", extra)
		}
	}
}

func TestMenuRejectsMissingOptionScript(t *testing.T) {
	dir, root := setup(t, map[string]string{"a.sh": "echo a\n"})
	p := writeCommands(t, dir, `{
	  "version": 2,
	  "commands": [{"text": "M", "menu": {
	    "id": "m", "script": "a.sh",
	    "options": [{"label": "A"}, {"label": "B", "script": "ghost.sh"}]
	  }}]
	}`)
	if _, err := New(p, root); err == nil {
		t.Fatal("option referencing a missing script must be rejected")
	}
}
