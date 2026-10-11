package ghost

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/textinput"
)

// Enter in a conversation sends the line and opens it again, empty, with
// the conversation still going.
func TestConversationStaysOpen(t *testing.T) {
	g, b := typingGhost(t)
	g.openInput()
	if !g.inConversation() {
		t.Fatal("typing at the ghost did not open a conversation")
	}
	typeString(g, "hello")
	g.onKey(textinput.KeyEnter, true)
	if !g.inputOpen() || !g.inConversation() {
		t.Fatal("the line closed on Enter")
	}
	g.inputMu.Lock()
	empty := g.input.editor.Empty()
	g.inputMu.Unlock()
	if !empty {
		t.Error("the line did not open again empty")
	}
	if events := drain(b); len(events) != 1 || events[0].Type != proto.EventMessage || events[0].Text != "hello" {
		t.Errorf("published %+v, want one message of %q", events, "hello")
	}
	typeString(g, "again")
	g.onKey(textinput.KeyEnter, true)
	if events := drain(b); len(events) != 1 || events[0].Text != "again" {
		t.Errorf("second line published %+v", events)
	}
}

// Escape ends the conversation, sending nothing.
func TestConversationEndsOnEscape(t *testing.T) {
	g, b := typingGhost(t)
	g.openInput()
	typeString(g, "never mind")
	g.onKey(textinput.KeyEsc, true)
	if g.inputOpen() || g.inConversation() {
		t.Error("escape left the conversation open")
	}
	if events := drain(b); len(events) != 0 {
		t.Errorf("escape published %+v", events)
	}
}

// A question of the driver's put during a conversation (ask_text) takes
// the line's next Enter as its answer, which is not a message, and the
// conversation goes on.
func TestConversationTakesAQuestion(t *testing.T) {
	g, b := typingGhost(t)
	g.openInput()
	reply := make(chan string, 1)
	if !g.openInputWithReply(reply) {
		t.Fatal("the question found no line to take")
	}
	typeString(g, "blue")
	g.onKey(textinput.KeyEnter, true)
	select {
	case got := <-reply:
		if got != "blue" {
			t.Errorf("answer %q, want %q", got, "blue")
		}
	default:
		t.Fatal("the question was not answered")
	}
	if events := drain(b); len(events) != 0 {
		t.Errorf("the answer was also sent as %+v", events)
	}
	if !g.inConversation() || !g.inputOpen() {
		t.Error("answering ended the conversation")
	}
	typeString(g, "hi")
	g.onKey(textinput.KeyEnter, true)
	if events := drain(b); len(events) != 1 || events[0].Text != "hi" {
		t.Errorf("the next line published %+v, want a message", events)
	}
}

// A conversation with nothing typed and nothing said for the idle time
// ends on its own; typing keeps it going.
func TestConversationIdleEnds(t *testing.T) {
	g, _ := typingGhost(t)
	g.conv.idleAfter = 80 * time.Millisecond
	g.openInput()
	time.Sleep(50 * time.Millisecond)
	typeString(g, "hm")
	time.Sleep(50 * time.Millisecond)
	if !g.inConversation() {
		t.Fatal("the conversation went quiet while the person was typing")
	}
	if !eventually(func() bool { return !g.inConversation() && !g.inputOpen() }) {
		t.Error("an idle conversation stayed open")
	}
}

// Nothing lingers away while a conversation is open; once it ends, the
// last words stay up and then go like any reply.
func TestConversationNoLinger(t *testing.T) {
	g := pagingGhost(t)
	g.openInput()
	g.showPaged("Hello there.")
	g.lingerBalloon(g.balloonGen(), time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	if !g.blnShowing() {
		t.Fatal("the conversation's balloon lingered away")
	}
	g.endConversation("test")
	if g.inputOpen() || !g.blnShowing() {
		t.Fatal("ending the conversation did not leave its words up")
	}
	g.lingerBalloon(g.balloonGen(), time.Millisecond)
	if !eventually(func() bool { return !g.blnShowing() }) {
		t.Error("the words never went after the conversation ended")
	}
}

// Rule 8 is the one rule the mode changes.
func TestConversationRule(t *testing.T) {
	s := standing{Name: "wisp"}
	off := standingRules(s)
	s.Conversation = true
	on := standingRules(s)
	if !strings.Contains(off, "under 25 words") || strings.Contains(off, "in conversation") {
		t.Errorf("rule 8 out of a conversation:\n%s", off)
	}
	if !strings.Contains(on, "8. You are in conversation: your persona's length applies, and up to a short paragraph") ||
		strings.Contains(on, "under 25 words") {
		t.Errorf("rule 8 in a conversation:\n%s", on)
	}
	lines := func(r string) []string { return strings.Split(r, "\n") }
	a, b := lines(off), lines(on)
	if len(a) != len(b) {
		t.Fatalf("%d lines against %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] && !strings.HasPrefix(a[i], "8. ") {
			t.Errorf("line %d changed with the mode: %q", i, b[i])
		}
	}
}

// The turn context says a conversation is open, last.
func TestConversationContext(t *testing.T) {
	g, _, _ := modelGhost(t, 0)
	if ctx := g.turnContext(time.Now()); strings.Contains(ctx, "In conversation") {
		t.Errorf("no conversation, and the context says one: %q", ctx)
	}
	g.openInput()
	if ctx := g.turnContext(time.Now()); !strings.HasSuffix(ctx, "\nIn conversation: yes.") {
		t.Errorf("the context does not end with the conversation: %q", ctx)
	}
	g.endConversation("test")
	if ctx := g.turnContext(time.Now()); strings.Contains(ctx, "In conversation") {
		t.Errorf("the conversation ended and the context still says it: %q", ctx)
	}
}

// end_conversation is offered unless the person switched it off.
func TestEndConversationOffered(t *testing.T) {
	g, b, srv := modelGhost(t, 0, silent("hello"))
	g.markPrompted()
	offered := func(i int) bool {
		for _, tool := range requests(t, srv)[i].Tools {
			if tool.Function.Name == endConversationName {
				return true
			}
		}
		return false
	}
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "hello"})
	waitTurns(t, b, 1)
	if !offered(0) {
		t.Error("end_conversation was not offered by default")
	}
	no := false
	g.applyConversation(config.ConversationConfig{GhostMayEnd: &no})
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "hello"})
	waitTurns(t, b, 2)
	if offered(1) {
		t.Error("end_conversation was offered with ghost_may_end = false")
	}
}

// A ghost that calls end_conversation says goodbye, and the conversation
// closes once the goodbye has been shown.
func TestEndConversationCloses(t *testing.T) {
	speak, _ := json.Marshal(map[string]string{"pose": ""})
	g, b, _ := modelGhost(t, 0,
		fake.Rule{Match: "bye", AfterTool: speakCue, Reply: "Goodbye, then."},
		fake.Rule{Match: "bye", AfterTool: "say goodbye", Calls: []fake.Call{{Name: speakName, Args: speak}}},
		fake.Rule{Match: "bye", Calls: []fake.Call{{Name: endConversationName}}})
	g.markPrompted()
	g.openInput()
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "bye for now"})
	waitTurns(t, b, 1)
	if g.inConversation() || g.inputOpen() {
		t.Fatal("the conversation is still open after the goodbye")
	}
	if !g.blnShowing() {
		t.Error("the goodbye is not on screen")
	}
	g.recallMu.Lock()
	lines := g.recall.lines
	g.recallMu.Unlock()
	if len(lines) == 0 || lines[len(lines)-1].text != "Goodbye, then." {
		t.Errorf("what was said: %+v, want the goodbye last", lines)
	}
	// Out of a conversation there is nothing to end.
	if _, err := g.askToEnd(); err == nil {
		t.Error("end_conversation worked with no conversation open")
	}
}

func convState(g *Ghost) (said, streaming, status string) {
	g.convMu.Lock()
	defer g.convMu.Unlock()
	return g.conv.said, g.conv.streaming, g.conv.status
}

func lastRecalled(g *Ghost) recalled {
	g.recallMu.Lock()
	defer g.recallMu.Unlock()
	return g.recall.lines[len(g.recall.lines)-1]
}

// Words taken back are not the ghost's last words: an empty stream leaves
// the last line said, and a conversation that ends with words still
// arriving leaves up only what was finished.
func TestConversationKeepsOnlyWhatWasSaid(t *testing.T) {
	g := pagingGhost(t)
	g.openInput()
	g.streamInConversation("I was going to say")
	g.streamInConversation("")
	g.onKey(textinput.KeyEsc, true)
	if g.blnShowing() {
		t.Error("words taken back were put up when the conversation ended")
	}

	g.openInput()
	g.showPaged("Said.")
	g.streamInConversation("And then")
	g.streamInConversation("")
	if said, streaming, _ := convState(g); said != "Said." || streaming != "" {
		t.Errorf("after a retraction: said %q, streaming %q; want the line said", said, streaming)
	}
	g.streamInConversation("unfinished")
	g.endConversation("test")
	if !g.blnShowing() {
		t.Fatal("the last line said did not stay up")
	}
	if last := lastRecalled(g); last.text != "Said." || last.gen != g.balloonGen() {
		t.Errorf("the balloon up is %d, last line %q in %d; want the line said in it", g.balloonGen(), last.text, last.gen)
	}
}

// A line of the driver's own in a conversation is its status line: shown,
// never remembered as the ghost's, gone when the ghost speaks, and not
// what lingers when the conversation ends. Done looking takes only the
// line down, not the conversation's panel.
func TestConversationStatusLine(t *testing.T) {
	g := pagingGhost(t)
	g.openInput()
	g.showBalloon(lookingLine)
	if said, _, status := convState(g); status != lookingLine || said != "" {
		t.Errorf("status %q and said %q, want the status line and nothing said", status, said)
	}
	g.recallMu.Lock()
	n := len(g.recall.lines)
	g.recallMu.Unlock()
	if n != 0 {
		t.Errorf("the status line went into the recall: %d lines", n)
	}
	g.doneLooking(g.balloonGen(), "", 0)
	if _, _, status := convState(g); status != "" {
		t.Errorf("done looking left the status line %q", status)
	}
	if !g.blnShowing() || !g.inConversation() {
		t.Error("done looking took the conversation's panel down")
	}
	g.showBalloon("(a tool — done)")
	g.showPaged("Here.")
	if _, _, status := convState(g); status != "" {
		t.Errorf("the ghost spoke and the status line %q stayed", status)
	}
	g.showBalloon("(a tool — done)")
	g.endConversation("test")
	if last := lastRecalled(g); last.text != "Here." || last.gen != g.balloonGen() {
		t.Errorf("lingering %d, last line %q in %d; want the ghost's words, not the status line", g.balloonGen(), last.text, last.gen)
	}
}

// After a conversation the first wheel step goes back to the line before
// the one up, not to the one up: the line said is followed into the
// balloon it ends in, even when drawing it made a new panel.
func TestConversationLastLineFollowed(t *testing.T) {
	g := pagingGhost(t)
	g.showPaged("Before.")
	g.openInput()
	g.showPaged("Last.")
	g.endConversation("test")
	g.onScroll(-1)
	g.recallMu.Lock()
	text := g.recall.lines[g.recall.at].text
	g.recallMu.Unlock()
	if text != "Before." {
		t.Errorf("the first wheel step showed %q, want the line before the one up", text)
	}
}

// A line held while the line was open and shown as it closes is reported,
// so ending a conversation does not put older words over it.
func TestCloseReportsHeldLine(t *testing.T) {
	g := pagingGhost(t)
	g.openInputWithReply(make(chan string, 1))
	if g.closeInputWith("") {
		t.Error("a close with nothing held reported a line shown")
	}
	g.openInputWithReply(make(chan string, 1))
	g.holdLine("newer", nil)
	if !g.closeInputWith("") {
		t.Error("a close that showed a held line did not say so")
	}
}

// Closing the line waits for a draw already under way, so the panel that
// draw makes does not outlive the line.
func TestCloseAfterDrawLeavesNoPanel(t *testing.T) {
	g := pagingGhost(t)
	for range 200 {
		g.openInputWithReply(make(chan string, 1))
		drawn := make(chan struct{})
		go func() {
			defer close(drawn)
			g.drawInput()
		}()
		g.closeInput()
		<-drawn
		if g.blnShowing() {
			t.Fatal("a draw racing the close left the panel up")
		}
	}
}

// A line sent in a conversation puts the waiting indicator up as the
// view's status line at once, before the model has answered; the first
// word takes it down, and it is never one of the lines said. A turn that
// ends in silence takes it down too.
func TestConversationThinkingStatus(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"pose": ""})
	g, b, _ := modelGhost(t, 0,
		fake.Rule{Match: "hello", AfterTool: speakCue, Reply: "Hello yourself, and welcome back.", GapMS: 20},
		fake.Rule{Match: "hello", Calls: []fake.Call{{Name: speakName, Args: args}}, DelayMS: 300},
		fake.Rule{Match: "hush", Calls: []fake.Call{{Name: silentName}}, DelayMS: 300})
	g.markPrompted()
	g.openInput()
	thinking := func() bool {
		_, _, status := convState(g)
		return strings.HasPrefix(status, "thinking.")
	}

	typeString(g, "hello")
	g.onKey(textinput.KeyEnter, true)
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "hello"})
	if !eventually(thinking) {
		t.Fatal("no thinking status after the line was sent")
	}
	if _, streaming, _ := convState(g); streaming != "" {
		t.Fatalf("words streaming %q before the model answered", streaming)
	}
	if !eventually(func() bool { _, streaming, _ := convState(g); return streaming != "" }) {
		t.Fatal("no words streamed")
	}
	if _, _, status := convState(g); status != "" {
		t.Errorf("the first word left the status %q up", status)
	}
	waitTurns(t, b, 1)
	if said, _, status := convState(g); status != "" || said != "Hello yourself, and welcome back." {
		t.Errorf("after the turn: said %q, status %q", said, status)
	}
	g.recallMu.Lock()
	for _, l := range g.recall.lines {
		if strings.HasPrefix(l.text, "thinking") {
			t.Errorf("the status was remembered: %q", l.text)
		}
	}
	g.recallMu.Unlock()

	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "hush"})
	if !eventually(thinking) {
		t.Fatal("no thinking status for the second line")
	}
	waitTurns(t, b, 2)
	if _, _, status := convState(g); status != "" {
		t.Errorf("a silent turn left the status %q up", status)
	}
}
