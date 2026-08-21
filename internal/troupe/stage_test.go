package troupe

import (
	"sync"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// seen collects what one ghost observed, safely across the pump goroutine.
type seen struct {
	mu   sync.Mutex
	list []observation
	got  chan struct{}
}

func newSeen() *seen { return &seen{got: make(chan struct{}, 64)} }

func (s *seen) deliver(from, action, detail string) {
	s.mu.Lock()
	s.list = append(s.list, observation{from, action, detail})
	s.mu.Unlock()
	select {
	case s.got <- struct{}{}:
	default:
	}
}

func (s *seen) all() []observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]observation(nil), s.list...)
}

// wait blocks until n observations have arrived, or gives up.
func (s *seen) wait(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		s.mu.Lock()
		got := len(s.list)
		s.mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-s.got:
		case <-deadline:
			t.Fatalf("only %d of %d observations arrived", got, n)
		}
	}
}

// A ghost sees what the others do and never its own reflection: a ghost
// reacting to its own remark is a loop with no second party in it at all.
func TestEveryoneButYourself(t *testing.T) {
	stage := NewStage(hlog.Discard())
	a, b, c := stage.For("wisp"), stage.For("whisper"), stage.For("mayura")
	sb, sc := newSeen(), newSeen()
	sa := newSeen()
	a.Observe(sa.deliver)
	b.Observe(sb.deliver)
	c.Observe(sc.deliver)

	a.Announce(proto.ActedSpoke, "good morning")
	sb.wait(t, 1)
	sc.wait(t, 1)

	for name, s := range map[string]*seen{"whisper": sb, "mayura": sc} {
		got := s.all()
		if len(got) != 1 || got[0].from != "wisp" || got[0].detail != "good morning" {
			t.Errorf("%s saw %+v, want wisp speaking once", name, got)
		}
	}
	if got := sa.all(); len(got) != 0 {
		t.Errorf("wisp observed itself: %+v", got)
	}
}

// A ghost still coming up hears what it missed.
//
// Every ghost boots on its own goroutine and takes its seat some way into
// that, so the first one to finish greets while the others are still
// starting. This test used to assert the opposite — that a seat hears only
// what happens after Observe — which is how a two-ghost session could open
// with one of them never learning the other had arrived, and how
// TestTwoGhostsAtOnce came to fail under load.
func TestASeatHearsWhatHappenedBeforeItListened(t *testing.T) {
	stage := NewStage(hlog.Discard())
	a, b := stage.For("wisp"), stage.For("whisper")
	a.Announce(proto.ActedSpoke, "anyone there?") // b is still booting

	s := newSeen()
	b.Observe(s.deliver)
	a.Announce(proto.ActedSpoke, "now?")

	s.wait(t, 2)
	got := s.all()
	if len(got) != 2 {
		t.Fatalf("saw %+v, want both", got)
	}
	// In order, and the earlier one first: a greeting that arrives after
	// the reply to it is worse than one that never arrives.
	if got[0].detail != "anyone there?" || got[1].detail != "now?" {
		t.Errorf("saw %+v, want them in the order they were said", got)
	}
}

// The bound still holds while a ghost is coming up: a seat nobody is
// draining fills and then drops, so one slow start cannot make the room
// hold everything that happened in it.
func TestAStartingGhostDoesNotQueueForever(t *testing.T) {
	stage := NewStage(hlog.Discard())
	a, b := stage.For("wisp"), stage.For("whisper")
	for i := 0; i < queued*4; i++ {
		a.Announce(proto.ActedMoved, "")
	}

	s := newSeen()
	b.Observe(s.deliver)
	s.wait(t, queued)
	time.Sleep(50 * time.Millisecond)
	if got := s.all(); len(got) != queued {
		t.Errorf("saw %d observations, want the queue's %d", len(got), queued)
	}
}

// A ghost that has left is not in the room, and a seat it has vacated must
// not accumulate for nobody — including one that left without ever having
// listened, which is what a ghost that failed on its way up looks like.
func TestAnnouncingToAGhostThatHasLeft(t *testing.T) {
	stage := NewStage(hlog.Discard())
	a, b := stage.For("wisp"), stage.For("whisper")
	b.Leave()
	a.Announce(proto.ActedSpoke, "anyone there?")

	s := newSeen()
	b.Observe(s.deliver) // too late; the seat is gone
	a.Announce(proto.ActedSpoke, "now?")
	time.Sleep(50 * time.Millisecond)
	if got := s.all(); len(got) != 0 {
		t.Errorf("saw %+v, want nothing: that ghost has gone", got)
	}
}

// The rule that keeps one ghost from stalling another: a bus blocks its
// publisher when the consumer is behind, so a ghost busy with a slow model
// must never be able to hold up the intent loop of the ghost that spoke.
// Late observations are dropped, because they describe something that has
// already happened.
func TestNobodyWaitsForASlowNeighbour(t *testing.T) {
	stage := NewStage(hlog.Discard())
	a, b := stage.For("wisp"), stage.For("whisper")

	block := make(chan struct{})
	started := make(chan struct{}, 1)
	b.Observe(func(from, action, detail string) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-block // wedged, exactly like a ghost in a long model call
	})

	a.Announce(proto.ActedSpoke, "first")
	<-started

	// Far more than the queue holds. If any of this blocked, the test
	// would hang here rather than fail.
	done := make(chan struct{})
	go func() {
		for i := 0; i < queued*4; i++ {
			a.Announce(proto.ActedMoved, "")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a wedged ghost blocked its neighbour's announcements")
	}
	close(block)
}

// Leaving takes the seat off the stage. A ghost that has stopped is not in
// the room, and its pump must not outlive it.
func TestLeavingTheRoom(t *testing.T) {
	stage := NewStage(hlog.Discard())
	a, b := stage.For("wisp"), stage.For("whisper")
	s := newSeen()
	b.Observe(s.deliver)

	a.Announce(proto.ActedSpoke, "before")
	s.wait(t, 1)

	b.Leave()
	a.Announce(proto.ActedSpoke, "after")
	// Nothing to wait for, so give the pump every chance to be wrong.
	time.Sleep(50 * time.Millisecond)
	if got := s.all(); len(got) != 1 {
		t.Errorf("saw %+v, want nothing after leaving", got)
	}

	stage.mu.Lock()
	_, still := stage.seats["whisper"]
	stage.mu.Unlock()
	if still {
		t.Error("the seat is still on the stage")
	}
}
