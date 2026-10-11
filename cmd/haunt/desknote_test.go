package main

import (
	"image"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/store"
	"github.com/aaronjauregui/haunt/internal/testenv"
)

// A note end to end, in headless sway: the model leaves one, a paper
// appears beside the ghost, it is dragged, the ghost restarts and it is
// back where it was dropped, and a click takes it down and off the list.
func TestADeskNoteOnScreen(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rig := newBrainRig(t, dir, rules(
		[]fake.Rule{silentOn("[haunt event]")},
		[]fake.Rule{
			{Match: "leave me a note", AfterTool: "Left", Calls: []fake.Call{{Name: "stay_silent"}}},
			{Match: "leave me a note", Calls: []fake.Call{decision("leave_note",
				map[string]any{"text": "water the plants"})}},
		},
		says("", "I hear you."),
	), "")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[persona]
system = "You are Wisp."
`)
	stateDir := filepath.Join(dir, "state")
	cmd, _ := rig.start(t, c, gDir, stateDir)
	rig.say(t, "leave me a note to water the plants")

	n := waitNote(t, stateDir, func(n *ghost.DeskNote) bool { return n != nil && n.Output != "" })
	t.Logf("note placed on %s at (%d,%d)", n.Output, n.X, n.Y)
	waitPaper(t, c, dir, n.X+4, n.Y+4, true)

	// Dragged, it stays where it was dropped, and the store says so.
	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	defer ptr.Close()
	if err := ptr.Drag(n.X+8, n.Y+8, n.X+8+200, n.Y+8+150); err != nil {
		t.Fatal(err)
	}
	moved := waitNote(t, stateDir, func(m *ghost.DeskNote) bool { return m != nil && m.X == n.X+200 && m.Y == n.Y+150 })
	waitPaper(t, c, dir, moved.X+4, moved.Y+4, true)

	// A restart puts it back there.
	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()
	waitPaper(t, c, dir, moved.X+4, moved.Y+4, false)
	rig.start(t, c, gDir, stateDir)
	waitPaper(t, c, dir, moved.X+4, moved.Y+4, true)

	// A click takes it down, and it is no longer held.
	if err := ptr.MoveTo(moved.X+8, moved.Y+8); err != nil {
		t.Fatal(err)
	}
	if err := ptr.Press(); err != nil {
		t.Fatal(err)
	}
	if err := ptr.Release(); err != nil {
		t.Fatal(err)
	}
	waitNote(t, stateDir, func(m *ghost.DeskNote) bool { return m == nil })
	waitPaper(t, c, dir, moved.X+4, moved.Y+4, false)

	out, err := exec.Command(rig.Bin, "journal", "-state", stateDir, "-notes").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "(it has left no notes)") {
		t.Errorf("journal -notes after the click: %v\n%s", err, out)
	}
}

// waitNote polls the ghost's store until its one note, or nil for none,
// satisfies ok.
func waitNote(t *testing.T, stateDir string, ok func(*ghost.DeskNote) bool) ghost.DeskNote {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var note *ghost.DeskNote
		if s, err := store.Open(filepath.Join(stateDir, "state.db")); err == nil {
			raw, found, _ := s.Get(ghost.IntentionsStoreKey)
			s.Close()
			if found {
				list, _ := ghost.DecodeIntentions(raw)
				for _, it := range list {
					if it.Note != nil {
						note = it.Note
					}
				}
			}
		}
		if ok(note) {
			if note == nil {
				return ghost.DeskNote{}
			}
			return *note
		}
		if time.Now().After(deadline) {
			t.Fatalf("the note never got there; last %+v", note)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitPaper polls screenshots until the default note paper is, or is not,
// at (x, y).
func waitPaper(t *testing.T, c *testenv.Compositor, dir string, x, y int, want bool) {
	t.Helper()
	path := filepath.Join(dir, "note-poll.png")
	deadline := time.Now().Add(10 * time.Second)
	for {
		found := paperAt(shot(t, c, path), x, y)
		if found == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("paper at (%d,%d) = %v, want %v", x, y, found, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// paperAt reports whether the pixel is balloon.NoteTheme's light sheet.
func paperAt(img image.Image, x, y int) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	return r>>8 > 235 && g>>8 > 228 && b>>8 > 195 && b>>8 < 240
}
