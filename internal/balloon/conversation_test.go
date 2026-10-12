package balloon

import (
	"bytes"
	"image"
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/sprite"
)

// conversationRenderer is the conversation panel's renderer: wide, capped.
func conversationRenderer(t *testing.T) *Renderer {
	t.Helper()
	theme := DefaultTheme()
	theme.MaxWidth, theme.MaxHeight, theme.Speaker = 480, 300, "wisp"
	r, err := NewRenderer(theme)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// talk is n short exchanges, oldest first.
func talk(n int) []Entry {
	var out []Entry
	for i := range n {
		out = append(out, Entry{You: true, Text: "question " + strings.Repeat("x", i)},
			Entry{Text: "An answer that goes on for a little while, number " + strings.Repeat("y", i)})
	}
	return out
}

// lined is a conversation render with the view cut to its line count.
func lined(sp *sprite.Sprite, v ConversationView) (*sprite.Sprite, int) { return sp, v.Lines }

// inputBounds is the size of the input box alone for text.
func inputBounds(r *Renderer, text string) image.Rectangle {
	sp, _ := r.RenderInput(InputBox{Text: text, Caret: true})
	return sp.Img.Bounds()
}

func pix(t *testing.T, r *Renderer, entries []Entry, offset int) []byte {
	t.Helper()
	sp, _ := r.RenderConversation(entries, offset, InputBox{Text: "", Caret: false})
	return sp.Img.Pix
}

// TestRenderConversationStacks: two entries and a line make a taller
// panel than the line alone, and count their lines with the blank between.
func TestRenderConversationStacks(t *testing.T) {
	r := conversationRenderer(t)
	alone, n := lined(r.RenderConversation(nil, 0, InputBox{Text: "hi", Caret: true}))
	if n != 0 || alone.Img.Bounds() != inputBounds(r, "hi") {
		t.Errorf("no entries: %d lines, %v", n, alone.Img.Bounds())
	}
	two, n := lined(r.RenderConversation([]Entry{{You: true, Text: "hello"}, {Text: "Hello yourself."}}, 0, InputBox{Text: "hi", Caret: true}))
	if n != 3 {
		t.Errorf("two one-line entries made %d lines, want 3", n)
	}
	if two.Img.Bounds().Dy() <= alone.Img.Bounds().Dy() {
		t.Errorf("two entries: %v, no taller than the line alone %v", two.Img.Bounds(), alone.Img.Bounds())
	}
}

// TestRenderConversationOffset: an offset shows earlier lines, and is
// clamped at the top of the transcript; the panel does not change size.
func TestRenderConversationOffset(t *testing.T) {
	r := conversationRenderer(t)
	entries := talk(8)
	now, n := lined(r.RenderConversation(entries, 0, InputBox{Text: "", Caret: false}))
	if n <= r.ConversationLines()+2 {
		t.Fatalf("%d lines do not overflow the %d that fit", n, r.ConversationLines())
	}
	back, _ := r.RenderConversation(entries, 2, InputBox{Text: "", Caret: false})
	if back.Img.Bounds() != now.Img.Bounds() {
		t.Errorf("scrolling changed the panel: %v against %v", back.Img.Bounds(), now.Img.Bounds())
	}
	if bytes.Equal(pix(t, r, entries, 2), pix(t, r, entries, 0)) {
		t.Error("offset 2 shows the same lines as offset 0")
	}
	top := n - r.ConversationLines()
	if !bytes.Equal(pix(t, r, entries, top), pix(t, r, entries, top+10)) {
		t.Error("an offset past the top was not clamped to it")
	}
	if bytes.Equal(pix(t, r, entries, top), pix(t, r, entries, top-1)) {
		t.Error("the top is not where the clamp says")
	}
}

// TestRenderConversationThumbnail: a picture adds its height, scaled to
// the panel's width and kept within its height.
func TestRenderConversationThumbnail(t *testing.T) {
	r := conversationRenderer(t)
	pic := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
	for i := range pic.Pix {
		pic.Pix[i] = 200
	}
	words := []Entry{{You: true, Text: "draw me a cat"}, {Text: "Here."}}
	without, n := lined(r.RenderConversation(words, 0, InputBox{Text: "", Caret: false}))
	with, m := lined(r.RenderConversation(append(words, Entry{Image: pic}), 0, InputBox{Text: "", Caret: false}))
	if m <= n+1 || with.Img.Bounds().Dy() <= without.Img.Bounds().Dy() {
		t.Errorf("a picture: %d lines against %d, %v against %v", m, n, with.Img.Bounds(), without.Img.Bounds())
	}
	if with.Img.Bounds().Dx() != 480 || with.Img.Bounds().Dy() > 300 {
		t.Errorf("the picture broke the panel's bounds: %v", with.Img.Bounds())
	}
}

// TestRenderConversationFixedWidth: the panel is the full width whatever
// is in it or being typed, and once full it stays the same size as it
// grows, so it can be redrawn in place.
func TestRenderConversationFixedWidth(t *testing.T) {
	r := conversationRenderer(t)
	for _, tc := range []struct {
		entries []Entry
		line    string
	}{
		{nil, ""},
		{[]Entry{{Text: "Boo."}}, "hi"},
		{talk(10), strings.Repeat("typing ", 40)},
	} {
		sp, _ := r.RenderConversation(tc.entries, 0, InputBox{Text: tc.line, Caret: true})
		if w := sp.Img.Bounds().Dx(); w != 480 {
			t.Errorf("%d entries, line %q: %d wide, want 480", len(tc.entries), tc.line, w)
		}
	}
	full, _ := r.RenderConversation(talk(10), 0, InputBox{Text: "", Caret: true})
	fuller, _ := r.RenderConversation(talk(11), 0, InputBox{Text: "more", Caret: false})
	if full.Img.Bounds() != fuller.Img.Bounds() || full.Img.Bounds().Dy() > 300 {
		t.Errorf("a full view changed size as it grew: %v, %v", full.Img.Bounds(), fuller.Img.Bounds())
	}
}

// inkSpan is the leftmost and rightmost columns in line slot row of the
// conversation view that differ from the panel, or -1, -1 for a blank one.
func inkSpan(r *Renderer, img *image.NRGBA, row int) (int, int) {
	t := r.theme
	lh := r.lineHeight()
	bg := img.NRGBAAt(t.Padding-1, t.Padding+row*lh+lh/2)
	lo, hi := -1, -1
	for y := t.Padding + row*lh; y < t.Padding+(row+1)*lh; y++ {
		for x := t.Padding; x < t.MaxWidth-t.Padding; x++ {
			if img.NRGBAAt(x, y) == bg {
				continue
			}
			if lo < 0 || x < lo {
				lo = x
			}
			hi = max(hi, x)
		}
	}
	return lo, hi
}

// TestRenderConversationSides: the person's lines end at the right edge,
// `you` past them, every wrapped line too; the ghost's start at the left
// with its name.
func TestRenderConversationSides(t *testing.T) {
	r := conversationRenderer(t)
	th := r.theme
	_, _, youW := r.layoutConversation(nil)
	long := strings.Repeat("a fairly long question that wraps ", 4)
	sp, n := lined(r.RenderConversation([]Entry{
		{You: true, Text: "hello"},
		{Text: "Hello yourself."},
		{You: true, Text: long},
	}, 0, InputBox{Text: "", Caret: false}))
	img := sp.Img
	if n < 6 {
		t.Fatalf("%d lines: the long question did not wrap", n)
	}
	left, right, mid := th.Padding, th.MaxWidth-th.Padding, th.MaxWidth/2
	near := func(a, b int) bool { return a >= b-4 && a <= b }

	if lo, hi := inkSpan(r, img, 0); !near(hi, right-1) || lo < mid {
		t.Errorf("the person's line spans %d..%d, want it in the right half ending at %d", lo, hi, right-1)
	}
	if lo, hi := inkSpan(r, img, 2); lo < left || lo > left+3 || hi > mid {
		t.Errorf("the ghost's line spans %d..%d, want it in the left half starting at %d", lo, hi, left)
	}
	for row := 5; row < n; row++ {
		if _, hi := inkSpan(r, img, row); !near(hi, right-youW-1) {
			t.Errorf("wrapped line %d ends at %d, want %d", row, hi, right-youW-1)
		}
	}
}

// TestRenderConversationNewlines: a line of the person's with newlines in
// it takes a row per line on the person's side, a blank one included.
func TestRenderConversationNewlines(t *testing.T) {
	r := conversationRenderer(t)
	sp, n := lined(r.RenderConversation([]Entry{{You: true, Text: "first\n\nthird"}}, 0, InputBox{}))
	if n != 3 {
		t.Fatalf("three lines made %d rows", n)
	}
	_, _, youW := r.layoutConversation(nil)
	right := r.theme.MaxWidth - r.theme.Padding - youW
	// The first row carries `you` in the gutter; the third is text alone.
	if _, hi := inkSpan(r, sp.Img, 2); hi < right-4 || hi > right {
		t.Errorf("the third line ends at %d, want against %d", hi, right)
	}
	if lo, _ := inkSpan(r, sp.Img, 1); lo != -1 {
		t.Errorf("the blank line has ink from %d", lo)
	}
}

// TestRenderConversationRoom: a taller input box takes its rows from the
// transcript's, and the panel stays within its cap; an offset that hides
// later lines puts a mark beneath the view.
func TestRenderConversationRoom(t *testing.T) {
	r := conversationRenderer(t)
	_, one := r.RenderConversation(talk(10), 0, InputBox{Text: "hi"})
	sp, three := r.RenderConversation(talk(10), 0, InputBox{Text: "a\nb\nc"})
	if three.Room != one.Room-2 {
		t.Errorf("room with a three-row box %d, with one %d", three.Room, one.Room)
	}
	if h := sp.Img.Bounds().Dy(); h > r.theme.MaxHeight {
		t.Errorf("the panel is %d tall, over the cap of %d", h, r.theme.MaxHeight)
	}
	at, _ := r.RenderConversation(talk(10), 0, InputBox{})
	back, _ := r.RenderConversation(talk(10), 2, InputBox{})
	lh := r.lineHeight()
	gapY := r.theme.Padding + one.Room*lh + lh/4
	mark := func(img *image.NRGBA) bool {
		bg := img.NRGBAAt(r.theme.Padding, gapY)
		for x := r.theme.Padding; x < r.theme.MaxWidth-r.theme.Padding; x++ {
			if img.NRGBAAt(x, gapY) != bg {
				return true
			}
		}
		return false
	}
	if mark(at.Img) {
		t.Error("a mark beneath the view at the bottom")
	}
	if !mark(back.Img) {
		t.Error("no mark beneath the view scrolled back")
	}
}
