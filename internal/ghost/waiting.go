package ghost

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aaronjauregui/haunt/internal/balloon"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/wlclient"
)

// Waiting on a model. A local backend can take a second to answer, or ten,
// or — when it has wedged — never, and until the first token arrives there
// is nothing on screen to say so. A ghost sitting silent with its mouth
// moving does not read as thinking; it reads as broken, and the only way to
// find out otherwise is to go and read the backend's log.
//
// So the driver shows the wait. The indicator is the driver's, not the
// brain's, because every ghost needs one and no ghost author should have to
// think about slow backends to get it; the words beside it are the brain's
// (the `wait` field on the chat and imagine intents), because waiting is
// something a character does in its own voice.
//
// Three rules it obeys, all of them about not being in the way:
//   - It appears only after waitDelay. A fast local model answers inside
//     that, and a dot pattern that flashed on every reply would be its own
//     kind of noise.
//   - It never takes a balloon that is already on screen. Something the
//     user is still reading — a paged reply, mid-page — outranks the news
//     that another answer is coming. It waits, and appears if and when the
//     balloon goes away.
//   - It is chrome, not dialogue: not billed to the curiosity budget, not
//     written to history, silent during quiet hours, and never painted over
//     a line the user is typing.
const (
	// waitDelay is how long a request may take before the ghost admits it
	// is waiting.
	waitDelay = 500 * time.Millisecond
	// waitFramePeriod is the dot animation's step.
	waitFramePeriod = 350 * time.Millisecond
)

// sessionContext is the context AI work should hang off: cancelled when the
// ghost is asked to stop, so a model still thinking about an answer nobody
// will see does not hold the shutdown open. Background for tests, which
// drive these paths without a session.
func (g *Ghost) sessionContext() context.Context {
	if g.runCtx == nil {
		return context.Background()
	}
	return g.runCtx
}

// stoppingC is the teardown signal, nil-safe for tests.
func (g *Ghost) stoppingC() <-chan struct{} { return g.stopping }

// firstTokenTimeout is how long a chat may go without producing a single
// token before the driver abandons it, from the user's config.
func (g *Ghost) firstTokenTimeout() time.Duration {
	if g.aiSvc == nil {
		return 30 * time.Second
	}
	return g.aiSvc.FirstTokenTimeout()
}

// waiter is one in-flight "the ghost is thinking" indicator.
type waiter struct {
	stop chan struct{}
	once sync.Once
	done chan struct{}
}

// startWaiting shows the waiting indicator until the returned waiter is
// stopped: after waitDelay, and only once nothing else is using the
// balloon. text is what the brain wants said while waiting, and may be
// empty — the dots alone are a complete signal.
func (g *Ghost) startWaiting(text string) *waiter {
	w := &waiter{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		select {
		case <-w.stop:
			return
		case <-g.done:
			return
		case <-g.stoppingC():
			return
		case <-time.After(waitDelay):
		}

		shown := false
		frame := 0
		for {
			if g.drawWaiting(text, frame, shown) {
				shown = true
				// Reduced motion gets the indicator, not the animation: the
				// dots are decoration on top of the fact that it is there.
				if g.reducedMotion {
					select {
					case <-w.stop:
						g.clearWaiting()
						return
					case <-g.done:
						return
					case <-g.stoppingC():
						return
					}
				}
				frame = (frame + 1) % balloon.WaitFrames
			}
			select {
			case <-w.stop:
				if shown {
					g.clearWaiting()
				}
				return
			case <-g.done:
				return
			case <-g.stoppingC():
				// The compositor connection is on its way out; painting
				// into it now is at best pointless and at worst a hang.
				return
			case <-time.After(waitFramePeriod):
			}
		}
	}()
	return w
}

// Stop ends the indicator, blocking until anything it drew is gone.
func (w *waiter) Stop() {
	if w == nil {
		return
	}
	w.once.Do(func() { close(w.stop) })
	<-w.done
}

// drawWaiting paints one frame, reporting whether the indicator is on
// screen. It declines — and keeps declining, harmlessly, until the next
// frame — while anything else owns the balloon.
func (g *Ghost) drawWaiting(text string, frame int, shown bool) bool {
	select {
	case <-g.stoppingC():
		return false
	default:
	}
	if muted, _ := g.quiet.Active(); muted {
		return false
	}
	if g.inputOpen() {
		return false
	}
	rend := g.renderer()
	if rend == nil {
		return false
	}
	if !shown && g.balloonBusy() {
		// Someone is reading something. Wait for it rather than snatching
		// it away.
		return false
	}
	sp := rend.RenderWaiting(text, frame)

	// Repaint in place where possible: the dots step several times a
	// second, and recreating the surface for each frame is the flicker the
	// input line already taught us to avoid.
	g.blnMu.Lock()
	bln := g.bln
	g.blnMu.Unlock()
	if shown && bln != nil {
		if err := bln.Redraw(sp); err == nil {
			return true
		} else if !errors.Is(err, wlclient.ErrSizeChanged) {
			g.log.Warn(hlog.Balloon, "waiting: redraw: %v", err)
		}
	}
	g.showBalloonSprite(sp)
	g.markWaiting(true)
	return true
}

// clearWaiting takes the indicator down, unless something has since
// replaced it — the first token of a reply, typically, which arrives on
// another goroutine and is exactly what we were waiting for.
func (g *Ghost) clearWaiting() {
	g.blnMu.Lock()
	mine := g.blnWaiting
	g.blnMu.Unlock()
	if mine {
		g.hideBalloon()
	}
}

// markWaiting records whether the balloon currently on screen is the
// waiting indicator rather than something the ghost said.
func (g *Ghost) markWaiting(v bool) {
	g.blnMu.Lock()
	g.blnWaiting = v
	g.blnMu.Unlock()
}

// balloonBusy reports whether a balloon the user might be reading is on
// screen. The indicator's own balloon does not count.
func (g *Ghost) balloonBusy() bool {
	g.blnMu.Lock()
	defer g.blnMu.Unlock()
	return g.bln != nil && !g.blnWaiting
}
