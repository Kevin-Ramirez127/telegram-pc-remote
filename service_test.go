package main

import (
	"strings"
	"testing"

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
