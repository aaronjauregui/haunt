package shell

import (
	"image"
	"image/color"
	"testing"
)

// solid is a w by h image of one colour.
func solid(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// TestResampleSolidSquareKeepsColour: a square at a third of its size, at
// an offset that puts its edges between pixels, is the same red everywhere
// it shows. Its transparent surround is blue, so resampling straight alpha
// would ring it with purple.
func TestResampleSolidSquareKeepsColour(t *testing.T) {
	art := solid(32, 32, color.NRGBA{B: 255})
	for y := 1; y < 31; y++ {
		for x := 1; x < 31; x++ {
			art.SetNRGBA(x, y, red)
		}
	}
	out, at := resample(art, [2]int{1, 1}, 1.0/3)
	if at != [2]int{0, 0} {
		t.Errorf("at = %v, want floor(1/3) = 0", at)
	}
	// Covers floor(1/3)..ceil(33/3) = 0..11.
	if b := out.Bounds(); b.Dx() != 11 || b.Dy() != 11 {
		t.Errorf("size = %v, want 11x11", b.Size())
	}
	opaque := 0
	for y := 0; y < 11; y++ {
		for x := 0; x < 11; x++ {
			c := out.NRGBAAt(x, y)
			if c.A == 0 {
				continue
			}
			if c.R != 255 || c.G != 0 || c.B != 0 {
				t.Fatalf("pixel (%d,%d) = %v, want pure red wherever it shows", x, y, c)
			}
			if c.A == 255 {
				opaque++
			}
		}
	}
	// The square spans art pixels 2..32, shell pixels 0.67..10.67: whole
	// pixels 1 to 9 are fully inside it.
	if opaque != 81 {
		t.Errorf("%d fully opaque pixels, want 81", opaque)
	}
}

// TestResampleAbuttingLayersStayAbutting: two layers that meet at art
// resolution meet at shell resolution too, sharing the pixel their seam
// falls in, with neither a gap nor an overlap between their coverage.
func TestResampleAbuttingLayersStayAbutting(t *testing.T) {
	left, lat := resample(solid(10, 6, red), [2]int{0, 0}, 1.0/3)
	right, rat := resample(solid(10, 6, green), [2]int{10, 0}, 1.0/3)
	// The seam is at 10/3 = 3.33: the left layer's last column and the
	// right layer's first are both column 3.
	lastLeft := lat[0] + left.Bounds().Dx() - 1
	if lastLeft != 3 || rat[0] != 3 {
		t.Fatalf("left ends at column %d, right starts at %d; want both 3", lastLeft, rat[0])
	}
	// Together they cover the shared column exactly once.
	la := int(left.NRGBAAt(3-lat[0], 0).A)
	ra := int(right.NRGBAAt(3-rat[0], 0).A)
	if sum := la + ra; sum < 254 || sum > 256 {
		t.Errorf("coverage of the seam = %d + %d = %d, want 255", la, ra, sum)
	}
	if la < 80 || la > 90 {
		t.Errorf("left covers the seam pixel by %d, want about a third", la)
	}
}

// TestResampleThinLine: a 1-pixel line at a quarter scale is one faint
// line, not a gap and not two.
func TestResampleThinLine(t *testing.T) {
	art := image.NewNRGBA(image.Rect(0, 0, 16, 8))
	for y := 0; y < 8; y++ {
		art.SetNRGBA(5, y, blue)
	}
	out, _ := resample(art, [2]int{0, 0}, 0.25)
	var cols []int
	for x := 0; x < out.Bounds().Dx(); x++ {
		c := out.NRGBAAt(x, 0)
		if c.A == 0 {
			continue
		}
		cols = append(cols, x)
		if c.A < 60 || c.A > 68 {
			t.Errorf("line alpha = %d, want a quarter (64)", c.A)
		}
		if c.B != 255 || c.R != 0 || c.G != 0 {
			t.Errorf("line colour = %v, want pure blue", c)
		}
	}
	if len(cols) != 1 || cols[0] != 1 {
		t.Errorf("line shows in columns %v, want just column 1", cols)
	}
}

// TestResampleIdentity: scale 1 does not resample at all, so a shell that
// does not use scale is the art on disk, pixel for pixel.
func TestResampleIdentity(t *testing.T) {
	art := solid(7, 5, red)
	out, at := resample(art, [2]int{3, 4}, 1)
	if out != art || at != [2]int{3, 4} {
		t.Error("scale 1 resampled the art")
	}
}

// TestResampleUp: doubling keeps the colour and puts the edges exactly at
// twice their position.
func TestResampleUp(t *testing.T) {
	out, at := resample(solid(5, 3, green), [2]int{1, 2}, 2)
	if at != [2]int{2, 4} {
		t.Errorf("at = %v, want (2,4)", at)
	}
	if b := out.Bounds(); b.Dx() != 10 || b.Dy() != 6 {
		t.Fatalf("size = %v, want 10x6", b.Size())
	}
	for y := 0; y < 6; y++ {
		for x := 0; x < 10; x++ {
			if c := out.NRGBAAt(x, y); c != green {
				t.Fatalf("pixel (%d,%d) = %v, want solid green", x, y, c)
			}
		}
	}
	// A fractional scale up gives the edge pixel partial coverage.
	out, at = resample(solid(3, 1, green), [2]int{1, 0}, 1.5)
	// Spans 1.5..6: pixels 1 to 5, the first half covered.
	if at[0] != 1 || out.Bounds().Dx() != 5 {
		t.Fatalf("at %v, width %d; want 1 and 5", at, out.Bounds().Dx())
	}
	if a := out.NRGBAAt(0, 0).A; a < 126 || a > 129 {
		t.Errorf("edge alpha = %d, want half", a)
	}
	if c := out.NRGBAAt(0, 0); c.G != 255 || c.R != 0 {
		t.Errorf("edge colour = %v, want green", c)
	}
}

// TestResampleEmpty: art that covers nothing is not resampled; only its
// position moves.
func TestResampleEmpty(t *testing.T) {
	empty := image.NewNRGBA(image.Rectangle{})
	out, at := resample(empty, [2]int{9, 9}, 0.5)
	if out != empty || !out.Bounds().Empty() || at != [2]int{4, 4} {
		t.Errorf("empty art became %v at %v", out.Bounds(), at)
	}
}
