package ghost

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/quiet"
)

// Initiative: the ghost acting without being addressed. The journal and the
// memory index are worth little while the ghost can only answer — recall
// only fires on an utterance — so this is what turns them into behavior.
//
// The driver decides what counts as unprompted by causality rather than by
// asking the brain, which cannot be trusted to report its own motives. Two
// clocks do the work:
//
//   - promptedUntil: the user did something (or an AI result they are
//     waiting on arrived). Anything the ghost emits inside this window is a
//     response and costs nothing.
//   - initiativeUntil: the ghost paid for an unprompted action. The rest of
//     that chain — the say after the chat, the imagine after the rewrite —
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
	// bootWindow is the much shorter grace after launch. Starting a ghost
	// invites a greeting, not a minute of free unprompted chatter, so boot
	// opens the response window only long enough for the brain to say
	// hello.
	bootWindow = 2 * time.Second
	// defaultActionsPerDay is roughly one unprompted remark every couple
	// of waking hours: present, not pestering.
	defaultActionsPerDay = 12
	// curiosityKey persists spends so restarting the ghost does not refill
	// the allowance.
	curiosityKey = internalKeyPrefix + "curiosity"
)

// CuriosityConfig is the [curiosity] portion of the user config. It is the
// user's dial, not the ghost's: a ghost declares no allowance and cannot
// raise one.
type CuriosityConfig struct {
	// ActionsPerDay caps unprompted actions in any rolling 24 hours. Zero
	// disables initiative entirely; absent means defaultActionsPerDay.
	ActionsPerDay *int `toml:"actions_per_day"`
}

// loadCuriosityConfig reads [curiosity] from the user config file.
func loadCuriosityConfig() (int, error) {
	path, err := quiet.ConfigPath()
	if err != nil {
		return 0, err
	}
	var root struct {
		Curiosity CuriosityConfig `toml:"curiosity"`
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	if err == nil {
		if err := toml.Unmarshal(data, &root); err != nil {
			return 0, fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	if root.Curiosity.ActionsPerDay == nil {
		return defaultActionsPerDay, nil
	}
	return *root.Curiosity.ActionsPerDay, nil
}

// curiosity is a persisted daily allowance of unprompted actions.
type curiosity struct {
	mu     sync.Mutex
	limit  int
	spends []time.Time
	store  Store
	log    func(string)
	now    func() time.Time
}

func newCuriosity(limit int, store Store, log func(string)) *curiosity {
	c := &curiosity{limit: limit, store: store, log: log, now: time.Now}
	c.load()
	return c
}

func (c *curiosity) load() {
	raw, ok, err := storeGet(c.store, curiosityKey)
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
func (c *curiosity) spend() (ok bool, remaining int) {
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

// persist writes the spend log; callers hold the lock.
func (c *curiosity) persist() {
	b, err := json.Marshal(c.spends)
	if err != nil {
		return
	}
	if err := c.store.Set(curiosityKey, string(b)); err != nil {
		c.log(fmt.Sprintf("haunt: curiosity: persisting: %v", err))
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

// endResponding closes both windows. Going idle means nobody is waiting on
// the ghost any more, whatever they last said, and that any chain it was
// working through is finished. Each idle is therefore a fresh occasion the
// ghost must pay for — without this, an idle threshold shorter than these
// windows would hand it free chatter, which is precisely the chatter the
// allowance exists to ration.
func (g *Ghost) endResponding() {
	g.initMu.Lock()
	defer g.initMu.Unlock()
	g.promptedUntil = time.Time{}
	g.initiativeUntil = time.Time{}
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
	if g.responding() {
		return true
	}
	if g.curiosity == nil || g.curiosity.limit <= 0 {
		g.opts.Log(fmt.Sprintf("haunt: initiative: %s dropped (curiosity disabled)", what))
		return false
	}
	ok, remaining := g.curiosity.spend()
	if !ok {
		g.opts.Log(fmt.Sprintf("haunt: initiative: %s dropped (daily curiosity spent)", what))
		return false
	}
	g.openInitiative()
	g.opts.Log(fmt.Sprintf("haunt: initiative: %s on the ghost's own account (%d left today)", what, remaining))
	return true
}
