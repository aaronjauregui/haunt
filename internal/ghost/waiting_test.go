package ghost

import (
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/wlclient"
)

// A reply that arrives promptly must not be preceded by a flash of dots:
// the indicator is for waits a person would notice, and one that appeared
// on every exchange would be its own kind of noise.
func TestWaitingIndicatorHoldsBackBriefly(t *testing.T) {
	g := pagingGhost(t)
	g.done = make(chan struct{})
	defer close(g.done)

	w := g.startWaiting("thinking…")
	time.Sleep(waitDelay / 2)
	if g.waitingUp() {
		t.Error("the indicator appeared immediately; a fast reply should never show it")
	}
	time.Sleep(waitDelay)
	if !g.waitingUp() {
		t.Fatal("the indicator never appeared")
	}
	w.Stop()
	if g.waitingUp() {
		t.Error("Stop returned with the indicator still on screen")
	}
}

// Something the user is reading outranks the news that another answer is
// coming: the indicator waits for the balloon rather than taking it.
func TestWaitingIndicatorDoesNotStealABalloon(t *testing.T) {
	g := pagingGhost(t)
	// A balloon the ghost is showing — the surface is never touched, only
	// its presence is asked about.
	g.bln = &wlclient.SpriteSurface{}

	if g.drawWaiting("thinking…", 0, false) {
		t.Fatal("the indicator took a balloon that was already on screen")
	}
	if g.waitingUp() {
		t.Error("it marked itself as showing anyway")
	}

	// Once the balloon is gone it may have the space.
	g.bln = nil
	if !g.drawWaiting("thinking…", 0, false) {
		t.Fatal("the indicator never appeared after the balloon went away")
	}
	if !g.waitingUp() {
		t.Error("the indicator is showing but was not marked as such")
	}
}

// It is chrome, not dialogue, and the rules that silence dialogue silence
// it too — including the one that keeps anything from painting over a line
// the user is in the middle of typing.
func TestWaitingIndicatorYieldsToATypedLine(t *testing.T) {
	g := pagingGhost(t)
	g.input.open = true
	if g.drawWaiting("thinking…", 0, false) {
		t.Error("the indicator painted over an open input line")
	}
}

// A balloon that replaces the indicator — the greeting of a turn the
// person's words are queued behind — is not painted over by its next
// frame; the indicator comes back once that balloon has gone.
func TestWaitingIndicatorGivesUpAReplacedBalloon(t *testing.T) {
	g := pagingGhost(t)
	if !g.drawWaiting("thinking…", 0, false) {
		t.Fatal("the indicator never appeared")
	}
	// Something else took the balloon.
	g.bln = &wlclient.SpriteSurface{}
	g.markWaiting(false)
	if g.drawWaiting("thinking…", 1, true) {
		t.Error("the indicator painted over the balloon that replaced it")
	}
	g.bln = nil
	if !g.drawWaiting("thinking…", 2, true) {
		t.Error("the indicator did not come back once the balloon had gone")
	}
}
