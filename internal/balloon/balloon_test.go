package balloon

import (
	"strings"
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

// TestRenderInputFixedSize: the input panel must not change size as you
// type, which is what lets the surface be repainted in place instead of
// destroyed and recreated — the difference between a steady box and a
// flickering one.
func TestRenderInputFixedSize(t *testing.T) {
	r, err := NewRenderer(DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	want := r.RenderInput("", true).Img.Bounds()
	for _, tc := range []struct {
		name  string
		text  string
		caret bool
	}{
		{"empty, caret off", "", false},
		{"a word", "hello", true},
		{"a sentence", "hello ghost, what have you been thinking about", false},
		{"longer than the panel", strings.Repeat("verbose ", 40), true},
		{"multibyte", "☕☕☕ 日本語 ☕☕☕", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.RenderInput(tc.text, tc.caret).Img.Bounds(); got != want {
				t.Errorf("bounds = %v, want %v", got, want)
			}
		})
	}
}

// TestBalloonHeightIsCapped: three paragraphs of generated dialogue must
// not produce a panel taller than the screen. A balloon that outgrows the
// display stops being a balloon and becomes a wall.
func TestBalloonHeightIsCapped(t *testing.T) {
	theme := DefaultTheme()
	theme.MaxHeight = 200
	r, err := NewRenderer(theme)
	if err != nil {
		t.Fatal(err)
	}

	para := strings.Repeat("The ghost considered the matter at length. ", 12)
	long := para + "\n\n" + para + "\n\n" + para

	sp := r.Render(long)
	if h := sp.Img.Bounds().Dy(); h > theme.MaxHeight {
		t.Errorf("balloon is %dpx tall, want no more than %d", h, theme.MaxHeight)
	}

	// Short text is untouched by the cap.
	short := r.Render("Hello.")
	if h := short.Img.Bounds().Dy(); h > 60 {
		t.Errorf("a one-line balloon is %dpx tall", h)
	}

	// Unlimited stays unlimited, so tests and headless uses can opt out.
	theme.MaxHeight = 0
	r2, err := NewRenderer(theme)
	if err != nil {
		t.Fatal(err)
	}
	if h := r2.Render(long).Img.Bounds().Dy(); h <= 200 {
		t.Errorf("an uncapped balloon was capped anyway: %dpx", h)
	}
}

// TestCappedBalloonKeepsTheTail: dialogue streams in, so the newest words
// are the ones being spoken. The ellipsis marks what scrolled off.
func TestCappedBalloonKeepsTheTail(t *testing.T) {
	theme := DefaultTheme()
	theme.MaxHeight = 200
	r, err := NewRenderer(theme)
	if err != nil {
		t.Fatal(err)
	}
	in := []string{"one", "two", "three", "four", "five", "six", "seven", "eight"}

	got := r.fit(in, 40) // 40px lines: (200-28)/40 = 4 fit
	if len(got) != 4 {
		t.Fatalf("fit kept %d lines (%v), want 4", len(got), got)
	}
	if got[0] != "…" {
		t.Errorf("first line = %q, want the ellipsis marking what scrolled off", got[0])
	}
	if got[len(got)-1] != "eight" {
		t.Errorf("last line = %q, want the newest text kept", got[len(got)-1])
	}
	if got[1] != "six" {
		t.Errorf("kept lines = %v, want the tail", got)
	}

	// Text that fits is returned untouched, ellipsis and all.
	if got := r.fit(in[:3], 40); len(got) != 3 || got[0] != "one" {
		t.Errorf("short text was trimmed: %v", got)
	}
	// A cap too small for even one line still renders something.
	if got := r.fit(in, 10_000); len(got) != 1 {
		t.Errorf("an impossibly tight cap kept %v", got)
	}
}

// TestRenderPage: a long line is split into readable screenfuls, each
// marked so the reader knows there is more.
func TestRenderPage(t *testing.T) {
	theme := DefaultTheme()
	theme.MaxHeight = 200
	r, err := NewRenderer(theme)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("The ghost considered the matter at length. ", 30)

	sp, pages := r.RenderPage(long, 0)
	if pages < 3 {
		t.Fatalf("long text made %d pages, want several", pages)
	}
	if h := sp.Img.Bounds().Dy(); h > theme.MaxHeight {
		t.Errorf("page is %dpx tall, want <= %d", h, theme.MaxHeight)
	}

	// Every page is within the cap, and the count is stable.
	for p := 0; p < pages; p++ {
		sp, n := r.RenderPage(long, p)
		if n != pages {
			t.Errorf("page %d reported %d pages, want %d", p, n, pages)
		}
		if h := sp.Img.Bounds().Dy(); h > theme.MaxHeight {
			t.Errorf("page %d is %dpx tall", p, h)
		}
	}

	// Out-of-range clamps rather than failing.
	if _, n := r.RenderPage(long, pages+10); n != pages {
		t.Errorf("clamped page reported %d pages", n)
	}
	if _, n := r.RenderPage(long, -3); n != pages {
		t.Errorf("negative page reported %d pages", n)
	}

	// Short text is a single page with no marker, so an ordinary line does
	// not grow a footer.
	short, n := r.RenderPage("Hello.", 0)
	if n != 1 {
		t.Errorf("short text made %d pages", n)
	}
	if plain := r.Render("Hello."); short.Img.Bounds() != plain.Img.Bounds() {
		t.Errorf("a one-page balloon %v differs from a plain one %v",
			short.Img.Bounds(), plain.Img.Bounds())
	}
}
