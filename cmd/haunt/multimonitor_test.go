package main

import (
	"encoding/json"
	"fmt"
	"image"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestSecondScreen: a ghost can be put on a named monitor, and comes back
// to the same one after a restart. This is the case a two-monitor desk
// makes constantly, and the one a single-output test can never see.
func TestSecondScreen(t *testing.T) {
	c := testenv.StartT(t)
	second, err := c.AddOutput()
	if err != nil {
		t.Skipf("cannot add a second output: %v", err)
	}
	t.Logf("second screen is %s", second)

	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	stateDir := filepath.Join(dir, "state")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "whisper")
	writeGhost(t, gDir, `[ghost]
name = "whisper"

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	run := func(args ...string) (*exec.Cmd, *syncBuffer) {
		t.Helper()
		cmd := exec.Command(bin, append([]string{"run", "-state", stateDir}, args...)...)
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
		return cmd, stderr
	}

	// Put it on the second screen, somewhere specific.
	cmd, _ := run("-output", second, "-x", "300", "-y", "220", gDir)
	// The ghost is on that output, not the first: sway knows which surfaces
	// belong to which output.
	if !onOutput(t, c, dir, second) {
		t.Errorf("the ghost is not on %s", second)
	}
	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()

	// A restart with no flags puts it back on the same screen: the place
	// is remembered per output, since layer-shell coordinates mean
	// different spots on different monitors.
	cmd2, stderr2 := run(gDir)
	if !onOutput(t, c, dir, second) {
		t.Errorf("after a restart the ghost is not back on %s:\n%s", second, stderr2.String())
	}
	cmd2.Process.Signal(syscall.SIGTERM)
	cmd2.Wait()
}

// onOutput reports whether the ghost's body is drawn inside a named
// screen's area. sway does not list layer surfaces in get_tree, so the
// honest question is where the pixels landed: grim captures the whole
// layout, and each output occupies a known rectangle of it.
func onOutput(t *testing.T, c *testenv.Compositor, dir, want string) bool {
	t.Helper()
	rect, err := outputRect(c, want)
	if err != nil {
		t.Fatalf("output %s: %v", want, err)
	}
	for i := 0; i < 50; i++ {
		img := shot(t, c, filepath.Join(dir, "screens.png"))
		found := 0
		for y := rect.Min.Y; y < rect.Max.Y && y < img.Bounds().Max.Y; y++ {
			for x := rect.Min.X; x < rect.Max.X && x < img.Bounds().Max.X; x++ {
				// The placeholder ghost's body is near-white.
				if r, g, b, _ := img.At(x, y).RGBA(); r>>8 > 200 && g>>8 > 200 && b>>8 > 200 {
					found++
				}
			}
		}
		if found > 500 {
			return true
		}
		wait100ms()
	}
	return false
}

// outputRect is where a screen sits in the captured layout.
func outputRect(c *testenv.Compositor, name string) (image.Rectangle, error) {
	out, err := c.Swaymsg("-t", "get_outputs")
	if err != nil {
		return image.Rectangle{}, err
	}
	var outputs []struct {
		Name string `json:"name"`
		Rect struct {
			X, Y, Width, Height int
		} `json:"rect"`
	}
	if err := json.Unmarshal(out, &outputs); err != nil {
		return image.Rectangle{}, err
	}
	for _, o := range outputs {
		if o.Name == name {
			return image.Rect(o.Rect.X, o.Rect.Y,
				o.Rect.X+o.Rect.Width, o.Rect.Y+o.Rect.Height), nil
		}
	}
	return image.Rectangle{}, fmt.Errorf("no such output")
}
