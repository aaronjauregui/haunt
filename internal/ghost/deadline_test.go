package ghost

import (
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/hermes"
)

// testDeadline is a turnDeadline on short clocks, and a channel that is
// closed when it expires.
func testDeadline(t *testing.T) (*turnDeadline, chan struct{}) {
	t.Helper()
	expired := make(chan struct{})
	d := newTurnDeadline(150*time.Millisecond, 100*time.Millisecond, func() { close(expired) })
	t.Cleanup(d.stop)
	return d, expired
}

// expiresWithin reports whether the deadline ran out within wait.
func expiresWithin(expired chan struct{}, wait time.Duration) bool {
	select {
	case <-expired:
		return true
	case <-time.After(wait):
		return false
	}
}

const hauntDraw = hermes.HauntToolPrefix + "draw"

// The opening events are hermes starting the run, not the model answering
// it, so they do not stop the first clock.
func TestTurnDeadlineFirstSignOfLife(t *testing.T) {
	d, expired := testDeadline(t)
	d.event("run.started")
	d.event("message.started")
	if !expiresWithin(expired, time.Second) {
		t.Fatal("a turn that never stirred was not given up on")
	}
	quiet, first, ok := d.expired()
	if !ok || !first || quiet != 150*time.Millisecond {
		t.Errorf("expired() = %v, %v, %v; want the first clock", quiet, first, ok)
	}
}

// A turn that called a tool and then went quiet, as one whose provider
// went down does while hermes waits it out, is given up on.
func TestTurnDeadlineQuietAfterATool(t *testing.T) {
	d, expired := testDeadline(t)
	d.event("run.started")
	d.event("message.started")
	for _, ev := range []hermes.ToolEvent{{Name: hermes.HauntToolPrefix + "face"}, {Name: hermes.HauntToolPrefix + "face", Done: true}} {
		d.event("tool.started")
		d.tool(ev)
	}
	if !expiresWithin(expired, time.Second) {
		t.Fatal("a turn that went quiet after a tool was not given up on")
	}
	quiet, first, ok := d.expired()
	if !ok || first || quiet != 100*time.Millisecond {
		t.Errorf("expired() = %v, %v, %v; want the stall clock", quiet, first, ok)
	}
}

// Events keep a turn alive however long it runs, reasoning among them.
func TestTurnDeadlineEventsKeepItAlive(t *testing.T) {
	d, expired := testDeadline(t)
	d.event("assistant.delta")
	for range 10 {
		time.Sleep(40 * time.Millisecond)
		d.event("tool.progress")
	}
	if expiresWithin(expired, 0) {
		t.Fatal("a turn with events coming was given up on")
	}
}

// One of the ghost's own tools may run as long as it runs; the clock
// starts again when it is done. Several may run at once.
func TestTurnDeadlineHauntToolStopsTheClock(t *testing.T) {
	d, expired := testDeadline(t)
	d.event("tool.started")
	d.tool(hermes.ToolEvent{Name: hauntDraw})
	d.event("tool.started")
	d.tool(hermes.ToolEvent{Name: hermes.HauntToolPrefix + "ask"})
	if expiresWithin(expired, 400*time.Millisecond) {
		t.Fatal("the turn was given up on while the ghost's tools ran")
	}
	d.event("tool.completed")
	d.tool(hermes.ToolEvent{Name: hauntDraw, Done: true})
	if expiresWithin(expired, 300*time.Millisecond) {
		t.Fatal("the turn was given up on while one of the ghost's tools still ran")
	}
	d.event("tool.failed")
	d.tool(hermes.ToolEvent{Name: hermes.HauntToolPrefix + "ask", Done: true, Failed: true})
	if !expiresWithin(expired, time.Second) {
		t.Fatal("a turn quiet after its tools were done was not given up on")
	}
}

// Hermes's own tools are quick and local, and do not stop the clock.
func TestTurnDeadlineHermesToolKeepsTheClock(t *testing.T) {
	d, expired := testDeadline(t)
	d.event("tool.started")
	d.tool(hermes.ToolEvent{Name: "session_search"})
	if !expiresWithin(expired, time.Second) {
		t.Fatal("a turn stuck in one of hermes's own tools was not given up on")
	}
}

// A turn that is over is not given up on afterwards.
func TestTurnDeadlineStops(t *testing.T) {
	d, expired := testDeadline(t)
	d.event("assistant.delta")
	d.stop()
	if expiresWithin(expired, 300*time.Millisecond) {
		t.Fatal("a finished turn expired")
	}
	if _, _, ok := d.expired(); ok {
		t.Error("a finished turn reports a deadline")
	}
}
