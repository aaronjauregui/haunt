package balloon

import (
	"bytes"
	"image"
	"strings"
	"testing"
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

func pix(t *testing.T, r *Renderer, entries []Entry, offset int) []byte {
	t.Helper()
	sp, _ := r.RenderConversation(entries, offset, "", false)
	return sp.Img.Pix
}

// TestRenderConversationStacks: two entries and a line make a taller
// panel than the line alone, and count their lines with the blank between.
func TestRenderConversationStacks(t *testing.T) {
	r := conversationRenderer(t)
	alone, n := r.RenderConversation(nil, 0, "hi", true)
	if n != 0 || alone.Img.Bounds() != r.RenderInput("hi", true).Img.Bounds() {
		t.Errorf("no entries: %d lines, %v", n, alone.Img.Bounds())
	}
	two, n := r.RenderConversation([]Entry{{You: true, Text: "hello"}, {Text: "Hello yourself."}}, 0, "hi", true)
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
	now, n := r.RenderConversation(entries, 0, "", false)
	if n <= r.ConversationLines()+2 {
		t.Fatalf("%d lines do not overflow the %d that fit", n, r.ConversationLines())
	}
	back, _ := r.RenderConversation(entries, 2, "", false)
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
	without, n := r.RenderConversation(words, 0, "", false)
	with, m := r.RenderConversation(append(words, Entry{Image: pic}), 0, "", false)
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
		sp, _ := r.RenderConversation(tc.entries, 0, tc.line, true)
		if w := sp.Img.Bounds().Dx(); w != 480 {
			t.Errorf("%d entries, line %q: %d wide, want 480", len(tc.entries), tc.line, w)
		}
	}
	full, _ := r.RenderConversation(talk(10), 0, "", true)
	fuller, _ := r.RenderConversation(talk(11), 0, "more", false)
	if full.Img.Bounds() != fuller.Img.Bounds() || full.Img.Bounds().Dy() > 300 {
		t.Errorf("a full view changed size as it grew: %v, %v", full.Img.Bounds(), fuller.Img.Bounds())
	}
}
