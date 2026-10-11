package ghost

import (
	"context"
	"errors"
	"slices"
	"strings"
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
// (the `wait` field on the imagine and act intents), because waiting is
// something a character does in its own voice.
//
// Three rules it obeys, all of them about not being in the way:
//   - It appears only after waitDelay. A fast local model answers inside
//     that, and a dot pattern that flashed on every reply would be its own
//     kind of noise.
//   - It never takes a balloon that is already on screen, and gives its
//     own up to anything that replaces it. Something the user is still
//     reading — a paged reply, mid-page, or the greeting of a turn their
//     words are queued behind — outranks the news that another answer is
//     coming. It waits, and appears if and when the balloon goes away.
//   - There is one on screen at a time. Waits nest — a turn waits on the
//     model, which waits on a tool, which waits on an image backend — and
//     the newest is the one with news, so it speaks and the others hold
//     their peace until it is done.
//   - In a conversation the balloon is the conversation's view, so the
//     indicator is the view's status line instead (waitInConversation):
//     the same words, the dots ticking after them as text, shown at once.
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

// isStopping reports whether teardown has begun.
func (g *Ghost) isStopping() bool {
	select {
	case <-g.stoppingC():
		return true
	default:
		return false
	}
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
	return g.startWaitingFor(func() string { return text })
}

// startWaitingFor is startWaiting for a wait whose words change as it
// goes: text is asked again for every frame.
func (g *Ghost) startWaitingFor(text func() string) *waiter {
	w := &waiter{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer g.recoverPanic(hlog.Balloon, "the waiting indicator")
		// In a conversation the person has just pressed Enter and is
		// watching the view, so it shows at once.
		delay := waitDelay
		if g.inConversation() {
			delay = 0
		}
		select {
		case <-w.stop:
			return
		case <-g.done:
			return
		case <-g.stoppingC():
			return
		case <-time.After(delay):
		}
		g.pushWaiter(w)

		shown := false
		frame := 0
		last := ""
		for {
			if g.topWaiter(w) && g.waitInConversation(text(), frame) {
				shown = false
				if !g.reducedMotion {
					frame = (frame + 1) % balloon.WaitFrames
				}
			} else if g.topWaiter(w) {
				say := text()
				// Reduced motion gets the indicator, not the animation: the
				// dots are decoration on top of the fact that it is there.
				// It is painted again only when its words change or
				// something else has had the balloon.
				still := g.reducedMotion && shown && say == last && g.waitingUp()
				if !still && g.drawWaiting(say, frame, shown) {
					shown = true
					last = say
					if !g.reducedMotion {
						frame = (frame + 1) % balloon.WaitFrames
					}
				}
			} else {
				// A newer wait has the balloon. Starting afresh when it is
				// done repaints this one's words over whatever it left.
				shown = false
			}
			select {
			case <-w.stop:
				// The last wait out takes the indicator down, whichever of
				// them drew it; while an older one is still going, it
				// stays, to be repainted with that one's words.
				if !g.popWaiter(w) {
					g.clearWaiting()
					g.dropWaitStatus()
				}
				return
			case <-g.done:
				g.popWaiter(w)
				return
			case <-g.stoppingC():
				// The compositor connection is on its way out; painting
				// into it now is at best pointless and at worst a hang.
				g.popWaiter(w)
				return
			case <-time.After(waitFramePeriod):
			}
		}
	}()
	return w
}

// pushWaiter makes w the newest wait.
func (g *Ghost) pushWaiter(w *waiter) {
	g.waitMu.Lock()
	g.waiters = append(g.waiters, w)
	g.waitMu.Unlock()
}

// popWaiter removes w, reporting whether other waits are still going.
func (g *Ghost) popWaiter(w *waiter) bool {
	g.waitMu.Lock()
	defer g.waitMu.Unlock()
	g.waiters = slices.DeleteFunc(g.waiters, func(o *waiter) bool { return o == w })
	return len(g.waiters) > 0
}

// topWaiter is whether w is the newest wait, the one that draws.
func (g *Ghost) topWaiter(w *waiter) bool {
	g.waitMu.Lock()
	defer g.waitMu.Unlock()
	return len(g.waiters) > 0 && g.waiters[len(g.waiters)-1] == w
}

// stopped is whether Stop has been called.
func (w *waiter) stopped() bool {
	select {
	case <-w.stop:
		return true
	default:
		return false
	}
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
	if g.balloonBusy() {
		// Someone is reading something, perhaps something that replaced
		// the indicator. Wait for it rather than snatching it away.
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

// waitingUp reports whether the balloon on screen is the indicator.
func (g *Ghost) waitingUp() bool {
	g.blnMu.Lock()
	defer g.blnMu.Unlock()
	return g.blnWaiting
}

// balloonBusy reports whether a balloon the user might be reading is on
// screen. The indicator's own balloon does not count.
func (g *Ghost) balloonBusy() bool {
	g.blnMu.Lock()
	defer g.blnMu.Unlock()
	return g.bln != nil && !g.blnWaiting
}

// waitInConversation shows one frame of the wait as the conversation
// view's status line, reporting whether there is a conversation to show it
// in. It stays out of the way of words already streaming and of a status
// line of the driver's own ("(looking…)", a question), and comes back when
// that one is taken down while the wait goes on. Reduced motion gets the
// words as they are; otherwise the dots after them tick.
func (g *Ghost) waitInConversation(text string, frame int) bool {
	if muted, _ := g.quiet.Active(); muted {
		return g.inConversation()
	}
	line := strings.TrimSpace(text)
	if !g.reducedMotion {
		line = strings.TrimRight(line, "….") + strings.Repeat(".", frame%3+1)
	}
	g.convMu.Lock()
	if !g.conv.open {
		g.convMu.Unlock()
		return false
	}
	free := g.conv.status == "" || g.conv.status == g.conv.waitStatus
	if g.conv.streaming != "" || !free || g.conv.status == line {
		g.convMu.Unlock()
		return true
	}
	g.conv.status, g.conv.waitStatus, g.conv.offset = line, line, 0
	g.convMu.Unlock()
	g.drawInput()
	return true
}

// dropWaitStatus takes the wait's status line down when the last wait is
// over, leaving any other status that replaced it.
func (g *Ghost) dropWaitStatus() {
	g.convMu.Lock()
	line := g.conv.waitStatus
	g.conv.waitStatus = ""
	g.convMu.Unlock()
	if line != "" {
		g.dropStatus(line)
	}
}
