package troupe

import (
	"sync"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// Ghosts watching each other.
//
// Two ghosts in a room that cannot see each other are two ghosts in two
// rooms. Messages already cross between them (`tell`), but a message is
// addressed: somebody chose to speak to somebody. Observation is the other
// half — a ghost sees what another ghost does in public and may react
// without being spoken to, which is where dynamics nobody wrote come from.
//
// Three things shape this.
//
// **Public means public.** The seat carries an action and a string, not an
// event: there is deliberately no way to hand another ghost a store, a
// prompt, a journal entry or the KV map that rides on a boot event. The
// narrow type is the enforcement — a ghost must not become a side channel
// into another ghost's private state, and a mechanism that *could* leak
// one is one refactor away from doing it.
//
// **Nobody may stall anybody.** A bus blocks its publisher when the
// consumer is behind, so announcing straight into another ghost's bus
// would let a ghost busy with a slow model hold up the intent loop of the
// ghost that spoke. Each seat therefore has a small queue and a pump of
// its own, and an observation that will not fit is dropped: it describes
// something that already happened, and a late one is worth less than the
// ghost that was made to wait for it.
//
// **The room is the boundary.** Only ghosts the supervisor is running can
// see each other. Two `haunt run` processes started separately share a
// screen but not a stage, which is the same answer the room gives about
// positions and the ceiling gives about budgets: what is shared is shared
// by the thing that knows they are together.

// queued is how many observations a ghost may fall behind by. Small on
// purpose: a ghost that is minutes behind on what its neighbour did should
// forget it rather than eventually react to it.
const queued = 16

// Stage is the shared fan-out. One per session.
type Stage struct {
	mu    sync.Mutex
	seats map[string]*Seat
	log   *hlog.Logger
}

// NewStage returns an empty stage. log may be nil.
func NewStage(log *hlog.Logger) *Stage {
	if log == nil {
		log = hlog.Discard()
	}
	return &Stage{seats: map[string]*Seat{}, log: log}
}

// For returns this ghost's seat: the same shared stage with the ghost's
// own name filled in, so it never announces itself to itself and never has
// to say who it is.
//
// The seat's queue is made here rather than in Observe, which is the whole
// of the fix for a race that cost a ghost its neighbour's arrival. Every
// ghost boots on its own goroutine and takes its seat some way into that,
// after the compositor connection and the brain; the first one to finish
// greets while the others are still coming up. Filling the queue at seat
// time means those greetings wait in it, and Observe finds them.
//
// So a nil queue now means exactly one thing — the ghost has left — where
// before it meant "left, or has not arrived yet", and the second of those
// was silently dropping the most interesting moment in the room.
func (s *Stage) For(name string) *Seat {
	s.mu.Lock()
	defer s.mu.Unlock()
	seat := &Seat{stage: s, name: name, in: make(chan observation, queued)}
	s.seats[name] = seat
	return seat
}

// Seat is one ghost's handle on the stage. It satisfies ghost.Stage.
type Seat struct {
	stage *Stage
	name  string

	mu sync.Mutex
	// in holds what this ghost has yet to see. It exists from the moment
	// the seat does, so a neighbour that acts before this ghost has
	// finished booting is queued rather than lost; nil means the ghost has
	// left. watching says somebody is draining it, which only changes the
	// wording of the line logged when the queue is full — a ghost still
	// coming up and a ghost minutes behind are both over capacity, and they
	// are not the same thing to read in a log.
	in       chan observation
	watching bool
	done     chan struct{}
}

type observation struct{ from, action, detail string }

// Announce tells everyone else what this ghost just did in public.
func (s *Seat) Announce(action, detail string) {
	st := s.stage
	st.mu.Lock()
	others := make([]*Seat, 0, len(st.seats))
	for name, seat := range st.seats {
		if name != s.name {
			others = append(others, seat)
		}
	}
	st.mu.Unlock()

	for _, o := range others {
		o.deliver(observation{from: s.name, action: action, detail: detail})
	}
}

// deliver queues an observation for the ghost sitting in this seat,
// dropping it if that ghost is too far behind to care.
func (s *Seat) deliver(o observation) {
	s.mu.Lock()
	in, watching := s.in, s.watching
	s.mu.Unlock()
	if in == nil {
		return // the ghost has left the room
	}
	select {
	case in <- o:
	default:
		why := "too far behind"
		if !watching {
			// The bound is doing what it is for: a ghost this slow to come
			// up has missed the beginning either way, and holding the room
			// open for it is how one bad start stalls everybody.
			why = "still coming up"
		}
		s.stage.log.Debug(hlog.Control, "%s missed what %s did: %s",
			s.name, o.from, why)
	}
}

// Observe starts delivering what other ghosts do to this one. Called once,
// as the ghost comes up; deliver runs on the stage's own goroutine, so it
// must not block for long.
//
// Whatever the neighbours did while this ghost was still starting is
// already in the queue and arrives first, in order. That is the point: the
// thing a ghost most wants to see its neighbour do is arrive, and arriving
// is precisely the moment nobody is listening yet.
func (s *Seat) Observe(deliver func(from, action, detail string)) {
	s.mu.Lock()
	if s.watching || s.in == nil {
		s.mu.Unlock()
		return // already listening, or gone
	}
	in := s.in
	done := make(chan struct{})
	s.done, s.watching = done, true
	s.mu.Unlock()

	go func() {
		for {
			select {
			case o := <-in:
				deliver(o.from, o.action, o.detail)
			case <-done:
				return
			}
		}
	}()
}

// Leave stops delivery and takes the seat off the stage: a ghost that has
// stopped is not in the room, and announcing to it would keep a pump alive
// for nobody.
func (s *Seat) Leave() {
	s.stage.mu.Lock()
	delete(s.stage.seats, s.name)
	s.stage.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		close(s.done)
		s.done = nil
	}
	// Nil whether or not it was ever watched: a ghost that failed on the
	// way up has a queue nobody will ever drain, and leaving it addressable
	// would have the others announcing into it for the rest of the session.
	s.in, s.watching = nil, false
}
