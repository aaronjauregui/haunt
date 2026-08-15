package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// writeLayer lays down a solid rectangle of art for a shell layer.
func writeLayer(t *testing.T, dir, name string, w, h int, c color.NRGBA) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// TestClipAnimates: a looping clip must actually change what is on screen,
// on its own, without anyone poking the ghost.
func TestClipAnimates(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	// A shell of two flat layers: a green body, and a patch that alternates
	// between blue and red on a fast loop.
	gDir := filepath.Join(dir, "flicker")
	if err := os.MkdirAll(gDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLayer(t, gDir, "body.png", 120, 120, color.NRGBA{G: 200, A: 255})
	writeLayer(t, gDir, "blue.png", 40, 40, color.NRGBA{B: 220, A: 255})
	writeLayer(t, gDir, "red.png", 40, 40, color.NRGBA{R: 220, A: 255})
	writeGhost(t, gDir, `[ghost]
name = "flicker"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"

[[shell.layer]]
name = "body"
image = "body.png"
z = 0

[[shell.layer]]
name = "patch"
z = 10
at = [40, 40]
default = "blue"
variants = { blue = "blue.png", red = "red.png" }

[[shell.clip]]
name = "flicker"
trigger = "always"
keys = [
  { at = 0, set = { patch = "red" } },
  { at = 250, set = { patch = "blue" } },
  { at = 500, set = { patch = "red" } },
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

	// Sample the patch over a couple of loops: both colours must appear, or
	// the clip is not running.
	seen := map[string]int{}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && len(seen) < 2 {
		seen[patchColour(t, c, dir, spriteX+60, spriteY+60)]++
		time.Sleep(60 * time.Millisecond)
	}
	if seen["red"] == 0 || seen["blue"] == 0 {
		t.Errorf("the clip did not animate; samples = %v", seen)
	}
}

// TestReducedMotionStopsIdleAnimation: the user's setting wins over the
// shell's enthusiasm.
func TestReducedMotionStopsIdleAnimation(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"),
		[]byte("[animation]\nreduced_motion = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
	gDir := filepath.Join(dir, "flicker")
	if err := os.MkdirAll(gDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLayer(t, gDir, "body.png", 120, 120, color.NRGBA{G: 200, A: 255})
	writeLayer(t, gDir, "blue.png", 40, 40, color.NRGBA{B: 220, A: 255})
	writeLayer(t, gDir, "red.png", 40, 40, color.NRGBA{R: 220, A: 255})
	writeGhost(t, gDir, `[ghost]
name = "flicker"

[brain]
wasm = "brain.wasm"

[[shell.layer]]
name = "body"
image = "body.png"
z = 0

[[shell.layer]]
name = "patch"
z = 10
at = [40, 40]
default = "blue"
variants = { blue = "blue.png", red = "red.png" }

[[shell.clip]]
name = "flicker"
trigger = "always"
keys = [
  { at = 0, set = { patch = "red" } },
  { at = 200, set = { patch = "blue" } },
]
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	const spriteX, spriteY = 200, 200
	cmd := exec.Command(bin, "run", "-x", "200", "-y", "200",
		"-state", filepath.Join(dir, "state"), gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_CONFIG_DIR="+cfgDir)
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

	seen := map[string]int{}
	for i := 0; i < 20; i++ {
		seen[patchColour(t, c, dir, spriteX+60, spriteY+60)]++
		time.Sleep(60 * time.Millisecond)
	}
	if len(seen) != 1 || seen["blue"] == 0 {
		t.Errorf("reduced motion did not stop the clip; samples = %v", seen)
	}
}

// patchColour names what is showing at a point.
func patchColour(t *testing.T, c *testenv.Compositor, dir string, x, y int) string {
	t.Helper()
	img := shot(t, c, filepath.Join(dir, "anim-poll.png"))
	r, g, b, _ := img.At(x, y).RGBA()
	switch {
	case r>>8 > 150:
		return "red"
	case b>>8 > 150:
		return "blue"
	case g>>8 > 150:
		return "body"
	}
	return fmt.Sprintf("other(%d,%d,%d)", r>>8, g>>8, b>>8)
}

// TestTransformAnimates: a `move` keyframe glides the layer across the
// canvas rather than jumping it, which is what interpolation buys over a
// frame-swap format. Sampling the midpoint of the travel is how we tell the
// difference: a stepped animation is never caught halfway.
func TestTransformAnimates(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "glider")
	if err := os.MkdirAll(gDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeLayer(t, gDir, "body.png", 200, 60, color.NRGBA{G: 200, A: 255})
	writeLayer(t, gDir, "dot.png", 20, 20, color.NRGBA{B: 220, A: 255})
	writeGhost(t, gDir, `[ghost]
name = "glider"

[brain]
wasm = "brain.wasm"

[[shell.layer]]
name = "body"
image = "body.png"
z = 0

[[shell.layer]]
name = "dot"
image = "dot.png"
z = 10
at = [0, 20]

[[shell.clip]]
name = "glide"
trigger = "always"
keys = [
  { at = 0, move = { dot = { offset = [0, 0] } } },
  { at = 1200, move = { dot = { offset = [160, 0] } } },
  { at = 2400, move = { dot = { offset = [0, 0] } } },
]
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	const spriteX, spriteY = 200, 200
	cmd := exec.Command(bin, "run", "-x", "200", "-y", "200",
		"-state", filepath.Join(dir, "state"), gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+filepath.Join(dir, "run"))
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

	// Where is the blue dot? Sample repeatedly and collect the distinct
	// positions it is seen at.
	seen := map[int]bool{}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && len(seen) < 4 {
		if x := dotX(t, c, dir, spriteX, spriteY+30); x >= 0 {
			seen[x/20] = true // bucket, so tiny sampling differences agree
		}
		time.Sleep(80 * time.Millisecond)
	}
	if len(seen) < 4 {
		t.Errorf("the dot was seen in %d places, want it gliding through several", len(seen))
	}
}

// dotX finds the blue layer's leftmost pixel along a scanline, or -1.
func dotX(t *testing.T, c *testenv.Compositor, dir string, x0, y int) int {
	t.Helper()
	img := shot(t, c, filepath.Join(dir, "glide-poll.png"))
	for x := x0; x < x0+200; x++ {
		r, g, b, _ := img.At(x, y).RGBA()
		if b>>8 > 150 && r>>8 < 120 && g>>8 < 150 {
			return x - x0
		}
	}
	return -1
}
