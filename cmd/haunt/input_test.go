package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// caretBlinks is the input caret's blink period, mirrored from
// internal/ghost so the timing here follows it.
const caretBlinks = 500 * time.Millisecond

// TestTypeAtGhost: right-clicking the sprite opens a line to type at, and
// the ghost holds the keyboard only while it is open. Whisper is used
// deliberately — no AI backend involved, and its left-click quip must still
// work, since trading a scripted ghost's reaction for an input box would be
// a poor bargain.
func TestTypeAtGhost(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "whisper")
	writeGhost(t, gDir, `[ghost]
name = "whisper"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	record := filepath.Join(dir, "session.jsonl")
	const spriteX, spriteY = 200, 200
	cmd := exec.Command(bin, "run", "-x", "200", "-y", "200",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	// HAUNT_DEBUG makes wlclient log every surface it creates.
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_DEBUG=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	})
	waitFor(t, stdout, "READY")

	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	defer ptr.Close()

	// Aim at an opaque pixel near the sprite's middle.
	if err := ptr.MoveTo(spriteX+80, spriteY+80); err != nil {
		t.Fatal(err)
	}

	// Right-click opens the input line. Assert on the input panel's own
	// fixed width rather than "a balloon appeared": whisper's boot greeting
	// is a balloon too, and an earlier version of this test was satisfied
	// by it while the input line never opened at all.
	if err := ptr.RightClick(); err != nil {
		t.Fatal(err)
	}
	waitLog(t, stderr, "surface create 320x")
	if !balloonVisible(t, c, dir, spriteX, spriteY) {
		t.Error("the input line was created but is not on screen")
	}
	if data, _ := os.ReadFile(record); len(data) > 0 {
		// The input line is the driver's, not the brain's: no click event
		// is fabricated for it.
		if n := countOccurrences(string(data), `"type":"click"`); n != 0 {
			t.Errorf("right-click produced %d click events, want 0", n)
		}
	}

	// The caret blinks for a while. It must repaint the panel in place: a
	// surface recreated per blink is what flickering looks like, and
	// screenshots are too coarse to catch the gap, so count the surfaces.
	// Sampling for a visible gap here passes even with the bug, which is
	// why this counts instead.
	before := countOccurrences(stderr.String(), "surface create")
	time.Sleep(4 * caretBlinks)
	created := countOccurrences(stderr.String(), "surface create") - before
	if created != 0 {
		t.Errorf("%d surfaces created while the caret blinked; the input line is flickering", created)
	}
	if !balloonVisible(t, c, dir, spriteX, spriteY) {
		t.Error("input panel disappeared while blinking")
	}

	// The left click still belongs to the ghost.
	if err := ptr.MoveTo(spriteX+80, spriteY+80); err != nil {
		t.Fatal(err)
	}
	if err := ptr.Press(); err != nil {
		t.Fatal(err)
	}
	if err := ptr.Release(); err != nil {
		t.Fatal(err)
	}
	waitRecordContains(t, record, `"type":"click"`)

	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("ghost exited uncleanly: %v", err)
	}
}

func countOccurrences(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
		}
	}
	return n
}
