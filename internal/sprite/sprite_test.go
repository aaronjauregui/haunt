package sprite

import (
	"image"
	"image/color"
	"testing"
)

func TestOpaqueRectsMergesAndSplits(t *testing.T) {
	// 4x4: opaque 2x2 block top-left, one opaque pixel bottom-right,
	// one low-alpha pixel that must be excluded.
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	op := color.NRGBA{A: 255}
	img.SetNRGBA(0, 0, op)
	img.SetNRGBA(1, 0, op)
	img.SetNRGBA(0, 1, op)
	img.SetNRGBA(1, 1, op)
	img.SetNRGBA(3, 3, op)
	img.SetNRGBA(3, 0, color.NRGBA{A: 5}) // below threshold

	s := &Sprite{Img: img}
	got := s.OpaqueRects(16)
	want := []image.Rectangle{
		image.Rect(0, 0, 2, 2), // merged vertically
		image.Rect(3, 3, 4, 4),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rect %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestOpaqueRectsNoVerticalMergeAcrossGap(t *testing.T) {
	// Same-x runs on rows 0 and 2 with an empty row between: two rects.
	img := image.NewNRGBA(image.Rect(0, 0, 2, 3))
	op := color.NRGBA{A: 255}
	img.SetNRGBA(0, 0, op)
	img.SetNRGBA(0, 2, op)
	s := &Sprite{Img: img}
	got := s.OpaqueRects(16)
	if len(got) != 2 {
		t.Fatalf("got %v, want two separate rects", got)
	}
}

func TestWriteBGRAPremultiplies(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, G: 100, B: 0, A: 128})
	s := &Sprite{Img: img}
	buf := make([]byte, 4)
	s.WriteBGRA(buf)
	// BGRA order, each channel scaled by alpha 128/255.
	if buf[0] != 0 || buf[1] != 50 || buf[2] != 128 || buf[3] != 128 {
		t.Errorf("got %v, want [0 50 128 128]", buf)
	}
}

func TestBuiltinHasTransparentCornersAndOpaqueCenter(t *testing.T) {
	s := Builtin(100)
	if a := s.Img.NRGBAAt(1, 1).A; a != 0 {
		t.Errorf("corner alpha = %d, want 0", a)
	}
	if a := s.Img.NRGBAAt(50, 50).A; a != 255 {
		t.Errorf("center alpha = %d, want 255", a)
	}
}
