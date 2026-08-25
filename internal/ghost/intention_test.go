package ghost

import (
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/bus"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// intendingGhost is a ghost with a store and a bus and nothing else: what
// these tests are about is what survives in the store and what reaches the
// brain, neither of which needs a screen.
func intendingGhost(t *testing.T) (*Ghost, *bus.Bus, *memStore) {
	t.Helper()
	s := &memStore{m: map[string]string{}}
	b := bus.New(64)
	g := &Ghost{
		store:    s,
		bus:      b,
		log:      hlog.Discard(),
		manifest: &Manifest{Ghost: GhostSection{Name: "wisp"}},
		timers:   map[string]*time.Timer{},
		done:     make(chan struct{}),
	}
	t.Cleanup(func() { close(g.done) })
	return g, b, s
}

func intend(g *Ghost, name, text string, delayMS int) {
	g.execIntend(proto.Intent{Type: proto.IntentIntend, Name: name, Text: text, DelayMS: delayMS})
}

// The whole reason this exists: an intention is in the store, so a ghost
// restarted at lunchtime still means to do the thing at four.
func TestAnIntentionOutlivesTheSession(t *testing.T) {
	g, _, s := intendingGhost(t)
	intend(g, "build", "tell them the build finished", 60*60*1000)

	// A second ghost over the same store is what a restart looks like.
	next := &Ghost{store: s, log: hlog.Discard(), timers: map[string]*time.Timer{}}
	held := next.loadIntentions()
	if len(held) != 1 {
		t.Fatalf("carried %d intentions across the restart, want 1", len(held))
	}
	if held[0].Text != "tell them the build finished" {
		t.Errorf("text = %q", held[0].Text)
	}
	if held[0].Due.IsZero() {
		t.Error("the moment did not survive")
	}
}

// Taking on the same name again re-arms it rather than adding a second, so
// "check again in ten minutes" does not fill the list with copies of one
// errand — and the ghost still counts it as having been owed since the
// first time it was asked.
func TestReIntendingReplaces(t *testing.T) {
	g, _, _ := intendingGhost(t)
	intend(g, "build", "check the build", 60*1000)
	made := g.loadIntentions()[0].Made

	time.Sleep(5 * time.Millisecond)
	intend(g, "build", "check the build again", 60*1000)

	held := g.loadIntentions()
	if len(held) != 1 {
		t.Fatalf("holding %d, want the one it re-armed: %+v", len(held), held)
	}
	if held[0].Text != "check the build again" {
		t.Errorf("text = %q, want the newer wording", held[0].Text)
	}
	if !held[0].Made.Equal(made) {
		t.Error("re-arming reset how long it has been owed")
	}
}

// Coming due does not discharge it. A reminder raised while the balloon was
// suppressed is still owed, which is the difference between this and a
// fire-and-forget timer.
func TestFiringDoesNotDischarge(t *testing.T) {
	g, b, _ := intendingGhost(t)
	intend(g, "build", "tell them the build finished", 0)
	g.fireIntention("build")

	var raised *proto.Event
	for _, e := range drain(b) {
		if e.Type == proto.EventIntention {
			raised = &e
		}
	}
	if raised == nil {
		t.Fatal("nothing reached the brain")
	}
	if raised.Name != "build" || raised.Text != "tell them the build finished" {
		t.Errorf("event = %+v, want the intention it was", *raised)
	}

	held := g.loadIntentions()
	if len(held) != 1 {
		t.Fatalf("holding %d after firing, want it still owed", len(held))
	}
	if !held[0].Fired {
		t.Error("it is not marked as having come due")
	}
	// And it says so, so the ghost brings it up next time they talk rather
	// than the reminder evaporating.
	if ctx := g.intentionsContext(""); !strings.Contains(ctx, "not dealt with it yet") {
		t.Errorf("context does not say it is outstanding:\n%s", ctx)
	}
}

func TestResolveDischarges(t *testing.T) {
	g, _, _ := intendingGhost(t)
	intend(g, "build", "check the build", 0)
	g.execResolve("build")
	if held := g.loadIntentions(); len(held) != 0 {
		t.Errorf("holding %+v after resolving, want nothing", held)
	}
	// Resolving twice, or resolving something that was never there, is not
	// an error: a brain must be able to be sure without checking first.
	g.execResolve("build")
	g.execResolve("never-existed")
}

// An intention whose moment passed while the ghost was switched off is not
// dropped. Being switched off is not a reason to forget what you were asked
// to do — but it does not fire during startup either, or the first thing
// the ghost says is a reminder instead of hello.
func TestAnOverdueIntentionSurvivesBeingAway(t *testing.T) {
	g, _, _ := intendingGhost(t)
	intend(g, "build", "tell them", 50)
	// Rewrite the due time into the past, which is what coming back to a
	// ghost the next morning looks like.
	held := g.loadIntentions()
	held[0].Due = time.Now().Add(-3 * time.Hour)
	g.saveIntentions(held)
	g.cancelIntention("build")

	if !g.loadIntentions()[0].Overdue(time.Now()) {
		t.Fatal("an intention due three hours ago is not overdue")
	}
	g.armIntentions()

	// Armed, not fired: it waits for the room to settle first.
	g.timersMu.Lock()
	_, armed := g.intentTimers["build"]
	g.timersMu.Unlock()
	if !armed {
		t.Error("an overdue intention was not re-armed after the restart")
	}
	if g.loadIntentions()[0].Fired {
		t.Error("it fired during startup, on top of the greeting")
	}
}

// Something with no moment is carried rather than scheduled: the ghost holds
// it and brings it up when it is relevant.
func TestAnIntentionWithNoMoment(t *testing.T) {
	g, _, _ := intendingGhost(t)
	intend(g, "plants", "mention the plants need watering", 0)

	g.timersMu.Lock()
	_, armed := g.intentTimers["plants"]
	g.timersMu.Unlock()
	if armed {
		t.Error("an intention with no moment was given one")
	}
	// It is a goal, so it rides into a request that is about it rather than
	// into every request for the rest of the year (see inContext).
	ctx := g.intentionsContext("did you water the plants?")
	if !strings.Contains(ctx, "plants") || !strings.Contains(ctx, "no particular moment") {
		t.Errorf("context = %q, want it carried and marked as having no moment", ctx)
	}
	if ctx := g.intentionsContext("what is the weather like"); ctx != "" {
		t.Errorf("context = %q, want a goal left out of a request that is not about it", ctx)
	}
}

// The list is bounded, because it is pasted into every prompt and because a
// companion carrying forty errands is a worse task list than a task list.
func TestIntentionsAreBounded(t *testing.T) {
	g, _, _ := intendingGhost(t)
	for i := range maxIntentions + 5 {
		// Errands rather than goals: goals have a much smaller cap of their
		// own, for a different reason (see TestGoalsAreCappedSeparately).
		intend(g, string(rune('a'+i)), "errand", 60*60*1000)
		time.Sleep(time.Millisecond) // distinct Made times, so oldest is defined
	}
	held := g.loadIntentions()
	if len(held) != maxIntentions {
		t.Fatalf("holding %d, want the cap of %d", len(held), maxIntentions)
	}
	// Oldest out: an intention is owed, not remembered, and the one owed
	// longest is the one most obviously never discharged.
	if held[0].Name != string(rune('a'+5)) {
		t.Errorf("kept from %q, want the oldest five to have gone", held[0].Name)
	}
}

// A brain naming a timer the same as an intention must not cancel it: they
// are different things that happen to share a lock.
func TestTimersAndIntentionsDoNotCollide(t *testing.T) {
	g, _, _ := intendingGhost(t)
	intend(g, "check", "check the build", 60*1000)
	g.setTimer("check", time.Hour)

	g.timersMu.Lock()
	_, isIntention := g.intentTimers["check"]
	_, isTimer := g.timers["check"]
	g.timersMu.Unlock()
	if !isIntention || !isTimer {
		t.Errorf("intention=%v timer=%v, want both to exist", isIntention, isTimer)
	}
	if held := g.loadIntentions(); len(held) != 1 {
		t.Errorf("the timer disturbed the intention: %+v", held)
	}
}

func TestIntentionValidation(t *testing.T) {
	for _, bad := range []proto.Intent{
		{Type: proto.IntentIntend, Text: "something"},
		{Type: proto.IntentIntend, Name: "x"},
		{Type: proto.IntentIntend, Name: "x", Text: "y", DelayMS: -1},
		{Type: proto.IntentResolve},
		{Type: proto.IntentSetAside},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	ok := proto.Intent{Type: proto.IntentIntend, Name: "x", Text: "y"}
	if err := ok.Validate(); err != nil {
		t.Errorf("a carried intention was rejected: %v", err)
	}
}
