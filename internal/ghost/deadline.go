package ghost

import (
	"strings"
	"sync"
	"time"

	"github.com/aaronjauregui/haunt/internal/hermes"
)

// turnDeadline gives up on a hermes turn that has gone quiet. It has two
// clocks, one after the other.
//
// The first runs from the turn's start to its first sign of life, which is
// any event past the run.started and message.started that hermes sends
// before the model is asked anything: a word, a tool call, reasoning. A
// gateway that has said nothing at all has wedged, or its model cannot be
// reached ([hermes] first_token_timeout).
//
// The second runs from every event after that ([hermes] stall_timeout).
// The turn as a whole is not bounded, since a turn can rightly take long,
// but its stream going silent is: when the model's provider goes down
// mid-turn, hermes waits it out with nothing on the stream but keepalive
// comments, which are not events and do not reset the clock, while the
// person's words queue behind the turn. Hermes does notice the turn being
// given up on. The driver closes the stream, the API server's next write
// (a keepalive within 10 s) fails, and it interrupts the run, which ends
// its recovery wait and keeps what the turn had done in the session.
//
// The clock stops while one of the ghost's own tools runs, from its
// tool.started to its tool.completed or tool.failed, since those take as
// long as they take: `draw` waits on an image backend, `ask` on the
// person, and the MCP server's own timeout ([hermes] tool_timeout) bounds
// them. Hermes's own tools (memory, session_search, the skills) do not
// stop it. They read and write local files and hermes's database and ask
// no model, so they are done in well under a second, and one that hung
// would be a wedged gateway, which is what the deadline is for.
type turnDeadline struct {
	first, stall time.Duration
	expire       func()

	mu      sync.Mutex
	timer   *time.Timer
	gen     int  // which arming the running timer belongs to
	stirred bool // whether the turn has shown a sign of life
	tools   int  // the ghost's own tools running
	done    bool
	quiet   time.Duration // the silence that expired the turn; zero until one has
}

// newTurnDeadline starts the first clock. expire is called, once, from the
// timer's goroutine when a clock runs out.
func newTurnDeadline(first, stall time.Duration, expire func()) *turnDeadline {
	d := &turnDeadline{first: first, stall: stall, expire: expire}
	d.mu.Lock()
	d.arm(first)
	d.mu.Unlock()
	return d
}

// arm starts the clock over at limit, or stops it while a tool of the
// ghost's runs. Called with mu held.
func (d *turnDeadline) arm(limit time.Duration) {
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.gen++
	if d.done || d.tools > 0 {
		return
	}
	gen := d.gen
	d.timer = time.AfterFunc(limit, func() {
		d.mu.Lock()
		// A timer stopped too late to keep its function from running
		// belongs to an arming since replaced.
		if d.done || gen != d.gen {
			d.mu.Unlock()
			return
		}
		d.done = true
		d.quiet = limit
		d.mu.Unlock()
		d.expire()
	})
}

// event is one event on the turn's stream, by name.
func (d *turnDeadline) event(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.stirred {
		if name == "run.started" || name == "message.started" {
			return
		}
		d.stirred = true
	}
	d.arm(d.stall)
}

// tool is a tool call starting or finishing. Its event has been heard by
// event already; this only keeps count of the ghost's own tools running.
func (d *turnDeadline) tool(ev hermes.ToolEvent) {
	if !strings.HasPrefix(ev.Name, hermes.HauntToolPrefix) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if ev.Done {
		d.tools = max(d.tools-1, 0)
	} else {
		d.tools++
	}
	d.arm(d.stall)
}

// stop ends both clocks, for a turn that is over.
func (d *turnDeadline) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.done = true
	d.arm(0)
}

// expired is the silence that ended the turn, and whether it was the one
// before the first sign of life.
func (d *turnDeadline) expired() (quiet time.Duration, first, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.quiet, !d.stirred, d.quiet > 0
}
