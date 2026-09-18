package ghost

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// Initiative: the ghost acting without being addressed. On hermes that is
// an event reaching the model at all (hermesbrain.go's unprompted), charged
// before it is sent and refunded if the ghost says nothing.
//
// The driver decides what counts as unprompted by causality rather than by
// asking the brain, which cannot be trusted to report its own motives. Two
// clocks do the work:
//
//   - promptedUntil: the user did something (or an AI result they are
//     waiting on arrived). Anything the ghost emits inside this window is a
//     response and costs nothing.
//   - initiativeUntil: the ghost paid for an unprompted action. The rest of
//     that chain — the say after the look, the picture after the remark —
//     rides free until the window closes. It is deliberately *not*
//     extendable while open, so one charge buys one chain rather than an
//     unbounded cascade.
//
// Outside both windows, an interrupting intent spends from a daily
// allowance, and is dropped when that is gone. Quiet hours and (later) the
// fullscreen rule bound *when* initiative may fire; this bounds how often.
const (
	// promptedWindow is how long after user input the ghost is still
	// considered to be responding. Generous, because a slow local model
	// can take tens of seconds to answer.
	promptedWindow = 60 * time.Second
	// initiativeWindow is how long a paid-for chain may keep acting.
	initiativeWindow = 60 * time.Second
	// obligationWindow is how long after an obligation comes due the ghost
	// is still discharging it. As generous as promptedWindow and for the
	// same reason: a slow local model can take tens of seconds, and a
	// reminder that arrives after its window closed would be charged to
	// the ghost's whims and dropped when those ran out.
	obligationWindow = 60 * time.Second
	// bootWindow is the much shorter grace after launch. Starting a ghost
	// invites a greeting, not a minute of free unprompted chatter, so boot
	// opens the response window only long enough for the brain to say
	// hello.
	bootWindow = 2 * time.Second
	// curiosityKey persists spends so restarting the ghost does not refill
	// the allowance.
	curiosityKey = internalKeyPrefix + "curiosity"
	// obligationKey persists the obligation allowance, likewise.
	obligationKey = internalKeyPrefix + "obligation"
)

// allowance is a persisted daily ration. Two exist: unprompted actions (the
// curiosity budget) and things the person put on the ghost's books (the
// obligation budget; see markObliged), kept apart because a reminder you
// asked for is not a whim.
type allowance struct {
	mu     sync.Mutex
	key    string
	limit  int
	spends []time.Time
	store  Store
	log    *hlog.Logger
	now    func() time.Time
	topic  hlog.Topic
}

func newAllowance(key string, limit int, store Store, log *hlog.Logger, topic hlog.Topic) *allowance {
	c := &allowance{key: key, limit: limit, store: store, log: log, now: time.Now, topic: topic}
	c.load()
	return c
}

// newCuriosity is the allowance that rations interruptions.
func newCuriosity(limit int, store Store, log *hlog.Logger) *allowance {
	return newAllowance(curiosityKey, limit, store, log, hlog.Curiosity)
}

// newObligation is the allowance for things a person put on the ghost's
// books. See markObliged for what separates it from curiosity.
func newObligation(limit int, store Store, log *hlog.Logger) *allowance {
	return newAllowance(obligationKey, limit, store, log, hlog.Curiosity)
}

func (c *allowance) load() {
	raw, ok, err := c.store.Get(c.key)
	if err != nil || !ok {
		return
	}
	var spends []time.Time
	if json.Unmarshal([]byte(raw), &spends) != nil {
		return
	}
	c.spends = spends
}

// spend consumes one unprompted action, reporting whether it was allowed.
func (c *allowance) spend() (ok bool, remaining int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := c.now().Add(-24 * time.Hour)
	kept := c.spends[:0]
	for _, t := range c.spends {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	c.spends = kept

	if len(c.spends) >= c.limit {
		return false, 0
	}
	c.spends = append(c.spends, c.now())
	c.persist()
	return true, c.limit - len(c.spends)
}

// refund puts back the most recent spend. It exists for one case: an
// action that passed this allowance and was then refused by a ceiling above
// it. The alternative — asking the ceiling first — would have the ghost
// spending a share of the household budget on something its own budget
// could not afford anyway.
func (c *allowance) refund() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.spends) == 0 {
		return
	}
	c.spends = c.spends[:len(c.spends)-1]
	c.persist()
}

// persist writes the spend log; callers hold the lock.
func (c *allowance) persist() {
	b, err := json.Marshal(c.spends)
	if err != nil {
		return
	}
	if err := c.store.Set(c.key, string(b)); err != nil {
		c.log.Warn(c.topic, "persisting the spend: %v", err)
	}
}

// promptedEvent reports whether an event counts as the user addressing the
// ghost. Boot counts: launching a ghost is a thing a person did, and a
// greeting is not an interruption. A message from another ghost does not —
// otherwise a pair of ghosts could talk each other into unlimited
// initiative.
func promptedEvent(e proto.Event) bool {
	switch e.Type {
	case proto.EventClick, proto.EventDragStart, proto.EventDragEnd, proto.EventBoot:
		return true
	case proto.EventMessage:
		return e.From == ""
	}
	return false
}

// markPrompted opens the response window: the user just did something, or a
// result they are waiting on has arrived.
func (g *Ghost) markPrompted() { g.markPromptedFor(promptedWindow) }

func (g *Ghost) markPromptedFor(d time.Duration) {
	g.initMu.Lock()
	defer g.initMu.Unlock()
	if until := time.Now().Add(d); until.After(g.promptedUntil) {
		g.promptedUntil = until
	}
}

// Obligations, and why they are not curiosity.
//
// The daily allowance rations the ghost's *whims*: twelve a day is tuned
// for "nice weather today", and it is persisted so that restarting is not a
// way to refill it. A reminder the person asked for is not a whim, and used
// to come out of the same twelve — so "remind me in ten minutes to take the
// pizza out" competed with small talk and was dropped outright once the
// small talk had won. The intention was not lost, because discharge is
// deliberate and it stayed on the books, but it was silenced, which is the
// failure mode the top of intention.go says the whole type exists to avoid.
//
// So there is a second allowance, and the line between them is **who
// decided**: the ghost choosing to speak is curiosity, and the ghost
// discharging something a person put on its books is an obligation.
//
// That line is checkable, which "chores versus spontaneity" never would
// have been. There are exactly two ways to put something on the books, and
// a brain can forge neither:
//
//   - An intention taken on while the person was talking to the ghost.
//     Whether they were is answered by respondingToUser at the moment the
//     intention is created, driver-side, and recorded on the intention
//     itself (Intention.Asked). A brain that invents an errand for itself
//     while nobody is there gets an ordinary one, charged to curiosity, and
//     cannot say otherwise.
//   - A trigger in the manifest, which the author declared and the person
//     accepted when they installed the ghost.
//
// Without the first of those the hole is obvious: a brain could intend
// something a millisecond out, over and over, and mint obligations until
// the user's own reminders had nothing left to spend.

// markObliged opens the window in which the ghost is discharging something
// it was asked to do, rather than saying something it thought of.
func (g *Ghost) markObliged() {
	g.initMu.Lock()
	defer g.initMu.Unlock()
	if until := time.Now().Add(obligationWindow); until.After(g.obligedUntil) {
		g.obligedUntil = until
	}
}

// obliged reports whether the ghost is inside that window.
func (g *Ghost) obliged() bool {
	g.initMu.Lock()
	defer g.initMu.Unlock()
	return time.Now().Before(g.obligedUntil)
}

// endResponding closes all three windows. Going idle means nobody is
// waiting on the ghost any more, whatever they last said, and that any
// chain it was working through is finished. Each idle is therefore a fresh
// occasion the ghost must pay for — without this, an idle threshold shorter
// than these windows would hand it free chatter, which is precisely the
// chatter the allowance exists to ration.
//
// The obligation window goes with them, and for the same reason. An
// intention that came due a moment before the person went idle would
// otherwise leave the window open across the idle event, and the remark the
// ghost then makes about nothing in particular would be charged to their
// reminders. The cost of being wrong this way round is small: an obligation
// caught by an idle that raced it falls back to curiosity rather than being
// dropped, and the intention stays on the books unresolved either way.
func (g *Ghost) endResponding() {
	g.initMu.Lock()
	defer g.initMu.Unlock()
	g.promptedUntil = time.Time{}
	g.initiativeUntil = time.Time{}
	g.obligedUntil = time.Time{}
}

// responding reports whether the ghost is inside a window where acting is a
// response rather than an interruption.
func (g *Ghost) responding() bool {
	g.initMu.Lock()
	defer g.initMu.Unlock()
	now := time.Now()
	return now.Before(g.promptedUntil) || now.Before(g.initiativeUntil)
}

// respondingToUser is the narrower question: is a person waiting on this?
// Only such chains may extend the response window when their AI results
// arrive. Letting a paid chain refresh itself would turn one charge into an
// unbounded cascade of them.
func (g *Ghost) respondingToUser() bool {
	g.initMu.Lock()
	defer g.initMu.Unlock()
	return time.Now().Before(g.promptedUntil)
}

// openInitiative starts a paid chain window. Not extendable while open: one
// charge buys one chain.
func (g *Ghost) openInitiative() {
	g.initMu.Lock()
	defer g.initMu.Unlock()
	if now := time.Now(); now.After(g.initiativeUntil) {
		g.initiativeUntil = now.Add(initiativeWindow)
	}
}

// allowInitiative gates an intent that would interrupt. Responses pass
// freely; anything else spends from the daily allowance and is dropped when
// it is exhausted. Refusal is silent to the brain — like quiet hours, this
// is driver policy, and telling the brain would only invite it to complain
// in a balloon it also is not allowed to show.
func (g *Ghost) allowInitiative(what string) bool {
	ok, _ := g.chargeInitiative(what)
	return ok
}

// noRefund is what a charge that spent nothing hands back.
func noRefund() {}

// chargeInitiative is allowInitiative for a caller that may want its money
// back: a wake that the model answered with silence interrupted nobody, and
// attention is charged when the ghost speaks, not when it is woken (see
// hermesbrain.go). The refund puts back whichever allowance paid, and is a
// no-op when none did.
func (g *Ghost) chargeInitiative(what string) (bool, func()) {
	if g.responding() {
		return true, noRefund
	}
	// Never over a game or a film. This is the rule the design has always
	// stated and could not enforce until the driver could see a fullscreen
	// window.
	if g.desktop != nil && g.desktop.Fullscreen() {
		g.log.Debug(hlog.Curiosity, "initiative: %s dropped (something is fullscreen)", what)
		return false, noRefund
	}
	// An obligation the person put on the books, rather than something the
	// ghost thought of. Its own allowance, and deliberately not subject to
	// the household ceiling below: that ceiling exists because three ghosts
	// each politely within their own curiosity still add up to three times
	// the interruptions, which is an argument about whims. Three ghosts
	// holding reminders you set them are three sets of reminders you asked
	// for, and letting one ghost's errand eat another's would make the
	// ceiling a lottery over things the person is waiting on.
	if g.obliged() {
		if g.obligation == nil || g.obligation.limit <= 0 {
			g.log.Debug(hlog.Curiosity, "obligation: %s dropped (obligations disabled)", what)
			return false, noRefund
		}
		ok, remaining := g.obligation.spend()
		if !ok {
			g.log.Debug(hlog.Curiosity, "obligation: %s dropped (daily obligations spent)", what)
			return false, noRefund
		}
		g.openInitiative()
		g.log.Debug(hlog.Curiosity, "obligation: %s (%d left today)", what, remaining)
		return true, g.obligation.refund
	}
	if g.curiosity == nil || g.curiosity.limit <= 0 {
		g.log.Debug(hlog.Curiosity, "initiative: %s dropped (curiosity disabled)", what)
		return false, noRefund
	}
	ok, remaining := g.curiosity.spend()
	if !ok {
		g.log.Debug(hlog.Curiosity, "initiative: %s dropped (daily curiosity spent)", what)
		return false, noRefund
	}
	// And the household ceiling, if there are other ghosts in the room.
	// Three ghosts each within their own allowance still add up to three
	// times the interruptions and three times the bill.
	if g.opts.Initiative != nil && !g.opts.Initiative(what) {
		g.curiosity.refund()
		g.log.Debug(hlog.Curiosity, "initiative: %s dropped (the room's daily allowance is spent)", what)
		return false, noRefund
	}
	g.openInitiative()
	g.log.Debug(hlog.Curiosity, "initiative: %s on the ghost's own account (%d left today)", what, remaining)
	return true, g.curiosity.refund
}
