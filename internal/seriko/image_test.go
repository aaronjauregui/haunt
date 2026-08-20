package seriko

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func encode(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Without this, an imported ghost is a rectangle. SERIKO art predates
// widespread alpha and carries transparency as a corner colour.
func TestColorKeyFromTheTopLeft(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range img.Pix {
		img.Pix[i] = 0
	}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, green)
		}
	}
	img.Set(2, 2, skin)

	got, err := LoadSurface(encode(t, img), nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := got.Pix[got.PixOffset(0, 0)+3]; a != 0 {
		t.Errorf("the key colour is still opaque (alpha %d)", a)
	}
	if a := got.Pix[got.PixOffset(2, 2)+3]; a != 255 {
		t.Errorf("the drawn pixel was keyed out (alpha %d)", a)
	}
}

// A .pna is authoritative where a shell ships one; the PNG beside it is
// usually opaque and its corner colour means nothing.
func TestPNAAlphaWins(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			src.Set(x, y, skin) // fully opaque, no key colour anywhere
		}
	}
	mask := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			c := color.NRGBA{A: 255} // black = transparent
			if x == 1 && y == 1 {
				c = color.NRGBA{R: 255, G: 255, B: 255, A: 255} // white = opaque
			}
			mask.Set(x, y, c)
		}
	}
	got, err := LoadSurface(encode(t, src), encode(t, mask))
	if err != nil {
		t.Fatal(err)
	}
	if a := got.Pix[got.PixOffset(1, 1)+3]; a != 255 {
		t.Errorf("white in the mask should be opaque, got alpha %d", a)
	}
	if a := got.Pix[got.PixOffset(0, 0)+3]; a != 0 {
		t.Errorf("black in the mask should be transparent, got alpha %d", a)
	}
}

func TestPNASizeMustMatch(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	mask := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if _, err := LoadSurface(encode(t, src), encode(t, mask)); err == nil {
		t.Error("a mismatched .pna should be an error, not silently ignored")
	}
}

// Art drawn with real alpha is left alone: modern shells exist, and
// second-guessing them punches holes in work that was done correctly.
func TestExistingAlphaIsLeftAlone(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 10, G: 20, B: 30, A: 128})
		}
	}
	got, err := LoadSurface(encode(t, img), nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 3; i < len(got.Pix); i += 4 {
		if got.Pix[i] != 128 {
			t.Fatalf("alpha changed to %d; existing transparency was second-guessed", got.Pix[i])
		}
	}
}

func TestLoadSurfaceRejectsGarbage(t *testing.T) {
	if _, err := LoadSurface([]byte("not a png"), nil); err == nil {
		t.Error("garbage decoded as a surface")
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"Head":       "head",
		"left arm":   "left_arm",
		"まゆら":        "",
		"Bust/Chest": "bust_chest",
		"  spaced  ": "spaced",
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}
