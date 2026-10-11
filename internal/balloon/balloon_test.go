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

// TestRenderWithInput: words stack above the line in one full-width
// panel, which grows with the words up to the height cap; with no words
// it is the input panel alone.
func TestRenderWithInput(t *testing.T) {
	theme := DefaultTheme()
	theme.MaxWidth, theme.MaxHeight = 480, 300
	r, err := NewRenderer(theme)
	if err != nil {
		t.Fatal(err)
	}
	alone := r.RenderInput("hi", true).Img.Bounds()
	if got := r.RenderWithInput("", "hi", true).Img.Bounds(); got != alone {
		t.Errorf("no words: %v, want the input panel %v", got, alone)
	}
	short := r.RenderWithInput("Hello there.", "hi", true).Img.Bounds()
	if short.Dx() != 480 || short.Dy() <= alone.Dy() {
		t.Errorf("words above the line: %v, want 480 wide and taller than %v", short, alone)
	}
	long := r.RenderWithInput(strings.Repeat("The ghost went on at length. ", 60), "hi", false).Img.Bounds()
	if long.Dx() != 480 || long.Dy() > 300 || long.Dy() <= short.Dy() {
		t.Errorf("long words: %v, want 480 wide, taller than %v and capped at 300", long, short)
	}
	if got := r.RenderWithInput("Hello there.", strings.Repeat("typing ", 30), false).Img.Bounds(); got != short {
		t.Errorf("a long typed line changed the panel: %v, want %v", got, short)
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

// TestScaledRendering: on a HiDPI screen the panel is drawn at the real
// device resolution and stands for the same logical size. Text is the part
// that suffers most from being drawn small and stretched, so this is where
// scaling has to happen rather than in the compositor.
func TestScaledRendering(t *testing.T) {
	theme := DefaultTheme()
	one, err := NewRendererScaled(theme, 1)
	if err != nil {
		t.Fatal(err)
	}
	two, err := NewRendererScaled(theme, 2)
	if err != nil {
		t.Fatal(err)
	}
	const line = "Hello, ghost."

	a := one.Render(line).Img.Bounds()
	b := two.Render(line).Img.Bounds()

	// Twice the pixels, give or take a rounding of the glyph metrics.
	if ratio := float64(b.Dy()) / float64(a.Dy()); ratio < 1.8 || ratio > 2.2 {
		t.Errorf("2x panel is %dpx tall against %dpx: ratio %.2f", b.Dy(), a.Dy(), ratio)
	}
	if ratio := float64(b.Dx()) / float64(a.Dx()); ratio < 1.8 || ratio > 2.2 {
		t.Errorf("2x panel is %dpx wide against %dpx: ratio %.2f", b.Dx(), a.Dx(), ratio)
	}

	// And it reports back the logical size the compositor should be told.
	lw, lh := two.LogicalSize(b.Dx(), b.Dy())
	if lw < a.Dx()-2 || lw > a.Dx()+2 || lh < a.Dy()-2 || lh > a.Dy()+2 {
		t.Errorf("logical size %dx%d, want about %dx%d", lw, lh, a.Dx(), a.Dy())
	}

	if one.Scale() != 1 || two.Scale() != 2 {
		t.Errorf("scales = %v, %v", one.Scale(), two.Scale())
	}
	// A nonsense scale falls back to 1 rather than dividing by zero.
	zero, err := NewRendererScaled(theme, 0)
	if err != nil {
		t.Fatal(err)
	}
	if zero.Scale() != 1 {
		t.Errorf("scale 0 became %v", zero.Scale())
	}
}

// The dots animate several times a second, and the only way to do that
// without flicker is to rewrite the pixels of the buffer the compositor
// already holds — which requires every frame to be the same size. See
// SpriteSurface.Redraw.
func TestWaitingFramesAreTheSameSize(t *testing.T) {
	r, err := NewRenderer(DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"", "thinking…"} {
		w0, h0 := r.RenderWaiting(text, 0).Size()
		for f := 1; f < WaitFrames; f++ {
			w, h := r.RenderWaiting(text, f).Size()
			if w != w0 || h != h0 {
				t.Errorf("text %q frame %d is %dx%d, frame 0 is %dx%d", text, f, w, h, w0, h0)
			}
		}
	}
}

// Frames have to differ, or the indicator is a still picture of three dots
// and says nothing about whether anything is still happening.
func TestWaitingFramesDiffer(t *testing.T) {
	r, err := NewRenderer(DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	a := r.RenderWaiting("", 0)
	b := r.RenderWaiting("", 1)
	same := true
	for i := range a.Img.Pix {
		if a.Img.Pix[i] != b.Img.Pix[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("consecutive waiting frames are identical")
	}
}

// The indicator stays one line: a wait that wraps to a paragraph is a
// balloon, and the balloon is the thing being waited for.
func TestWaitingStaysOneLine(t *testing.T) {
	th := DefaultTheme()
	r, err := NewRenderer(th)
	if err != nil {
		t.Fatal(err)
	}
	_, h := r.RenderWaiting(strings.Repeat("thinking very hard indeed ", 20), 0).Size()
	_, short := r.RenderWaiting("x", 0).Size()
	if h != short {
		t.Errorf("a long wait line grew the panel to %d (short line is %d)", h, short)
	}
}
