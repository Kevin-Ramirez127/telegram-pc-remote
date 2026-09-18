package actions

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"telegram-pc-remote/internal/store"
)

// 1x1 transparent PNG.
const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// setupScript writes a script file into a fresh commands dir and returns the
// ExecuteOpts pointing at it.
func setupScript(t *testing.T, body string) (store.Command, ExecuteOpts) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "cmd.sh")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return store.Command{Text: "T", Script: "cmd.sh"}, ExecuteOpts{ScriptPath: path, WorkDir: root}
}

func TestScriptTextOutput(t *testing.T) {
	cmd, opts := setupScript(t, "#!/usr/bin/env bash\necho hello-script\n")
	out, err := Execute(context.Background(), cmd, opts)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "hello-script" {
		t.Fatalf("want script output, got %q", out.Text)
	}
	if out.Photo != nil {
		t.Fatal("text command must not produce a photo")
	}
}

func TestTemplateSubstitutionAndEscapes(t *testing.T) {
	cmd, opts := setupScript(t, "echo 28%")
	cmd.Template = "Disk usage: ${output}\\nChecked."
	out, err := Execute(context.Background(), cmd, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := "Disk usage: 28%\nChecked."
	if out.Text != want {
		t.Fatalf("want %q, got %q", want, out.Text)
	}
}

func TestScriptFailure(t *testing.T) {
	cmd, opts := setupScript(t, "echo partial-output\nexit 3\n")
	_, err := Execute(context.Background(), cmd, opts)
	if err == nil {
		t.Fatal("non-zero exit must produce an error")
	}
}

func TestScriptTimeout(t *testing.T) {
	start := time.Now()
	cmd, opts := setupScript(t, "sleep 30\n")
	cmd.TimeoutSec = 1
	_, err := Execute(context.Background(), cmd, opts)
	if err == nil {
		t.Fatal("timed-out script must produce an error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestScriptEnvIsMinimal(t *testing.T) {
	cmd, opts := setupScript(t, `echo "${HOME:-UNSET}"`)
	out, err := Execute(context.Background(), cmd, opts)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "UNSET" {
		t.Fatalf("HOME should not be inherited, got %q", out.Text)
	}
}

func TestScriptRunsInWorkDir(t *testing.T) {
	dir := t.TempDir()
	opts := ExecuteOpts{ScriptPath: filepath.Join(dir, "x.sh"), WorkDir: dir}
	cmd := store.Command{Text: "T", Script: "x.sh"}
	if err := os.WriteFile(filepath.Join(dir, "x.sh"), []byte("echo $(cat probe.txt)\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "probe.txt"), []byte("relative-ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := Execute(context.Background(), cmd, opts)
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "relative-ok" {
		t.Fatalf("script should run with cwd=commands dir, got %q", out.Text)
	}
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(tinyPNG)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestImageCommand(t *testing.T) {
	root := t.TempDir()
	imgPath := filepath.Join(root, "shot.png")
	if err := os.WriteFile(imgPath, pngBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "shot.sh")
	if err := os.WriteFile(script, []byte("echo "+imgPath+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := store.Command{Text: "Shot", Script: "shot.sh", Img: true, Template: "📸 Captured"}
	out, err := Execute(context.Background(), cmd, ExecuteOpts{ScriptPath: script, WorkDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if out.Photo == nil || !bytes.Equal(out.Photo, pngBytes(t)) {
		t.Fatal("image bytes must match the printed file")
	}
	if out.Text != "📸 Captured" {
		t.Fatalf("template should become the caption, got %q", out.Text)
	}
}

func TestImageCommandRelativePath(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pic.webp"), pngBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "w.sh")
	if err := os.WriteFile(script, []byte("echo pic.webp\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := store.Command{Text: "W", Script: "w.sh", Img: true}
	out, err := Execute(context.Background(), cmd, ExecuteOpts{ScriptPath: script, WorkDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if out.Photo == nil {
		t.Fatal("relative image path should resolve against the commands dir")
	}
}

func TestImageRejectsNonImage(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "bad.sh")
	if err := os.WriteFile(script, []byte("echo not-an-image\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := store.Command{Text: "B", Script: "bad.sh", Img: true}
	_, err := Execute(context.Background(), cmd, ExecuteOpts{ScriptPath: script, WorkDir: root})
	if err == nil {
		t.Fatal("a printed non-image path must be rejected by magic-byte sniffing")
	}
}

func TestImageRejectsMissingFile(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(root, "m.sh")
	if err := os.WriteFile(script, []byte("echo does-not-exist.png\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := store.Command{Text: "M", Script: "m.sh", Img: true}
	_, err := Execute(context.Background(), cmd, ExecuteOpts{ScriptPath: script, WorkDir: root})
	if err == nil {
		t.Fatal("a printed path to a missing file must be rejected")
	}
}

func TestImageRejectsOversize(t *testing.T) {
	root := t.TempDir()
	big := filepath.Join(root, "big.png")
	if err := os.WriteFile(big, pngBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	// Grow it past the limit with trailing junk (sparse enough to be fast).
	f, err := os.OpenFile(big, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(store.MaxImageBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	script := filepath.Join(root, "o.sh")
	if err := os.WriteFile(script, []byte("echo big.png\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := store.Command{Text: "O", Script: "o.sh", Img: true}
	_, err = Execute(context.Background(), cmd, ExecuteOpts{ScriptPath: script, WorkDir: root})
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized image must be rejected, got %v", err)
	}
}
