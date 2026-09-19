package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"telegram-pc-remote/internal/config"
	"telegram-pc-remote/internal/store"
)

func TestOptionDataRoundTrip(t *testing.T) {
	data := encodeOptionData("change-workspace", 3)
	if got, idx, ok := parseOptionData(data); !ok || got != "change-workspace" || idx != 3 {
		t.Fatalf("round trip failed: %q => %q %d %v", data, got, idx, ok)
	}
	if len(data) > 64 {
		t.Fatalf("callback data exceeds Telegram's 64-byte limit: %d bytes", len(data))
	}
}

func TestParseOptionDataRejectsGarbage(t *testing.T) {
	bad := []string{
		"",
		"Change Workspace", // plain button text
		"m\x1fonly-one",
		"m\x1fa\x1fabc",
		"m\x1fa\x1f-1",
		"x\x1fid\x1f0",
		"m\x1f\x1f\x1f",
	}
	for _, b := range bad {
		if _, _, ok := parseOptionData(b); ok {
			t.Errorf("parseOptionData(%q) should fail", b)
		}
	}
}

func TestOptionRowsWrapping(t *testing.T) {
	m := &store.Menu{ID: "ws", Options: make([]store.MenuOption, 6)}
	for i := range m.Options {
		m.Options[i].Label = string(rune('A' + i))
	}
	rows := optionRows(m, "ignored")
	if len(rows) != 2 {
		t.Fatalf("6 options should wrap into 2 rows, got %d", len(rows))
	}
	if len(rows[0]) != store.MaxButtonsPerRow || len(rows[1]) != 1 {
		t.Fatalf("row sizes wrong: %d/%d", len(rows[0]), len(rows[1]))
	}
	if data := *rows[1][0].CallbackData; data != encodeOptionData("ws", 5) {
		t.Fatalf("last button callback wrong: %q", data)
	}
	if _, idx, ok := parseOptionData(*rows[1][0].CallbackData); !ok || idx != 5 {
		t.Fatalf("last button parses wrong: %d %v", idx, ok)
	}
}

func TestOptionDataWithLongMenuID(t *testing.T) {
	id := strings.Repeat("a", store.MaxMenuIDBytes)
	data := encodeOptionData(id, 49) // max options index
	if len(data) > 64 {
		t.Fatalf("max-size callback exceeds 64 bytes: %d", len(data))
	}
	got, idx, ok := parseOptionData(data)
	if !ok || got != id || idx != 49 {
		t.Fatalf("parsing failed for max-size callback")
	}
}

func TestOptionRowsSingleRow(t *testing.T) {
	m := &store.Menu{ID: "ws", Options: make([]store.MenuOption, 5)}
	rows := optionRows(m, "ignored")
	if len(rows) != 1 || len(rows[0]) != 5 {
		t.Fatalf("5 options should fit on one row")
	}
}

func TestResolveOption(t *testing.T) {
	menu := &store.Menu{Script: "handler.sh"}
	cases := []struct {
		name       string
		opt        store.MenuOption
		wantScript string
		wantValue  string
	}{
		{"own script wins", store.MenuOption{Label: "A", Script: "a.sh", Value: "9"}, "a.sh", "9"},
		{"shared handler fallback", store.MenuOption{Label: "B"}, "handler.sh", "B"},
		{"value defaults to label", store.MenuOption{Label: "3"}, "handler.sh", "3"},
		{"own script with label-only value", store.MenuOption{Label: "Custom", Script: "c.sh"}, "c.sh", "Custom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script, value := resolveOption(menu, tc.opt)
			if script != tc.wantScript || value != tc.wantValue {
				t.Fatalf("resolveOption = (%q, %q), want (%q, %q)", script, value, tc.wantScript, tc.wantValue)
			}
		})
	}
}

// --- fake Telegram server ----------------------------------------------------

type tgCall struct {
	method string
	form   url.Values
}

type fakeTG struct {
	mu    sync.Mutex
	calls []tgCall
}

func newFakeTG(t *testing.T) (*fakeTG, *httptest.Server) {
	t.Helper()
	f := &fakeTG{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		method := filepath.Base(r.URL.Path)
		f.mu.Lock()
		f.calls = append(f.calls, tgCall{method: method, form: r.PostForm})
		f.mu.Unlock()
		switch method {
		case "getMe":
			fmt.Fprint(w, `{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"test","username":"testbot"}}`)
		case "answerCallbackQuery":
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":1,"type":"private"}}}`)
		}
	}))
	return f, srv
}

func (f *fakeTG) callsFrom(offset int) []tgCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[offset:]
}

// messageCalls returns the sendMessage calls in the given window as
// (text, replyMarkupJSON) pairs, skipping answerCallbackQuery noise.
func messageCalls(calls []tgCall) []struct {
	text   string
	markup string
} {
	out := make([]struct{ text, markup string }, 0, len(calls))
	for _, c := range calls {
		if c.method != "sendMessage" {
			continue
		}
		out = append(out, struct {
			text   string
			markup string
		}{text: c.form.Get("text"), markup: c.form.Get("reply_markup")})
	}
	return out
}

// callbacksOf parses an inline keyboard's callback_data values.
func callbacksOf(markupJSON string) []string {
	if markupJSON == "" {
		return nil
	}
	var kb struct {
		InlineKeyboard [][]struct {
			CallbackData *string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(markupJSON), &kb); err != nil {
		return nil
	}
	var out []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.CallbackData != nil {
				out = append(out, *b.CallbackData)
			}
		}
	}
	return out
}

// TestRunMenuOptionResendsAndNavigates is the integration test for the
// requested behavior:
//   - a script option: the reply is sent, then the GENERAL menu (/menu, all
//     non-hidden commands) is re-sent so the user is back at the top level;
//   - a navigation option (menu_id): the nested menu's options are shown and
//     nothing is re-sent — the general menu only comes back after a final
//     (leaf) script has run and replied;
//   - a plain top-level script command (e.g. report.sh): same as a script
//     option — reply, then the GENERAL menu is re-sent.
func TestRunMenuOptionResendsAndNavigates(t *testing.T) {
	fake, srv := newFakeTG(t)
	defer srv.Close()

	bot, err := tgbotapi.NewBotAPIWithClient("123:test", srv.URL+"/bot%s/%s", srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	scripts := filepath.Join(root, "commands")
	if err := os.MkdirAll(scripts, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "status.sh"), []byte("echo ran:$1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "report.sh"), []byte("echo report:ok\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	commandsFile := filepath.Join(root, "commands.json")
	seed := `{
  "version": 2,
  "commands": [
    {"text": "Media", "menu": {"id": "media", "prompt": "Media actions:", "script": "status.sh",
      "options": [{"label": "Volume", "menu_id": "volume"}, {"label": "Static", "script": "status.sh"}]}},
    {"text": "Volume Menu", "hidden": true, "menu": {"id": "volume", "prompt": "Volume:", "script": "status.sh",
      "options": [{"label": "+", "value": "+2"}, {"label": "-", "value": "-2"}]}},
    {"text": "Report", "script": "report.sh"}
  ]
}`
	if err := os.WriteFile(commandsFile, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.New(commandsFile, scripts)
	if err != nil {
		t.Fatal(err)
	}
	svc := &service{
		bot:   bot,
		cfg:   &config.Config{CommandsDir: scripts, MaxCommandDuration: 30 * time.Second},
		store: st,
	}

	// 1) Script option in "Media" (Static): reply, then the GENERAL menu again.
	before := len(fake.callsFrom(0))
	svc.runMenuOption(1, encodeOptionData("media", 1))
	msgs := messageCalls(fake.callsFrom(before))
	if len(msgs) != 2 {
		t.Fatalf("script option should produce reply + re-sent general menu, got %d messages: %+v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0].text, "ran:Static") {
		t.Fatalf("reply should contain the script output, got %q", msgs[0].text)
	}
	if msgs[1].text != "Pick a command:" {
		t.Fatalf("after the reply the GENERAL menu should be re-sent, got %q", msgs[1].text)
	}
	if cb := callbacksOf(msgs[1].markup); len(cb) != 2 || cb[0] != "Media" || cb[1] != "Report" {
		t.Fatalf("general menu should list only non-hidden commands (Volume Menu is hidden), got %v", cb)
	}

	// 2) Navigation option in "Media" (Volume): shows the nested menu, parent
	//    and general menu NOT re-sent → exactly one message, the nested menu.
	before = len(fake.callsFrom(0))
	svc.runMenuOption(1, encodeOptionData("media", 0))
	msgs = messageCalls(fake.callsFrom(before))
	if len(msgs) != 1 {
		t.Fatalf("navigation option should send exactly the nested menu (no re-send), got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].text != "Volume:" {
		t.Fatalf("nested prompt expected, got %q", msgs[0].text)
	}
	cb := callbacksOf(msgs[0].markup)
	if len(cb) != 2 || !strings.HasPrefix(cb[0], optionDataPrefix+"volume") || !strings.HasPrefix(cb[1], optionDataPrefix+"volume") {
		t.Fatalf("nested options should target the volume menu, got %v", cb)
	}

	// 3) Script option in the nested "Volume" menu: reply carrying the
	//    option's value, then the GENERAL menu re-sent (flow ended).
	before = len(fake.callsFrom(0))
	svc.runMenuOption(1, encodeOptionData("volume", 0))
	msgs = messageCalls(fake.callsFrom(before))
	if len(msgs) != 2 {
		t.Fatalf("nested script option should produce reply + re-sent general menu, got %d: %+v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0].text, "ran:+2") {
		t.Fatalf("option value should reach the handler ($1), got %q", msgs[0].text)
	}
	if msgs[1].text != "Pick a command:" {
		t.Fatalf("after the final script the GENERAL menu should be re-sent, got %q", msgs[1].text)
	}
	if cb := callbacksOf(msgs[1].markup); len(cb) != 2 || cb[0] != "Media" || cb[1] != "Report" {
		t.Fatalf("re-sent general menu should list only non-hidden commands, got %v", cb)
	}

	// 4) Plain script command (e.g. report.sh, a top-level button): the reply
	//    is sent, then the GENERAL menu is re-sent again.
	before = len(fake.callsFrom(0))
	svc.runCommand("Report", 1)
	msgs = messageCalls(fake.callsFrom(before))
	if len(msgs) != 2 {
		t.Fatalf("plain script command should produce reply + re-sent general menu, got %d: %+v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0].text, "report:ok") {
		t.Fatalf("reply should contain the script output, got %q", msgs[0].text)
	}
	if msgs[1].text != "Pick a command:" {
		t.Fatalf("after a plain script command the GENERAL menu should be re-sent, got %q", msgs[1].text)
	}
	if cb := callbacksOf(msgs[1].markup); len(cb) != 2 || cb[0] != "Media" || cb[1] != "Report" {
		t.Fatalf("re-sent general menu should list only non-hidden commands, got %v", cb)
	}
}
