package ghost

import (
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/wlclient"
)

// waitingShown reports whether the indicator is currently on screen, which
// without a compositor is exactly the flag the balloon bookkeeping keeps.
func (g *Ghost) waitingShown() bool {
	g.blnMu.Lock()
	defer g.blnMu.Unlock()
	return g.blnWaiting
}

// A reply that arrives promptly must not be preceded by a flash of dots:
// the indicator is for waits a person would notice, and one that appeared
// on every exchange would be its own kind of noise.
func TestWaitingIndicatorHoldsBackBriefly(t *testing.T) {
	g := pagingGhost(t)
	g.done = make(chan struct{})
	defer close(g.done)

	w := g.startWaiting("thinking…")
	time.Sleep(waitDelay / 2)
	if g.waitingShown() {
		t.Error("the indicator appeared immediately; a fast reply should never show it")
	}
	time.Sleep(waitDelay)
	if !g.waitingShown() {
		t.Fatal("the indicator never appeared")
	}
	w.Stop()
	if g.waitingShown() {
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
	if g.waitingShown() {
		t.Error("it marked itself as showing anyway")
	}

	// Once the balloon is gone it may have the space.
	g.bln = nil
	if !g.drawWaiting("thinking…", 0, false) {
		t.Fatal("the indicator never appeared after the balloon went away")
	}
	if !g.waitingShown() {
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
