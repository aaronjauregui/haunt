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
	if !strings.Contains(on, "8. You are in conversation: a few sentences are fine, still no markdown") ||
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
