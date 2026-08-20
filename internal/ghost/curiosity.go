package ghost

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/quiet"
	"github.com/aaronjauregui/haunt/internal/session"
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
	// sleepKey persists the consolidation allowance, for the same reason.
	sleepKey = internalKeyPrefix + "sleep"
)

// AnimationConfig is the [animation] portion of the user config.
type AnimationConfig struct {
	// ReducedMotion stops idle animation — blinking, breathing, anything on
	// an automatic trigger. Clips a brain asks for still play: those are the
	// ghost saying something, and a companion that ignores its own brain is
	// broken rather than considerate.
	ReducedMotion bool `toml:"reduced_motion"`
}

// loadAnimationConfig reads [animation] from the user config file.
func loadAnimationConfig() (AnimationConfig, error) {
	path, err := quiet.ConfigPath()
	if err != nil {
		return AnimationConfig{}, err
	}
	var root struct {
		Animation AnimationConfig `toml:"animation"`
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return AnimationConfig{}, err
	}
	if err == nil {
		if err := toml.Unmarshal(data, &root); err != nil {
			return AnimationConfig{}, fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	return root.Animation, nil
}

// AwarenessConfig is the [awareness] portion of the user config: what the
// ghost is allowed to notice about your screen.
type AwarenessConfig struct {
	// Ignore lists applications never to report, matched case-insensitively
	// by substring. Password managers and banking belong here, and the
	// defaults cover the obvious ones so the careless case is not the
	// dangerous one.
	Ignore []string `toml:"ignore"`
	// Dwell is how long in one window before the ghost may remark on it
	// ("25m" by default). One of three thresholds people actually want to
	// tune: this is the difference between a companion that notices you are
	// stuck and one that interrupts you while you are working. The session
	// model's remaining thresholds are fixed.
	Dwell string `toml:"dwell"`
	// Away is how long a gap counts as having been away from the machine,
	// so coming back is worth remarking on ("90m" by default).
	Away string `toml:"away"`
	// LateHour is the hour after which still working is itself worth
	// noticing, in local time (23 by default), running until the small
	// hours of the next morning. Negative switches the shape off, which is
	// the setting for people whose day starts at eleven at night.
	LateHour *int `toml:"late_hour"`

	// Session is the parsed form of the above, filled in at load.
	Session session.Config `toml:"-"`
}

// defaultIgnoredApps are never watched unless the user replaces the list.
var defaultIgnoredApps = []string{
	"1password", "bitwarden", "keepass", "gnome-keyring", "seahorse",
	"polkit", "pinentry", "gnome-disks", "banking",
}

// loadAwarenessConfig reads [awareness] from the user config file.
func loadAwarenessConfig() (AwarenessConfig, error) {
	path, err := quiet.ConfigPath()
	if err != nil {
		return AwarenessConfig{}, err
	}
	var root struct {
		Awareness AwarenessConfig `toml:"awareness"`
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return AwarenessConfig{}, err
	}
	if err == nil {
		if err := toml.Unmarshal(data, &root); err != nil {
			return AwarenessConfig{}, fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	cfg := root.Awareness
	if cfg.Ignore == nil {
		cfg.Ignore = defaultIgnoredApps
	}
	if cfg.Dwell != "" {
		d, err := time.ParseDuration(cfg.Dwell)
		if err != nil {
			return AwarenessConfig{}, fmt.Errorf("parsing %s: [awareness] dwell %q: %w", path, cfg.Dwell, err)
		}
		cfg.Session.Dwell = d
	}
	if cfg.Away != "" {
		d, err := time.ParseDuration(cfg.Away)
		if err != nil {
			return AwarenessConfig{}, fmt.Errorf("parsing %s: [awareness] away %q: %w", path, cfg.Away, err)
		}
		cfg.Session.AwayLong = d
	}
	if cfg.LateHour != nil {
		h := *cfg.LateHour
		if h > 23 || (h >= 0 && h < session.EarliestLateHour) {
			return AwarenessConfig{}, fmt.Errorf(
				"parsing %s: [awareness] late_hour %d: use %d-23, or a negative number to switch it off — "+
					"late runs until the small hours, so anything earlier would make the whole day late",
				path, h, session.EarliestLateHour)
		}
		cfg.Session.LateHour = h
	}
	return cfg, nil
}

// CuriosityConfig is the [curiosity] portion of the user config. It is the
// user's dial, not the ghost's: a ghost declares no allowance and cannot
// raise one.
type CuriosityConfig struct {
	// ActionsPerDay caps unprompted actions in any rolling 24 hours. Zero
	// disables initiative entirely; absent means defaultActionsPerDay.
	ActionsPerDay *int `toml:"actions_per_day"`
	// ActionsPerDayTotal caps unprompted actions across every resident
	// ghost together. Absent means the ceiling is derived from the number
	// of ghosts in the room (see internal/troupe); it is here for the user
	// who wants to say "however many of you there are, twenty a day".
	ActionsPerDayTotal *int `toml:"actions_per_day_total"`
}

// Curiosity reports the user's [curiosity] settings: the per-ghost daily
// allowance, and the explicit ceiling for the room if they set one (-1 when
// they did not). The supervisor needs the second and the ghost needs the
// first, and neither should be parsing the config file a second time.
func Curiosity() (perGhost, room int, err error) {
	cfg, err := curiosityConfig()
	if err != nil {
		return 0, 0, err
	}
	perGhost = defaultActionsPerDay
	if cfg.ActionsPerDay != nil {
		perGhost = *cfg.ActionsPerDay
	}
	room = -1
	if cfg.ActionsPerDayTotal != nil {
		room = *cfg.ActionsPerDayTotal
	}
	return perGhost, room, nil
}

// curiosityConfig reads [curiosity] from the user config file.
func curiosityConfig() (CuriosityConfig, error) {
	path, err := quiet.ConfigPath()
	if err != nil {
		return CuriosityConfig{}, err
	}
	var root struct {
		Curiosity CuriosityConfig `toml:"curiosity"`
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return CuriosityConfig{}, err
	}
	if err == nil {
		if err := toml.Unmarshal(data, &root); err != nil {
			return CuriosityConfig{}, fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	return root.Curiosity, nil
}

// allowance is a persisted daily ration of things the user did not ask
// for. Two exist: unprompted actions (the curiosity budget) and
// consolidation passes (memory.go's sleep), kept apart because they ration
// different costs — one is the person's attention, the other their bill —
// and a ghost should not go quiet because it spent the night thinking.
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

func (c *allowance) load() {
	raw, ok, err := storeGet(c.store, c.key)
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
	// Never over a game or a film. This is the rule the design has always
	// stated and could not enforce until the driver could see a fullscreen
	// window.
	if g.desktop != nil && g.desktop.Fullscreen() {
		g.log.Debug(hlog.Curiosity, "initiative: %s dropped (something is fullscreen)", what)
		return false
	}
	if g.curiosity == nil || g.curiosity.limit <= 0 {
		g.log.Debug(hlog.Curiosity, "initiative: %s dropped (curiosity disabled)", what)
		return false
	}
	ok, remaining := g.curiosity.spend()
	if !ok {
		g.log.Debug(hlog.Curiosity, "initiative: %s dropped (daily curiosity spent)", what)
		return false
	}
	// And the household ceiling, if there are other ghosts in the room.
	// Three ghosts each within their own allowance still add up to three
	// times the interruptions and three times the bill.
	if g.opts.Initiative != nil && !g.opts.Initiative(what) {
		g.curiosity.refund()
		g.log.Debug(hlog.Curiosity, "initiative: %s dropped (the room's daily allowance is spent)", what)
		return false
	}
	g.openInitiative()
	g.log.Debug(hlog.Curiosity, "initiative: %s on the ghost's own account (%d left today)", what, remaining)
	return true
}
