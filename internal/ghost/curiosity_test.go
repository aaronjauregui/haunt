package ghost

import (
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/bus"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// TestPromptedEvent: what the driver counts as being addressed. The brain
// never sees this bit, so these are the only ways a ghost gets to act for
// free.
func TestPromptedEvent(t *testing.T) {
	for _, tc := range []struct {
		e    proto.Event
		want bool
	}{
		{proto.Event{Type: proto.EventMessage, Text: "hi"}, true},
		{proto.Event{Type: proto.EventClick}, true},
		{proto.Event{Type: proto.EventDragEnd}, true},
		// Launching a ghost is something a person did, and its greeting is
		// not an interruption.
		{proto.Event{Type: proto.EventBoot}, true},
		// Another ghost is not a person: without this, two ghosts could
		// talk each other into unlimited initiative.
		{proto.Event{Type: proto.EventMessage, Text: "hi", From: "whisper"}, false},
		// Watching another ghost act is not being spoken to by it. Without
		// this, two ghosts in a room would grant each other free
		// initiative simply by existing near one another.
		{proto.Event{Type: proto.EventObserved, From: "whisper", Name: proto.ActedSpoke, Text: "hello"}, false},
		{proto.Event{Type: proto.EventIdle}, false},
		{proto.Event{Type: proto.EventCron, Name: "morning"}, false},
		{proto.Event{Type: proto.EventTimer, Name: "followup"}, false},
		{proto.Event{Type: proto.EventAIReply, Text: "hello"}, false},
	} {
		if got := promptedEvent(tc.e); got != tc.want {
			t.Errorf("promptedEvent(%s from %q) = %v, want %v", tc.e.Type, tc.e.From, got, tc.want)
		}
	}
}

func testCuriosityGhost(t *testing.T, limit int) (*Ghost, func() []string) {
	t.Helper()
	rec, logged := hlog.Recorder()
	g := &Ghost{
		opts: Options{},
		log:  rec,
		// Buffered and unread: these tests care about what publish marks,
		// not about delivery.
		bus: bus.New(64),
	}
	g.curiosity = newCuriosity(limit, &memStore{m: map[string]string{}}, g.log)
	return g, logged
}

// TestInitiativeIsFreeWhileResponding: everything inside the window after
// user input is a reply, however many intents it takes.
func TestInitiativeIsFreeWhileResponding(t *testing.T) {
	g, _ := testCuriosityGhost(t, 1)
	g.publish(proto.Event{Type: proto.EventMessage, Text: "hi"})

	for i := 0; i < 5; i++ {
		if !g.allowInitiative("say") {
			t.Fatalf("intent %d refused while responding", i)
		}
	}
	if spent := len(g.curiosity.spends); spent != 0 {
		t.Errorf("replies charged %d curiosity", spent)
	}
}

// TestInitiativeChargesWhenUnprompted: an idle event grants nothing by
// itself, so the ghost pays.
func TestInitiativeChargesWhenUnprompted(t *testing.T) {
	g, logged := testCuriosityGhost(t, 2)
	g.publish(proto.Event{Type: proto.EventIdle})

	if !g.allowInitiative("chat") {
		t.Fatal("first unprompted action refused")
	}
	if len(g.curiosity.spends) != 1 {
		t.Errorf("spends = %d, want 1", len(g.curiosity.spends))
	}
	if len(logged()) == 0 {
		t.Error("spending curiosity went unlogged")
	}
}

// TestPaidChainRidesFreeButDoesNotRenew: one charge buys one chain — the
// imagine after the rewrite is free, but the window cannot be extended from
// inside, or a single charge would fund an endless cascade.
func TestPaidChainRidesFreeButDoesNotRenew(t *testing.T) {
	g, _ := testCuriosityGhost(t, 5)

	if !g.allowInitiative("chat") {
		t.Fatal("first unprompted action refused")
	}
	// The rest of the chain costs nothing.
	for i := 0; i < 3; i++ {
		if !g.allowInitiative("imagine") {
			t.Fatalf("chain step %d refused", i)
		}
	}
	if spent := len(g.curiosity.spends); spent != 1 {
		t.Errorf("chain cost %d, want 1", spent)
	}

	// An AI result inside a paid chain must not extend the window: only
	// user-driven results do that.
	g.publishAIResult(proto.Event{Type: proto.EventAIReply, Text: "x"}, false)
	g.initMu.Lock()
	g.initiativeUntil = time.Now().Add(-time.Second) // window closes
	g.initMu.Unlock()

	if !g.allowInitiative("chat") {
		t.Fatal("second chain refused with budget remaining")
	}
	if spent := len(g.curiosity.spends); spent != 2 {
		t.Errorf("second chain cost nothing; spends = %d, want 2", spent)
	}
}

// TestUserDrivenResultExtendsTheWindow: a slow model must not turn a reply
// into an interruption. The ai_reply of a user-driven request reopens the
// window so the follow-up imagine stays free.
func TestUserDrivenResultExtendsTheWindow(t *testing.T) {
	g, _ := testCuriosityGhost(t, 5)
	g.publish(proto.Event{Type: proto.EventMessage, Text: "draw a dog"})

	// The model took longer than the window.
	g.initMu.Lock()
	g.promptedUntil = time.Now().Add(-time.Second)
	g.initMu.Unlock()

	g.publishAIResult(proto.Event{Type: proto.EventAIReply, Text: "tags: a dog"}, true)
	if !g.allowInitiative("imagine") {
		t.Fatal("chained imagine refused")
	}
	if spent := len(g.curiosity.spends); spent != 0 {
		t.Errorf("a reply chain cost %d curiosity", spent)
	}
}

// TestCuriosityExhausts: the allowance runs out and stays out.
func TestCuriosityExhausts(t *testing.T) {
	g, logged := testCuriosityGhost(t, 2)
	for i := 0; i < 2; i++ {
		if !g.allowInitiative("say") {
			t.Fatalf("action %d refused early", i)
		}
		// Close the paid window so each action is charged separately.
		g.initMu.Lock()
		g.initiativeUntil = time.Now().Add(-time.Second)
		g.initMu.Unlock()
	}
	if g.allowInitiative("say") {
		t.Error("third action allowed past a limit of 2")
	}
	lines := logged()
	last := lines[len(lines)-1]
	if want := "daily curiosity spent"; !strings.Contains(last, want) {
		t.Errorf("log = %q, want mention of %q", last, want)
	}

	// The user speaking still gets a reply: exhaustion silences initiative,
	// not the ghost.
	g.publish(proto.Event{Type: proto.EventMessage, Text: "hi"})
	if !g.allowInitiative("say") {
		t.Error("spent curiosity also blocked a reply")
	}
}

// TestCuriosityDisabled: zero means the ghost never acts unbidden.
func TestCuriosityDisabled(t *testing.T) {
	g, _ := testCuriosityGhost(t, 0)
	if g.allowInitiative("chat") {
		t.Error("initiative allowed with the allowance disabled")
	}
	g.publish(proto.Event{Type: proto.EventMessage, Text: "hi"})
	if !g.allowInitiative("chat") {
		t.Error("disabling initiative also blocked a reply")
	}
}

// TestCuriosityPersists: restarting is not a way to refill the allowance.
func TestCuriosityPersists(t *testing.T) {
	s := &memStore{m: map[string]string{}}
	log := hlog.Discard()

	first := newCuriosity(2, s, log)
	if ok, _ := first.spend(); !ok {
		t.Fatal("first spend refused")
	}

	second := newCuriosity(2, s, log) // "restart"
	if ok, remaining := second.spend(); !ok || remaining != 0 {
		t.Errorf("after restart: ok=%v remaining=%d, want true/0", ok, remaining)
	}
	if ok, _ := second.spend(); ok {
		t.Error("restart refilled the allowance")
	}
}

// TestCuriosityWindowRolls: spends older than a day stop counting.
func TestCuriosityWindowRolls(t *testing.T) {
	c := newCuriosity(1, &memStore{m: map[string]string{}}, hlog.Discard())
	now := time.Now()
	c.now = func() time.Time { return now }

	if ok, _ := c.spend(); !ok {
		t.Fatal("first spend refused")
	}
	if ok, _ := c.spend(); ok {
		t.Fatal("second spend allowed at a limit of 1")
	}
	now = now.Add(25 * time.Hour)
	if ok, _ := c.spend(); !ok {
		t.Error("allowance never rolled over")
	}
}

// TestIdleClosesTheWindows: each spell of inactivity is its own occasion.
// Neither a recent conversation nor a chain the ghost already paid for
// carries into it — otherwise an idle threshold shorter than the windows
// would hand out free chatter.
func TestIdleClosesTheWindows(t *testing.T) {
	g, _ := testCuriosityGhost(t, 5)

	// A conversation, then the user wanders off.
	g.publish(proto.Event{Type: proto.EventMessage, Text: "hi"})
	g.publish(proto.Event{Type: proto.EventIdle})
	if !g.allowInitiative("chat") {
		t.Fatal("refused with budget remaining")
	}
	if spent := len(g.curiosity.spends); spent != 1 {
		t.Errorf("idle chatter after a chat cost %d, want 1", spent)
	}

	// The user returns and wanders off again: a second occasion, a second
	// charge, even though the first chain's window would still be open.
	g.publish(proto.Event{Type: proto.EventActive})
	g.publish(proto.Event{Type: proto.EventIdle})
	if !g.allowInitiative("chat") {
		t.Fatal("second occasion refused with budget remaining")
	}
	if spent := len(g.curiosity.spends); spent != 2 {
		t.Errorf("second idle cost nothing; spends = %d, want 2", spent)
	}
}

// TestTheRoomCeilingCanRefuse: a ghost inside its own allowance may still
// be turned away by the ceiling above it, and the spend it was about to
// make is put back — otherwise a refused action would quietly cost the
// ghost one of its own twelve.
func TestTheRoomCeilingCanRefuse(t *testing.T) {
	g, logged := testCuriosityGhost(t, 5)
	room := 0
	g.opts.Initiative = func(string) bool {
		room++
		return room <= 1
	}

	if !g.allowInitiative("say") {
		t.Fatal("the first unprompted action should pass both allowances")
	}
	// Not inside the paid chain window, or the second would ride free.
	g.endResponding()
	if g.allowInitiative("say") {
		t.Error("the second should be refused by the room's ceiling")
	}
	if got := len(g.curiosity.spends); got != 1 {
		t.Errorf("the ghost spent %d of its own allowance, want 1: a refused action was charged", got)
	}
	if lines := strings.Join(logged(), "\n"); !strings.Contains(lines, "room's daily allowance") {
		t.Errorf("the refusal should say who refused it:\n%s", lines)
	}
}

// And with nobody else in the room there is no ceiling to consult.
func TestALoneGhostHasNoCeiling(t *testing.T) {
	g, _ := testCuriosityGhost(t, 2)
	if g.opts.Initiative != nil {
		t.Fatal("a ghost on its own should have no ceiling above it")
	}
	if !g.allowInitiative("say") {
		t.Error("a lone ghost inside its allowance should act")
	}
}
