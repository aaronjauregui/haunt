package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

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
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
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

	// Right-click opens the input line: a balloon appears without the brain
	// having said anything.
	if err := ptr.RightClick(); err != nil {
		t.Fatal(err)
	}
	waitBalloon(t, c, dir, spriteX, spriteY, true)
	if data, _ := os.ReadFile(record); len(data) > 0 {
		// The input line is the driver's, not the brain's: no click event
		// is fabricated for it.
		if n := countOccurrences(string(data), `"type":"click"`); n != 0 {
			t.Errorf("right-click produced %d click events, want 0", n)
		}
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
