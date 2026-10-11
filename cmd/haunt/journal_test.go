package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/store"
)

// `haunt journal -mood` says when there is none, prints the line and when
// it was set, and marks one from an earlier day as cleared.
func TestJournalMood(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 10, 11, 9, 12, 0, 0, time.Local)
	var out bytes.Buffer
	if err := printMood(&out, s, at); err != nil || out.String() != "(no mood set)\n" {
		t.Errorf("no mood: %q %v", out.String(), err)
	}
	raw, _ := json.Marshal(ghost.Mood{Text: "a little wistful", At: at})
	if err := s.Set(ghost.MoodStoreKey, string(raw)); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := printMood(&out, s, at.Add(10*time.Hour)); err != nil ||
		out.String() != "a little wistful\n  set 2026-10-11 09:12\n" {
		t.Errorf("today: %q %v", out.String(), err)
	}
	out.Reset()
	if err := printMood(&out, s, at.Add(24*time.Hour)); err != nil ||
		out.String() != "a little wistful\n  set 2026-10-11 09:12, an earlier day; it has cleared\n" {
		t.Errorf("an earlier day: %q %v", out.String(), err)
	}
}
