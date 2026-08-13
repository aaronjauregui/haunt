package main

import (
	"bufio"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/sprite"
	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestSpriteRendersInHeadlessCompositor renders the PoC sprite in a headless
// sway and verifies via screenshot that opaque pixels appear at the expected
// position while transparent corners leave the background untouched.
func TestSpriteRendersInHeadlessCompositor(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()

	// A magenta disc on a transparent background: distinctive center pixels,
	// guaranteed-transparent corners.
	const size = 200
	sp := makeDisc(size, color.NRGBA{R: 255, G: 0, B: 255, A: 255})
	spritePath := filepath.Join(dir, "disc.png")
	writePNG(t, spritePath, sp.Img)

	bin := buildPoc(t, dir)

	before := screenshot(t, c, filepath.Join(dir, "before.png"))

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
	waitReady(t, stdout)

	after := screenshot(t, c, filepath.Join(dir, "after.png"))

	// Center of the disc: must be magenta.
	center := after.NRGBAAt(posX+size/2, posY+size/2)
	if !closeTo(center, color.NRGBA{R: 255, G: 0, B: 255, A: 255}, 8) {
		t.Errorf("disc center = %+v, want magenta", center)
	}
	// Corner of the sprite's bounding box: transparent, so the background
	// must be exactly what it was before the sprite appeared.
	cornerBefore := before.NRGBAAt(posX+2, posY+2)
	cornerAfter := after.NRGBAAt(posX+2, posY+2)
	if cornerAfter != cornerBefore {
		t.Errorf("transparent corner changed background: before %+v after %+v", cornerBefore, cornerAfter)
	}
	// Well outside the sprite: also unchanged.
	outBefore := before.NRGBAAt(50, 50)
	outAfter := after.NRGBAAt(50, 50)
	if outBefore != outAfter {
		t.Errorf("pixel outside sprite changed: before %+v after %+v", outBefore, outAfter)
	}

	// Clean shutdown on SIGTERM.
	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("haunt-poc exited uncleanly: %v", err)
	}
}

func makeDisc(size int, col color.NRGBA) *sprite.Sprite {
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
	return &sprite.Sprite{Img: img}
}

func writePNG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func buildPoc(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "haunt-poc")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building haunt-poc: %v: %s", err, out)
	}
	return bin
}

func waitReady(t *testing.T, r interface{ Read([]byte) (int, error) }) {
	t.Helper()
	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if sc.Text() == "READY" {
				lines <- "READY"
				return
			}
		}
		close(lines)
	}()
	select {
	case l, ok := <-lines:
		if !ok || l != "READY" {
			t.Fatal("haunt-poc exited before READY")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for READY")
	}
}

func screenshot(t *testing.T, c *testenv.Compositor, path string) *image.NRGBA {
	t.Helper()
	if err := c.Screenshot(path); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	nrgba, ok := img.(*image.NRGBA)
	if !ok {
		b := img.Bounds()
		nrgba = image.NewNRGBA(b)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				nrgba.Set(x, y, img.At(x, y))
			}
		}
	}
	return nrgba
}

func closeTo(a, b color.NRGBA, tol int) bool {
	d := func(x, y uint8) int {
		v := int(x) - int(y)
		if v < 0 {
			v = -v
		}
		return v
	}
	return d(a.R, b.R) <= tol && d(a.G, b.G) <= tol && d(a.B, b.B) <= tol
}
