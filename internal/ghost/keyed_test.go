package ghost

import (
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/memory"
)

// keyedMemory gives a model ghost a few topics, enough lines that a single
// word scores above keyedMinScore.
func keyedMemory(t *testing.T, b *modelBrain) {
	t.Helper()
	if b.mem == nil {
		t.Fatal("the ghost has no memory")
	}
	p := memory.Provenance{Source: "person", At: time.Now()}
	for _, l := range [][2]string{
		{"food", "likes apples"},
		{"food", "hates the taste of coriander"},
		{"pets", "has a cat called Miso"},
		{"work", "is writing a thesis on the history of sound"},
		{"places", "grew up by the sea"},
		{"family", "their sister lives in Lisbon"},
	} {
		if err := b.mem.Remember(l[0], l[1], p); err != nil {
			t.Fatal(err)
		}
	}
}

// contextOf is the turn context a prompt for input carries.
func contextOf(t *testing.T, b *modelBrain, input string) string {
	t.Helper()
	msgs, err := b.prompt(&turn{input: input, person: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return msgs[len(msgs)-1].Content
}

const applesLine = "You remember, relevant now: likes apples (food)."

func TestKeyedFiresOnMatch(t *testing.T) {
	_, b, _ := modelGhost(t, 0)
	keyedMemory(t, b)
	if ctx := contextOf(t, b, "how was your day"); strings.Contains(ctx, "You remember") {
		t.Errorf("nothing matched and still: %q", ctx)
	}
	if ctx := contextOf(t, b, "I bought some apples on the way home!"); !strings.Contains(ctx, applesLine) {
		t.Errorf("the apples line is not in the context: %q", ctx)
	}
}

// apples is a hit on the apples line, scoring well.
var apples = []memory.Hit{{Topic: "food", Line: "likes apples", Score: 2}}

// A fired line is still there two turns later with no match.
func TestKeyedSticks(t *testing.T) {
	var s keyedState
	s.step(apples)
	for i := 0; i < keyedSticky-1; i++ {
		s.step(nil)
		if got := s.line(); got != applesLine {
			t.Fatalf("%d turns after it fired: %q", i+1, got)
		}
	}
	// A weak hit does not fire at all.
	var weak keyedState
	weak.step([]memory.Hit{{Topic: "food", Line: "likes apples", Score: keyedMinScore / 2}})
	if got := weak.line(); got != "" {
		t.Errorf("a weak hit fired: %q", got)
	}
}

// Gone after the sticky turns, not refired by a match while it cools, and
// refired by one once it has.
func TestKeyedCoolsDown(t *testing.T) {
	var s keyedState
	s.step(apples) // turn 1
	for range keyedSticky - 1 {
		s.step(nil)
	}
	s.step(nil) // the first turn after it was held
	if got := s.line(); got != "" {
		t.Fatalf("still held after %d turns: %q", keyedSticky, got)
	}
	// Every turn of the cooldown but the last, the person says apples.
	for i := 1; i < keyedCooldown; i++ {
		s.step(apples)
		if got := s.line(); got != "" {
			t.Fatalf("refired %d turns into the cooldown: %q", i, got)
		}
	}
	s.step(apples)
	if got := s.line(); got != applesLine {
		t.Errorf("not refired after the cooldown: %q", got)
	}
}

// An event mentioning what memory holds never keys, and still counts as a
// turn of the table.
func TestKeyedIgnoresEvents(t *testing.T) {
	g, b, _ := modelGhost(t, 0)
	keyedMemory(t, b)
	env := Envelope("observed", F("from", "Moth"), F("action", "spoke"), F("text", "I love apples and the sea"))
	if ctx := contextOf(t, b, env); strings.Contains(ctx, "You remember") {
		t.Errorf("an envelope keyed: %q", ctx)
	}
	if turn := g.keyedTable().turn; turn != 1 {
		t.Errorf("the table is on turn %d, want 1", turn)
	}
}

// The person's words lose the ones that name no subject before the
// lookup.
func TestKeyedQuery(t *testing.T) {
	if got := keyedQuery("I don't think you'd like the APPLES, Miso!"); got != "apples miso" {
		t.Errorf("keyedQuery = %q", got)
	}
}
