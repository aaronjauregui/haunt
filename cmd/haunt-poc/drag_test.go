package main

import (
	"bufio"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestDragAndHitTest verifies with synthetic pointer input that dragging the
// sprite's opaque center moves it, and that pressing on a transparent corner
// does not (the alpha input region makes it a pass-through).
func TestDragAndHitTest(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()

	const size = 200
	magenta := color.NRGBA{R: 255, G: 0, B: 255, A: 255}
	sp := makeDisc(size, magenta)
	spritePath := filepath.Join(dir, "disc.png")
	writePNG(t, spritePath, sp.Img)
	bin := buildPoc(t, dir)

	const posX, posY = 300, 150
	cmd := exec.Command(bin, "-sprite", spritePath, "-x", "300", "-y", "150")
	cmd.Env = c.Env()
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

	events := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			events <- sc.Text()
		}
		close(events)
	}()
	waitLine(t, events, "READY")
	// Wait until the compositor has actually mapped and drawn the sprite;
	// protocol roundtrips don't cover sway's async scene arrangement.
	waitVisible(t, c, dir, posX+size/2, posY+size/2, magenta)

	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	defer ptr.Close()

	// Drag from the disc center by (+150, +80), waiting for the client to
	// acknowledge each injected event before the next.
	const dx, dy = 150, 80
	cx, cy := posX+size/2, posY+size/2
	mustDo(t, ptr.MoveTo(cx, cy))
	waitLine(t, events, "EVT enter")
	mustDo(t, ptr.Press())
	waitLine(t, events, "EVT button state=1")
	mustDo(t, ptr.MoveTo(cx+dx, cy+dy))
	waitLine(t, events, "EVT motion")
	mustDo(t, ptr.Release())
	waitLine(t, events, "EVT button state=0")
	// Park the cursor away from sampled pixels: grim composites the cursor
	// into headless screenshots.
	mustDo(t, ptr.MoveTo(30, 30))

	newX, newY := posX+dx, posY+dy
	waitVisible(t, c, dir, newX+size/2, newY+size/2, magenta)
	shot := screenshot(t, c, filepath.Join(dir, "dragged.png"))
	if got := shot.NRGBAAt(posX+12, posY+12); closeTo(got, magenta, 8) {
		t.Errorf("sprite still present at old position: %+v", got)
	}

	// Press on the sprite's transparent corner (inside the bounding box,
	// outside the input region) and drag: the press lands on the desktop,
	// no button event reaches the client, and the sprite must not move.
	mustDo(t, ptr.Drag(newX+2, newY+2, newX+100, newY+100))
	mustDo(t, ptr.MoveTo(30, 30))
	drainDelay := time.After(300 * time.Millisecond)
loop:
	for {
		select {
		case l, ok := <-events:
			if !ok {
				t.Fatal("haunt-poc exited during corner drag")
			}
			// An unpaired release may be delivered if the held pointer
			// crosses the sprite; only a press indicates a hit-test hole.
			if strings.HasPrefix(l, "EVT button state=1") {
				t.Fatalf("transparent corner press reached the client: %q", l)
			}
		case <-drainDelay:
			break loop
		}
	}
	after := screenshot(t, c, filepath.Join(dir, "after-corner.png"))
	if got := after.NRGBAAt(newX+size/2, newY+size/2); !closeTo(got, magenta, 8) {
		t.Errorf("sprite moved after corner drag: center pixel %+v", got)
	}
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// waitLine reads events until one starts with prefix, or times out.
func waitLine(t *testing.T, events <-chan string, prefix string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-events:
			if !ok {
				t.Fatalf("haunt-poc exited while waiting for %q", prefix)
			}
			if strings.HasPrefix(l, prefix) {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for line %q", prefix)
		}
	}
}

// waitVisible polls screenshots until the pixel at (x, y) matches want.
func waitVisible(t *testing.T, c *testenv.Compositor, dir string, x, y int, want color.NRGBA) {
	t.Helper()
	path := filepath.Join(dir, "visible-poll.png")
	deadline := time.Now().Add(5 * time.Second)
	for {
		img := screenshot(t, c, path)
		if closeTo(img.NRGBAAt(x, y), want, 8) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sprite never became visible at (%d,%d): last pixel %+v", x, y, img.NRGBAAt(x, y))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
