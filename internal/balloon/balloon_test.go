package balloon

import (
	"testing"
)

func TestRenderSizesToContent(t *testing.T) {
	r, err := NewRenderer(DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}

	small := r.Render("Boo.")
	long := r.Render("This is a much longer line of dialogue that will certainly need to wrap across several lines of the balloon.")

	sw, sh := small.Size()
	lw, lh := long.Size()
	if sw <= 0 || sh <= 0 {
		t.Fatalf("small balloon has degenerate size %dx%d", sw, sh)
	}
	if lw > DefaultTheme().MaxWidth {
		t.Errorf("long balloon width %d exceeds max %d", lw, DefaultTheme().MaxWidth)
	}
	if lh <= sh {
		t.Errorf("wrapped balloon (h=%d) not taller than single line (h=%d)", lh, sh)
	}
	if sw >= lw {
		t.Errorf("short text (w=%d) not narrower than wrapped max-width text (w=%d)", sw, lw)
	}
}

func TestRenderPaintsTextAndPanel(t *testing.T) {
	r, err := NewRenderer(DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	sp := r.Render("Hello")
	w, h := sp.Size()

	// Center row should contain both background and text pixels.
	theme := DefaultTheme()
	var bg, fg int
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := sp.Img.NRGBAAt(x, y)
			if c == theme.Background {
				bg++
			}
			if c == theme.Text {
				fg++
			}
		}
	}
	if bg == 0 {
		t.Error("no background pixels painted")
	}
	if fg == 0 {
		t.Error("no text pixels painted")
	}

	// Corners outside the rounding radius stay transparent.
	if a := sp.Img.NRGBAAt(0, 0).A; a != 0 {
		t.Errorf("corner pixel alpha = %d, want 0", a)
	}
}

func TestExplicitNewlines(t *testing.T) {
	r, err := NewRenderer(DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	one := r.Render("a")
	three := r.Render("a\nb\nc")
	_, h1 := one.Size()
	_, h3 := three.Size()
	// Padding is constant, so the text block (height minus padding) must
	// triple exactly.
	pad := 2 * DefaultTheme().Padding
	if h3-pad != 3*(h1-pad) {
		t.Errorf("text block heights: three lines %d, one line %d, want exact 3x", h3-pad, h1-pad)
	}
}
