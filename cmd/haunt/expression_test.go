package main

import (
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestExpressionChangesTheFace: a set_expression intent must reach the
// screen, not just the shell's internal state. Whisper answers a message
// with an expression, and the pixels where the built-in shell draws its
// mouth have to change.
func TestExpressionChangesTheFace(t *testing.T) {
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

	const spriteX, spriteY = 200, 200
	record := filepath.Join(dir, "session.jsonl")
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

	// The mouth region of the built-in shell: below centre, where the smile
	// and frown arcs are drawn. Counting ink is not enough to tell them
	// apart — both arcs are the same number of pixels — so compare where
	// the ink sits: a smile bows below its baseline, a frown above.
	mouth := image.Rect(spriteX+55, spriteY+85, spriteX+105, spriteY+115)
	if up, down := inkProfile(t, c, dir, mouth); up+down != 0 {
		t.Errorf("the neutral face has a mouth: %d above, %d below", up, down)
	}

	say := func(text string) {
		t.Helper()
		sc := exec.Command(bin, "say", "-ghost", "whisper", text)
		sc.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
		if out, err := sc.CombinedOutput(); err != nil {
			t.Fatalf("say %q: %v: %s", text, err, out)
		}
	}

	// "hello" maps to happy, which draws a smile: ink below the baseline.
	say("hello")
	waitRecordContains(t, record, `"type":"message"`)
	up, down := waitInk(t, c, dir, mouth)
	if down <= up {
		t.Errorf("happy drew %d ink above and %d below; want a smile", up, down)
	}

	// A sad line bows the other way.
	say("that is sad")
	waitRecordCount(t, record, `"type":"message"`, 2)
	for i := 0; i < 100; i++ {
		up, down = inkProfile(t, c, dir, mouth)
		if up > down && up > 0 {
			break
		}
		wait100ms()
	}
	if up <= down {
		t.Errorf("sad drew %d ink above and %d below; want a frown", up, down)
	}
}

// inkProfile counts dark pixels above and below the middle of a region.
// The built-in shell draws its face in near-black on a pale body, so this
// is a cheap way to ask which way the mouth is bent.
func inkProfile(t *testing.T, c *testenv.Compositor, dir string, r image.Rectangle) (up, down int) {
	t.Helper()
	img := shot(t, c, filepath.Join(dir, "face-poll.png"))
	mid := (r.Min.Y + r.Max.Y) / 2
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			if cr>>8 < 80 && cg>>8 < 80 && cb>>8 < 90 {
				if y < mid {
					up++
				} else {
					down++
				}
			}
		}
	}
	return up, down
}

// waitInk polls until a mouth appears at all.
func waitInk(t *testing.T, c *testenv.Compositor, dir string, r image.Rectangle) (up, down int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if up, down = inkProfile(t, c, dir, r); up+down > 0 {
			return up, down
		}
		wait100ms()
	}
	t.Fatal("no expression ever reached the screen")
	return 0, 0
}
