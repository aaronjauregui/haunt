package main

import (
	"bytes"
	"encoding/json"
	"os"
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

// `haunt journal -diary` prints the diary file as it is, and says when
// there is none.
func TestJournalDiary(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := printDiary(&out, dir); err != nil || out.String() != "(the diary is empty)\n" {
		t.Errorf("no diary: %q %v", out.String(), err)
	}
	diary := "# Diary\n\n## 2026-10-11 Sunday\nWe talked about the oven.\n\n<!-- self t=9 2026-10-12 -->\n"
	if err := os.MkdirAll(filepath.Join(dir, "memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memory", "diary.md"), []byte(diary), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := printDiary(&out, dir); err != nil || out.String() != diary {
		t.Errorf("the diary printed as %q (%v)", out.String(), err)
	}
}
