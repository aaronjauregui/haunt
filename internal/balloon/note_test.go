package balloon

import (
	"image"
	"image/color"
	"testing"

	"github.com/aaronjauregui/haunt/internal/sprite"
)

func noteRenderer(t *testing.T, scale float64) *Renderer {
	t.Helper()
	r, err := NewRendererScaled(DefaultTheme(), scale)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The default paper is sized to its words: a short note is a narrow slip,
// a long one wraps at the note's width and grows downwards.
func TestRenderNoteDefaultSizesToText(t *testing.T) {
	r := noteRenderer(t, 1)
	short := r.RenderNote("hi", nil, nil)
	long := r.RenderNote("water the plants before you leave, the fern is looking thirsty and the basil worse", nil, nil)
	sw, sh := short.Size()
	lw, lh := long.Size()
	if sw >= lw {
		t.Errorf("a short note is %d wide and a long one %d; the short one should be narrower", sw, lw)
	}
	if lw > noteMaxWidth {
		t.Errorf("a long note is %d wide, past the %d a note may be", lw, noteMaxWidth)
	}
	if lh <= sh {
		t.Errorf("a long note is %d tall and a short one %d; the long one should wrap", lh, sh)
	}
	// Light paper, dark writing: the middle of the top edge's inside is
	// the paper, and it is light.
	c := short.Img.NRGBAAt(sw/2, 4)
	if int(c.R)+int(c.G)+int(c.B) < 600 {
		t.Errorf("the default paper is %v, want a light sheet", c)
	}
}

// A picture sits above the words at the note's inner width, so a note
// with one is the full width and taller than the same words alone.
func TestRenderNotePictureAddsHeight(t *testing.T) {
	r := noteRenderer(t, 1)
	pic := image.NewNRGBA(image.Rect(0, 0, 64, 32))
	for i := range pic.Pix {
		pic.Pix[i] = 0x80
	}
	plain := r.RenderNote("the whale", nil, nil)
	with := r.RenderNote("the whale", pic, nil)
	pw, ph := plain.Size()
	ww, wh := with.Size()
	if wh <= ph {
		t.Errorf("with a picture the note is %d tall, without %d", wh, ph)
	}
	if ww != noteMaxWidth || ww <= pw {
		t.Errorf("with a picture the note is %d wide, want the full %d", ww, noteMaxWidth)
	}
	// Twice as wide as tall, at the inner width.
	inner := noteMaxWidth - 2*notePadding
	if got := wh - ph; got < inner/2 {
		t.Errorf("the picture added %d pixels, want at least %d", got, inner/2)
	}
}

// Paper the shell supplies is what the note is drawn on, at its own width,
// and it is drawn at the renderer's resolution.
func TestRenderNoteUsesSuppliedPaper(t *testing.T) {
	paper := image.NewNRGBA(image.Rect(0, 0, 150, 90))
	red := color.NRGBA{R: 200, A: 255}
	for y := 0; y < 90; y++ {
		for x := 0; x < 150; x++ {
			paper.SetNRGBA(x, y, red)
		}
	}
	for _, scale := range []float64{1, 2} {
		r := noteRenderer(t, scale)
		sp := r.RenderNote("hi", nil, &sprite.Sprite{Img: paper})
		w, h := sp.Size()
		if want := int(150 * scale); w != want {
			t.Errorf("at %gx the note on supplied paper is %d wide, want %d", scale, w, want)
		}
		if want := int(90 * scale); h != want {
			t.Errorf("at %gx the note on supplied paper is %d tall, want %d", scale, h, want)
		}
		if c := sp.Img.NRGBAAt(1, h-2); c != red {
			t.Errorf("at %gx the corner is %v, want the paper's %v", scale, c, red)
		}
	}
	// More words than the paper holds stretch it downwards.
	r := noteRenderer(t, 1)
	sp := r.RenderNote("a great many words that will not fit on so small a slip of paper as this one, however it is folded", nil, &sprite.Sprite{Img: paper})
	if _, h := sp.Size(); h <= 90 {
		t.Errorf("a long note on short paper is %d tall, want it stretched past 90", h)
	}
}
