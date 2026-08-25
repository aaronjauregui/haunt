package ghost

import (
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/bus"
	"github.com/aaronjauregui/haunt/internal/desktop"
	"github.com/aaronjauregui/haunt/internal/hlog"
)

// obligedGhost is a ghost with both allowances and nothing else. The
// numbers are deliberately tiny so a test can exhaust one and see what the
// other still has.
func obligedGhost(t *testing.T, curiosity, obligations int) *Ghost {
	t.Helper()
	s := &memStore{m: map[string]string{}}
	g := &Ghost{
		store:    s,
		bus:      bus.New(64),
		log:      hlog.Discard(),
		manifest: &Manifest{Ghost: GhostSection{Name: "wisp"}},
		timers:   map[string]*time.Timer{},
		done:     make(chan struct{}),
	}
	g.curiosity = newCuriosity(curiosity, s, g.log)
	g.obligation = newObligation(obligations, s, g.log)
	t.Cleanup(func() { close(g.done) })
	return g
}

// The bug this fixes. A reminder the person asked for used to spend from
// the same twelve-a-day allowance as the ghost's small talk, so a chatty
// morning meant "remind me in ten minutes to take the pizza out" was
// dropped outright when its moment came.
func TestAReminderDoesNotCompeteWithSmallTalk(t *testing.T) {
	g := obligedGhost(t, 1, 5)

	// The ghost spends its whole day's curiosity on a remark.
	if !g.allowInitiative("chat") {
		t.Fatal("the first unprompted remark was refused")
	}
	g.endResponding()
	if g.allowInitiative("chat") {
		t.Fatal("curiosity outlasted its limit of one")
	}

	// The reminder still lands, because it is not curiosity.
	g.markObliged()
	if !g.allowInitiative("chat") {
		t.Error("a reminder was dropped because the ghost had been chatty")
	}
}

// And the converse: obligations are their own pool, so a day full of
// reminders does not silence the ghost's own character.
func TestObligationsDoNotSpendCuriosity(t *testing.T) {
	g := obligedGhost(t, 1, 5)
	for i := 0; i < 3; i++ {
		g.markObliged()
		if !g.allowInitiative("chat") {
			t.Fatalf("reminder %d was refused", i+1)
		}
		g.endResponding()
	}
	// Curiosity is untouched: the ghost has still not said anything of its
	// own accord.
	if !g.allowInitiative("chat") {
		t.Error("the reminders spent the ghost's own allowance")
	}
}

// Going idle closes the obligation window with the others. Otherwise an
// intention that came due just before the person walked away would leave it
// open across the idle event, and the ghost's remark about nothing in
// particular would be charged to their reminders.
func TestIdleClosesTheObligationWindow(t *testing.T) {
	g := obligedGhost(t, 1, 5)
	g.markObliged()
	if !g.obliged() {
		t.Fatal("the obligation window did not open")
	}
	g.endResponding()
	if g.obliged() {
		t.Error("going idle left the obligation window open")
	}
}

// The obligation pool is still bounded, so a brain that has gone wrong
// cannot talk forever by calling everything a duty.
func TestObligationsAreStillRationed(t *testing.T) {
	g := obligedGhost(t, 0, 2)
	for i := 0; i < 2; i++ {
		g.markObliged()
		if !g.allowInitiative("chat") {
			t.Fatalf("obligation %d was refused inside the limit", i+1)
		}
		g.endResponding()
	}
	g.markObliged()
	if g.allowInitiative("chat") {
		t.Error("obligations ran past their daily limit")
	}
}

// The forgery hole the Asked bit closes. A brain that invents an errand for
// itself while nobody is there has had an idea, not been given a job, and
// must not be able to mint itself an allowance out of the user's reminders.
func TestAGhostCannotPutItsOwnErrandsOnTheBooks(t *testing.T) {
	g := obligedGhost(t, 1, 5)

	// Nobody is talking to it.
	intend(g, "own", "check the build again", 0)
	list := g.loadIntentions()
	if len(list) != 1 {
		t.Fatalf("intentions = %+v, want one", list)
	}
	if list[0].Asked {
		t.Error("an errand the ghost invented while alone was recorded as asked for")
	}

	// A person is talking to it.
	g.markPrompted()
	intend(g, "theirs", "tell them the build finished", 0)
	for _, it := range g.loadIntentions() {
		if it.Name == "theirs" && !it.Asked {
			t.Error("an errand taken on mid-conversation was not recorded as asked for")
		}
	}
}

// A paid chain is the ghost's own idea running on, not the person talking,
// so an errand taken on inside one is still the ghost's own.
func TestAnErrandTakenOnMidChainIsTheGhostsOwn(t *testing.T) {
	g := obligedGhost(t, 1, 5)
	g.openInitiative()
	if !g.responding() {
		t.Fatal("the chain window did not open")
	}
	intend(g, "own", "look at that again later", 0)
	list := g.loadIntentions()
	if len(list) != 1 || list[0].Asked {
		t.Errorf("intentions = %+v, want one not marked as asked for", list)
	}
}

// Only what a person put on the books opens the window, and fireIntention
// is where that is decided.
func TestOnlyAnAskedIntentionObliges(t *testing.T) {
	g := obligedGhost(t, 1, 5)

	intend(g, "own", "the ghost's own idea", 0)
	g.fireIntention("own")
	if g.obliged() {
		t.Error("the ghost's own errand opened the obligation window")
	}

	g.markPrompted()
	intend(g, "theirs", "what they asked for", 0)
	g.endResponding()
	g.fireIntention("theirs")
	if !g.obliged() {
		t.Error("an errand the person asked for did not open the obligation window")
	}
}

// Fullscreen still wins. An intention raised over a game is held rather
// than shown — it stays on the books unresolved and comes up later, which
// is the property intention.go is built around.
func TestAnObligationStillYieldsToFullscreen(t *testing.T) {
	g := obligedGhost(t, 1, 5)
	g.desktop = desktop.Fixed(true)
	g.markObliged()
	if g.allowInitiative("chat") {
		t.Error("a reminder interrupted something fullscreen")
	}
}
