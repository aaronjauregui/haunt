package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestTransparentPixelsPassClicksThrough: the sprite's input region is its
// alpha, so a press on the opaque middle of a disc reaches the ghost and
// drags it, and a press on the transparent corner of its bounding box lands
// on the desktop, reaches nothing, and moves nothing.
func TestTransparentPixelsPassClicksThrough(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	const size = 200
	magenta := color.NRGBA{R: 255, G: 0, B: 255, A: 255}
	gDir := filepath.Join(dir, "disc")
	writeGhost(t, gDir, `[ghost]
name = "disc"
capabilities = ["message"]

[shell]
sprite = "disc.png"

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")
	writeDisc(t, filepath.Join(gDir, "disc.png"), size, magenta)

	const posX, posY = 300, 150
	record := filepath.Join(dir, "session.jsonl")
	cmd := startGhost(t, c, bin, gDir, filepath.Join(dir, "state"), record, filepath.Join(dir, "run"), posX, posY)
	defer stopGhost(t, cmd)
	waitPixel(t, c, dir, posX+size/2, posY+size/2, magenta)

	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	defer ptr.Close()

	// A click on the middle reaches the brain.
	cx, cy := posX+size/2, posY+size/2
	if err := ptr.MoveTo(cx, cy); err != nil {
		t.Fatal(err)
	}
	if err := ptr.Press(); err != nil {
		t.Fatal(err)
	}
	if err := ptr.Release(); err != nil {
		t.Fatal(err)
	}
	waitRecordCount(t, record, `"type":"click"`, 1)

	// A drag from the middle moves it.
	const dx, dy = 150, 80
	if err := ptr.Drag(cx, cy, cx+dx, cy+dy); err != nil {
		t.Fatal(err)
	}
	waitRecordCount(t, record, `"type":"drag_end"`, 1)
	// Park the cursor away from the pixels sampled below: grim composites
	// it into headless screenshots.
	if err := ptr.MoveTo(30, 30); err != nil {
		t.Fatal(err)
	}
	newX, newY := posX+dx, posY+dy
	waitPixel(t, c, dir, newX+size/2, newY+size/2, magenta)
	if got := shot(t, c, filepath.Join(dir, "dragged.png")).At(posX+12, posY+12); closeTo(got, magenta) {
		t.Errorf("the sprite is still at its old position: %+v", got)
	}

	// A drag from the transparent corner of the bounding box reaches
	// nothing: no click, no drag, and the sprite stays where it is.
	if err := ptr.Drag(newX+2, newY+2, newX+100, newY+100); err != nil {
		t.Fatal(err)
	}
	if err := ptr.MoveTo(30, 30); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	waitRecordCount(t, record, `"type":"click"`, 1)
	waitRecordCount(t, record, `"type":"drag_end"`, 1)
	if got := shot(t, c, filepath.Join(dir, "corner.png")).At(newX+size/2, newY+size/2); !closeTo(got, magenta) {
		t.Errorf("the sprite moved after a drag from its transparent corner: %+v", got)
	}
}

// writeDisc writes a magenta disc on a transparent background: distinctive
// pixels in the middle, guaranteed-transparent corners.
func writeDisc(t *testing.T, path string, size int, col color.NRGBA) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	c := float64(size) / 2
	r := c * 0.9
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)+0.5-c, float64(y)+0.5-c
			if dx*dx+dy*dy <= r*r {
				img.SetNRGBA(x, y, col)
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// waitPixel polls screenshots until the pixel at (x, y) is want.
func waitPixel(t *testing.T, c *testenv.Compositor, dir string, x, y int, want color.NRGBA) {
	t.Helper()
	path := filepath.Join(dir, "pixel-poll.png")
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := shot(t, c, path).At(x, y)
		if closeTo(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pixel at (%d,%d) never became %+v; last %+v", x, y, want, got)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// closeTo compares colours loosely: the compositor blends, and grim rounds.
func closeTo(got color.Color, want color.NRGBA) bool {
	r, g, b, _ := got.RGBA()
	near := func(x uint32, y uint8) bool {
		d := int(x>>8) - int(y)
		return d >= -8 && d <= 8
	}
	return near(r, want.R) && near(g, want.G) && near(b, want.B)
}
