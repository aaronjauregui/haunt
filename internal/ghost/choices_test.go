package ghost

import (
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/balloon"
	"github.com/aaronjauregui/haunt/internal/bus"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/quiet"
	"github.com/aaronjauregui/haunt/internal/textinput"
)

// choosingGhost drives the menu state machine without a compositor: the
// rendering is real, the screen is not.
func choosingGhost(t *testing.T) (*Ghost, *bus.Bus) {
	t.Helper()
	g, b := typingGhost(t)
	theme := balloon.DefaultTheme()
	theme.MaxHeight = 200
	r, err := balloon.NewRenderer(theme)
	if err != nil {
		t.Fatal(err)
	}
	g.blnRend = r
	q, err := quiet.Load()
	if err != nil {
		t.Fatal(err)
	}
	g.quiet = q
	return g, b
}

// chosen returns the choice event the ghost published, or fails.
func chosen(t *testing.T, b *bus.Bus) proto.Event {
	t.Helper()
	for _, e := range drain(b) {
		if e.Type == proto.EventChoice {
			return e
		}
	}
	t.Fatal("no choice event was published")
	return proto.Event{}
}

func offer(g *Ghost) {
	g.showChoices(proto.Intent{
		Type:    proto.IntentSay,
		Text:    "What would you like?",
		Tag:     "menu",
		Choices: []string{"a look", "a picture", "nothing"},
	})
}

// The keyboard path: digits pick a row. This is the first interaction in
// the driver that needs no mouse at all.
func TestDigitPicksAChoice(t *testing.T) {
	g, b := choosingGhost(t)
	offer(g)
	if !g.choiceOpen() {
		t.Fatal("the menu did not open")
	}

	g.onKey(3, true) // scancode 3 is the "2" key
	e := chosen(t, b)
	if e.Name != "a picture" {
		t.Errorf("choice = %q, want the second option", e.Name)
	}
	if e.Tag != "menu" {
		t.Errorf("tag = %q, want the one the question carried", e.Tag)
	}
	if g.choiceOpen() {
		t.Error("the menu is still up after being answered")
	}
}

// Arrows move, Enter takes. Digits are quicker but arrows are what people
// try, and a menu that ignored them would feel broken.
func TestArrowsAndEnter(t *testing.T) {
	g, b := choosingGhost(t)
	offer(g)

	if got := g.choiceHighlight(); got != -1 {
		t.Errorf("something was preselected (%d); a menu should not answer itself", got)
	}
	g.onKey(keyDown, true)
	g.onKey(keyDown, true)
	if got := g.choiceHighlight(); got != 1 {
		t.Fatalf("highlight = %d after two downs, want 1", got)
	}
	g.onKey(keyUp, true)
	if got := g.choiceHighlight(); got != 0 {
		t.Fatalf("highlight = %d after up, want 0", got)
	}
	g.onKey(textinput.KeyEnter, true)
	if e := chosen(t, b); e.Name != "a look" {
		t.Errorf("Enter took %q, want the highlighted row", e.Name)
	}
}

// Enter with nothing highlighted must not pick the first row by accident:
// answering a question for someone is worse than making them answer it.
func TestEnterWithNothingHighlightedDoesNothing(t *testing.T) {
	g, _ := choosingGhost(t)
	offer(g)
	g.onKey(textinput.KeyEnter, true)
	if !g.choiceOpen() {
		t.Error("Enter answered a question nobody had chosen")
	}
}

// Escape is a dismissal, and a dismissal is an answer — a brain waiting on
// a question has to hear that nobody answered it.
func TestEscapeDismissesAndSaysSo(t *testing.T) {
	g, b := choosingGhost(t)
	offer(g)
	g.onKey(textinput.KeyEsc, true)

	e := chosen(t, b)
	if e.Name != "" {
		t.Errorf("dismissal reported %q as the pick", e.Name)
	}
	if e.Tag != "menu" {
		t.Errorf("tag = %q; a dismissal has to be attributable too", e.Tag)
	}
	if g.choiceOpen() {
		t.Error("the menu survived Escape")
	}
}

// Out-of-range digits are ignored rather than treated as a dismissal: a
// fat-fingered 7 on a three-option menu should leave the question up.
func TestDigitsOutsideTheMenuAreIgnored(t *testing.T) {
	g, _ := choosingGhost(t)
	offer(g)
	g.onKey(8, true) // the "7" key
	if !g.choiceOpen() {
		t.Error("a digit with no matching option closed the menu")
	}
}

// While a menu is up it owns the keyboard: keystrokes must not leak into
// the typed-input editor behind it.
func TestMenuOwnsTheKeyboard(t *testing.T) {
	g, _ := choosingGhost(t)
	g.openInput()
	offer(g)
	if g.inputOpen() {
		t.Fatal("a question opened over a half-typed line")
	}
	typeString(g, "hello")
	if got := g.input.editor.Text(); got != "" {
		t.Errorf("keys reached the input line while a menu was up: %q", got)
	}
}

// A question left alone comes down by itself. A menu that stayed forever
// would be a modal dialog, which is the opposite of a companion.
func TestUnansweredMenuExpires(t *testing.T) {
	g, b := choosingGhost(t)
	offer(g)

	g.choiceMu.Lock()
	expiry := g.choice.expiry
	g.choiceMu.Unlock()
	if expiry == nil {
		t.Fatal("no expiry was scheduled")
	}
	// Rather than waiting out the real two minutes.
	expiry.Reset(10 * time.Millisecond)

	e := chosen(t, b)
	if e.Name != "" {
		t.Errorf("expiry reported %q as a pick", e.Name)
	}
	if g.choiceOpen() {
		t.Error("the menu is still up after expiring")
	}
}

// A question is one balloon among many, and anything else the ghost says
// replaces it — so it has to be *closed*, not merely painted over. A menu
// whose panel is gone but whose state is open holds the keyboard and waits
// for an answer nobody can give. Found by taking a screenshot two seconds
// after opening a menu and finding an empty rectangle: the brain's `hush`
// timer had fired an ordinary hide_say.
func TestSomethingElseSaidClosesTheQuestion(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(g *Ghost)
	}{
		{"hide_say", func(g *Ghost) { g.execIntent(proto.Intent{Type: proto.IntentHideSay}) }},
		{"another line", func(g *Ghost) {
			g.execIntent(proto.Intent{Type: proto.IntentSay, Text: "never mind"})
		}},
		{"a streamed reply", func(g *Ghost) { g.showBalloon("as I was saying") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, b := choosingGhost(t)
			offer(g)
			// A person is in the conversation, so the ghost is allowed to
			// say the thing that replaces the question (initiative is
			// disabled in this test ghost, and a refused say would replace
			// nothing).
			g.markPrompted()
			tc.act(g)

			if g.choiceOpen() {
				t.Error("the question is still listening with nothing on screen")
			}
			// And the brain is told, because from its side the question
			// went unanswered.
			var dismissed bool
			for _, e := range drain(b) {
				if e.Type == proto.EventChoice && e.Name == "" {
					dismissed = true
				}
			}
			if !dismissed {
				t.Error("no dismissal reached the brain")
			}
		})
	}
}

// A line's lifetime is timed from when it appears, and a line that has
// already been replaced is left alone. Both halves matter: the first is why
// a slow reply is readable at all, the second is why an old line's timer
// cannot erase a new one.
func TestLingerHidesOnlyTheLineItWasFor(t *testing.T) {
	g, _ := choosingGhost(t)
	g.markPrompted()

	// A line, and a hide scheduled for it.
	g.execIntent(proto.Intent{Type: proto.IntentSay, Text: "first", LingerMS: 30})
	first := g.balloonGen()

	// Something newer arrives before the linger expires.
	g.execIntent(proto.Intent{Type: proto.IntentSay, Text: "second"})
	second := g.balloonGen()
	if second == first {
		t.Fatal("the second line did not replace the first")
	}

	time.Sleep(120 * time.Millisecond)
	if got := g.balloonGen(); got != second {
		t.Errorf("balloon generation = %d, want the second line (%d) still up: the "+
			"first line's timer took away a line that was not its own", got, second)
	}

	// And a line whose own linger expires does go away.
	g.execIntent(proto.Intent{Type: proto.IntentSay, Text: "third", LingerMS: 30})
	gen := g.balloonGen()
	deadline := time.Now().Add(2 * time.Second)
	for g.balloonGen() == gen && g.blnShowing() {
		if time.Now().After(deadline) {
			t.Fatal("the line never lingered its way off screen")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
