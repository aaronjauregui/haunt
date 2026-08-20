package main

import (
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestShortBlinkReachesTheScreen: a blink shaped the way an imported
// ukagaka shell's is — a brief overlay over a body, resting on a 1x1
// transparent variant, visible for a fraction of the clip — must actually
// appear on screen.
//
// It is its own test rather than a case in TestClipAnimates because the two
// fail for different reasons. That one flickers a big patch every 250ms and
// proves the render path exists at all. This one is about the *short* end:
// a converted blink shows its changed frame for 50ms, and everything
// between the clip's clock and the compositor's has to be prompt enough for
// that to land. Sampling has to be faster than the thing being sampled.
func TestShortBlinkReachesTheScreen(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "blinker")
	if err := os.MkdirAll(gDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The structure a converted shell has: a body, and an overlay layer
	// whose resting variant is a single transparent pixel.
	writeLayer(t, gDir, "body.png", 120, 120, color.NRGBA{G: 200, A: 255})
	writeLayer(t, gDir, "shut.png", 40, 40, color.NRGBA{R: 220, A: 255})
	writeLayer(t, gDir, "none.png", 1, 1, color.NRGBA{})
	writeGhost(t, gDir, `[ghost]
name = "blinker"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"

[[shell.layer]]
name = "body"
image = "body.png"
z = 0

[[shell.layer]]
name = "eyes"
z = 10
at = [40, 40]
default = "none"
variants = { none = "none.png", shut = "shut.png" }

[[shell.clip]]
name = "blink"
trigger = "sometimes"
every_ms = 700
keys = [
  { at = 50, set = { eyes = "none" } },
  { at = 100, set = { eyes = "shut" } },
  { at = 150, set = { eyes = "none" } },
]
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	const spriteX, spriteY = 200, 200
	cmd := exec.Command(bin, "run", "-x", "200", "-y", "200",
		"-state", filepath.Join(dir, "state"), gDir)
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

	// The overlay sits at (40,40) and is 40x40, so its middle is +60,+60
	// from the sprite's corner — the same spot TestClipAnimates watches.
	shut := 0
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) && shut == 0 {
		if patchColour(t, c, dir, spriteX+60, spriteY+60) == "red" {
			shut++
		}
	}
	if shut == 0 {
		t.Error("a 50ms blink never reached the screen in 25s of sampling")
	}
}
