package ghost

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"testing"

	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/shell"
	"github.com/aaronjauregui/haunt/internal/textinput"
)

func convOffset(g *Ghost) (offset, lines int) {
	g.convMu.Lock()
	defer g.convMu.Unlock()
	return g.conv.offset, g.conv.lines
}

// The wheel over the ghost moves the conversation's view back and forth
// a few lines at a step, clamped at the top; new words bring it back to
// the bottom.
func TestConversationViewScrolls(t *testing.T) {
	g := pagingGhost(t)
	g.openInput()
	for range 6 {
		g.showPaged(longLine[:300])
	}
	if off, lines := convOffset(g); off != 0 || lines < 30 {
		t.Fatalf("after six replies: offset %d, %d lines", off, lines)
	}
	g.onScroll(-1)
	if off, _ := convOffset(g); off != conversationScroll {
		t.Errorf("one step back: offset %d, want %d", off, conversationScroll)
	}
	g.onScroll(-1)
	g.onScroll(1)
	if off, _ := convOffset(g); off != conversationScroll {
		t.Errorf("back two, forward one: offset %d, want %d", off, conversationScroll)
	}
	g.onScroll(-1000)
	off, lines := convOffset(g)
	if top := lines - g.renderer().ConversationLines(); off != top {
		t.Errorf("all the way back: offset %d, want the top at %d", off, top)
	}
	g.onScroll(1)
	if o, _ := convOffset(g); o != off-conversationScroll {
		t.Errorf("forward from the top: offset %d, want %d", o, off-conversationScroll)
	}
	g.streamInConversation("And another thing")
	if off, _ := convOffset(g); off != 0 {
		t.Errorf("new words left the view %d lines back", off)
	}
	g.onScroll(-1)
	g.showPaged("Said.")
	if off, _ := convOffset(g); off != 0 {
		t.Errorf("a finished line left the view %d lines back", off)
	}
	// Outside a conversation the wheel is recall's, as ever.
	g.endConversation("test")
	g.onScroll(-1)
	g.recallMu.Lock()
	browsing := g.recall.gen != 0
	g.recallMu.Unlock()
	if !browsing {
		t.Error("after the conversation the wheel did not go back through what was said")
	}
}

// The person's line is in the view the moment it is sent, before any
// reply: the view's line count grows on Enter.
func TestConversationViewShowsYourLineAtOnce(t *testing.T) {
	g := pagingGhost(t)
	g.curiosity = newCuriosity(0, &memStore{m: map[string]string{}}, g.log)
	g.openInput()
	g.showPaged("Hello.")
	_, before := convOffset(g)
	typeString(g, "hi there")
	g.onKey(textinput.KeyEnter, true)
	if _, after := convOffset(g); after <= before {
		t.Errorf("the view has %d lines after Enter, %d before", after, before)
	}
}

// A picture shown in a conversation goes in the view as a thumbnail, and
// the conversation goes on.
func TestConversationViewShowsPictures(t *testing.T) {
	g := pagingGhost(t)
	g.picturesDir = t.TempDir()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 256, 256))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.picturePath("cat"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	g.openInput()
	g.showPaged("Here.")
	_, before := convOffset(g)
	if err := g.showPicture(Picture{ID: "cat"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, after := convOffset(g); after <= before+2 {
		t.Errorf("the picture added %d lines to the view", after-before)
	}
	if !g.inConversation() || !g.inputOpen() || g.pictureShowing() {
		t.Error("the picture took the conversation's balloon")
	}
	g.recallMu.Lock()
	last := g.recall.lines[len(g.recall.lines)-1]
	g.recallMu.Unlock()
	if last.picture != "cat" {
		t.Errorf("the picture was not remembered: %+v", last)
	}
}

// A click on the ghost in a conversation is a poke and nothing else: the
// line, the conversation and what is being typed stay. With the driver's
// own question on the line, or outside a conversation, it closes the line
// as it always has.
func TestClickOnGhostKeepsConversation(t *testing.T) {
	g, b := typingGhost(t)
	g.shell = &shell.Shell{}
	g.openInput()
	typeString(g, "half")
	g.onClick(10, 10)
	if !g.inConversation() || !g.inputOpen() {
		t.Fatal("a click on the ghost ended the conversation")
	}
	g.inputMu.Lock()
	text := g.input.editor.Text()
	g.inputMu.Unlock()
	if text != "half" {
		t.Errorf("the line holds %q after the click, want %q", text, "half")
	}
	if events := drain(b); len(events) != 1 || events[0].Type != proto.EventClick {
		t.Errorf("the click published %+v, want one click", events)
	}

	reply := make(chan string, 1)
	g.openInputWithReply(reply)
	g.onClick(10, 10)
	if g.inputOpen() || g.inConversation() {
		t.Error("a click left a line the driver opened for its question")
	}
	if got := <-reply; got != "" {
		t.Errorf("the question was answered %q by a click", got)
	}

	g.openInputWithReply(make(chan string, 1))
	g.onClick(10, 10)
	if g.inputOpen() {
		t.Error("a click outside a conversation left the line open")
	}
}
