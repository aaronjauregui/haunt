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

// TestClickStormDoesNotCrash hammers the sprite with rapid clicks. Every
// click makes the brain swap the balloon (create+destroy surfaces from the
// intent goroutine) while pointer events dispatch concurrently — the exact
// interleaving that crashed the unpatched wayland client with a concurrent
// map fault.
func TestClickStormDoesNotCrash(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "whisper")
	writeGhost(t, gDir, `[ghost]
name = "whisper"

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	cmd := exec.Command(bin, "run", "-x", "100", "-y", "100", "-state", filepath.Join(dir, "state"), gDir)
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
	if err := ptr.MoveTo(180, 180); err != nil { // sprite center
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		if err := ptr.Press(); err != nil {
			t.Fatal(err)
		}
		if err := ptr.Release(); err != nil {
			t.Fatal(err)
		}
	}

	// The ghost must still be alive and responsive.
	time.Sleep(300 * time.Millisecond)
	if cmd.ProcessState != nil {
		t.Fatal("ghost process died during click storm")
	}
	waitBalloon(t, c, dir, 100, 100, true)

	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("ghost exited uncleanly after click storm: %v", err)
	}
}
