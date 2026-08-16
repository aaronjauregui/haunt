package ghost

import (
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/bus"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/textinput"
)

// typingGhost drives the input state machine without a compositor: no
// surface to hold focus, no balloon to paint, just the state and the events
// it produces.
func typingGhost(t *testing.T) (*Ghost, *bus.Bus) {
	t.Helper()
	b := bus.New(64)
	g := &Ghost{
		opts: Options{},
		log:  hlog.Discard(),
		bus:  b,
		done: make(chan struct{}),
	}
	g.curiosity = newCuriosity(0, &memStore{m: map[string]string{}}, g.log)
	t.Cleanup(func() { close(g.done) })
	return g, b
}

// typeString feeds scancodes for a lowercase ASCII string.
func typeString(g *Ghost, s string) {
	codes := map[rune]uint32{
		'a': 30, 'b': 48, 'c': 46, 'd': 32, 'e': 18, 'f': 33, 'g': 34, 'h': 35,
		'i': 23, 'j': 36, 'k': 37, 'l': 38, 'm': 50, 'n': 49, 'o': 24, 'p': 25,
		'q': 16, 'r': 19, 's': 31, 't': 20, 'u': 22, 'v': 47, 'w': 17, 'x': 45,
		'y': 21, 'z': 44, ' ': textinput.KeySpace,
	}
	for _, r := range s {
		code, ok := codes[r]
		if !ok {
			panic("typeString: no scancode for " + string(r))
		}
		g.onKey(code, true)
		g.onKey(code, false)
	}
}

// drain collects events published so far.
func drain(b *bus.Bus) []proto.Event {
	var out []proto.Event
	for {
		select {
		case e := <-b.Events():
			out = append(out, e)
		case <-time.After(50 * time.Millisecond):
			return out
		}
	}
}

// TestTypedLineBecomesAMessage: what you type reaches the brain as the same
// message event `haunt say` produces, so brains need no new handling.
func TestTypedLineBecomesAMessage(t *testing.T) {
	g, b := typingGhost(t)
	g.openInput()
	if !g.inputOpen() {
		t.Fatal("input did not open")
	}
	typeString(g, "hello ghost")
	g.onKey(textinput.KeyEnter, true)

	if g.inputOpen() {
		t.Error("input stayed open after submitting")
	}
	events := drain(b)
	if len(events) != 1 {
		t.Fatalf("published %d events, want 1: %+v", len(events), events)
	}
	if events[0].Type != proto.EventMessage || events[0].Text != "hello ghost" {
		t.Errorf("event = %+v, want a message of %q", events[0], "hello ghost")
	}
	// Typed input is the user addressing the ghost, so the reply must not
	// come out of the curiosity allowance — which here is zero, meaning a
	// misclassified reply would be dropped entirely.
	if !g.allowInitiative("say") {
		t.Error("replying to typed input was billed as initiative")
	}
}

// TestEscapeAbandonsTheLine: nothing typed is sent, and nothing is left
// open.
func TestEscapeAbandonsTheLine(t *testing.T) {
	g, b := typingGhost(t)
	g.openInput()
	typeString(g, "never mind")
	g.onKey(textinput.KeyEsc, true)

	if g.inputOpen() {
		t.Error("escape left the line open")
	}
	if events := drain(b); len(events) != 0 {
		t.Errorf("escape published %+v", events)
	}

	// And the next line starts empty rather than resuming the abandoned one.
	g.openInput()
	typeString(g, "hi")
	g.onKey(textinput.KeyEnter, true)
	events := drain(b)
	if len(events) != 1 || events[0].Text != "hi" {
		t.Errorf("second line = %+v, want just %q", events, "hi")
	}
}

// TestEmptyLineSendsNothing: pressing enter at an empty prompt closes it
// without waking the brain.
func TestEmptyLineSendsNothing(t *testing.T) {
	g, b := typingGhost(t)
	g.openInput()
	g.onKey(textinput.KeySpace, true)
	g.onKey(textinput.KeySpace, false)
	g.onKey(textinput.KeyEnter, true)

	if events := drain(b); len(events) != 0 {
		t.Errorf("whitespace was sent as a message: %+v", events)
	}
	if g.inputOpen() {
		t.Error("input stayed open")
	}
}

// TestKeysIgnoredWhenClosed: a ghost that is not being typed at must not
// accumulate keystrokes, whatever the compositor sends.
func TestKeysIgnoredWhenClosed(t *testing.T) {
	g, b := typingGhost(t)
	typeString(g, "stray")
	g.onKey(textinput.KeyEnter, true)
	if events := drain(b); len(events) != 0 {
		t.Errorf("keys with no open line produced %+v", events)
	}

	g.openInput()
	g.onKey(textinput.KeyEnter, true)
	if events := drain(b); len(events) != 0 {
		t.Errorf("the stray keys were buffered and sent later: %+v", events)
	}
}

// TestLosingFocusClosesTheLine: the compositor handing the keyboard
// elsewhere means the user's attention moved on.
func TestLosingFocusClosesTheLine(t *testing.T) {
	g, _ := typingGhost(t)
	g.openInput()
	typeString(g, "half a thought")
	g.closeInput() // what the OnKeyboardFocus(false) handler calls
	if g.inputOpen() {
		t.Error("losing focus left the line open")
	}
	// Closing twice is what happens when focus loss and a click race.
	g.closeInput()
}

// A line closed and reopened quickly must survive the compositor's
// paperwork. Closing one releases keyboard interactivity, which makes the
// compositor send a leave; if the user has already right-clicked for the
// next line by the time it arrives, acting on it tears down a line that was
// opened a moment ago. Found by hand in a nested sway — open, click away,
// open again, and the second line vanished the instant it appeared.
func TestStaleKeyboardLeaveDoesNotCloseTheNextLine(t *testing.T) {
	g, _ := typingGhost(t)

	// A line, focused and then abandoned the ordinary way.
	g.openInput()
	g.onKeyboardFocus(true)
	g.onKeyboardFocus(false)
	if g.inputOpen() {
		t.Fatal("losing the keyboard left the line open")
	}

	// The next line, opened before the previous one's leave lands.
	g.openInput()
	if !g.inputOpen() {
		t.Fatal("the second line did not open")
	}
	g.onKeyboardFocus(false) // the straggler
	if !g.inputOpen() {
		t.Error("a leave meant for the previous line closed this one")
	}

	// And once this line really has the keyboard, losing it still closes.
	g.onKeyboardFocus(true)
	g.onKeyboardFocus(false)
	if g.inputOpen() {
		t.Error("the line survived losing focus it actually held")
	}
}
