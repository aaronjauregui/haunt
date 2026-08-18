package ghost

import (
	"strings"
	"sync"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// Consent, asked in the moment.
//
// The install sheet is the worst place to decide what a ghost may do: it is
// a wall of checkboxes at the least informed moment there will ever be,
// before the thing has been seen doing anything. So a capability the ghost
// *declared* but was not granted is not simply refused — the first time it
// is actually needed, the driver asks, in the balloon, about the thing that
// is happening right now:
//
//	Wisp would like to generate dialogue with your configured language
//	model. Allow?
//	1. Allow
//	2. Not now
//	3. Never ask again
//
// "Allow" writes the grant and the request proceeds, so the answer arrives
// as if nothing had happened. "Not now" refuses this one. "Never ask again"
// is recorded, because without somewhere to write a refusal down, "no"
// means "ask me again in a minute", which is how a companion becomes a
// nuisance.
//
// What is *not* asked for here, deliberately:
//
//   - Anything the manifest never declared. A ghost may not ask for powers
//     it did not ship asking for; the package is the outer bound and the
//     question is only ever about a subset of it.
//   - `native-brain` and `message`, which are settled before the ghost is
//     running: one chooses how the brain is executed, the other whether a
//     socket exists at all. Neither can be turned on mid-session, so asking
//     would be a lie.
//   - Anything from a background loop. The question follows a thing the
//     ghost was asked to do, so there is something on screen to make sense
//     of it. Window awareness therefore stays behind `haunt perms` for now:
//     a permission popup at login, about nothing in particular, is exactly
//     the pattern this is meant to replace.

// consentGap is how long to leave someone alone after a "not now" before
// the same question may be asked again.
const consentGap = 10 * time.Minute

// The answers, in the order they are offered.
const (
	consentAllow  = "Allow"
	consentNotNow = "Not now"
	consentNever  = "Never ask again"
)

// askedFor remembers what has been asked, so a refusal is respected for a
// while, a question is never on screen twice, and a capability just allowed
// is usable immediately.
type askedFor struct {
	mu   sync.Mutex
	last map[string]time.Time
	busy map[string]bool
	// fresh holds capabilities allowed moments ago, until the grants file
	// the answer was written to has been re-read. Without it the request
	// that provoked the question fails anyway: the watcher polls at most
	// once a second, so "Allow" was followed by a refusal, which is a
	// worse experience than never asking.
	fresh map[string]time.Time
}

func (a *askedFor) init() {
	if a.last == nil {
		a.last = map[string]time.Time{}
		a.busy = map[string]bool{}
		a.fresh = map[string]time.Time{}
	}
}

func (a *askedFor) begin(cap string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.init()
	if a.busy[cap] {
		return false
	}
	if at, ok := a.last[cap]; ok && time.Since(at) < consentGap {
		return false
	}
	a.busy[cap] = true
	return true
}

// end releases the question. allowed says the answer was yes, in which case
// there is nothing to hold off on: the grant itself answers from now on.
func (a *askedFor) end(cap string, allowed bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.init()
	a.busy[cap] = false
	if allowed {
		a.fresh[cap] = time.Now()
		delete(a.last, cap)
		return
	}
	a.last[cap] = time.Now()
}

// justAllowed reports whether cap was granted in the last moment, before
// the file-backed answer has caught up. Deliberately short-lived: the
// grants file is the record, and `haunt perms revoke` has to keep working
// the instant it is used.
func (a *askedFor) justAllowed(cap string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	at, ok := a.fresh[cap]
	return ok && time.Since(at) < freshGrant
}

// freshGrant is how long a just-written grant is trusted from memory, which
// only has to outlast the watcher's poll.
const freshGrant = 3 * time.Second

// mayAsk is which capabilities can be turned on while the ghost is running,
// and are therefore worth asking about. The rest are settled at startup.
var mayAsk = map[string]bool{
	"llm":     true,
	"imagine": true,
	"screen":  true,
	// `tell` is left out until there is a second ghost to talk to: that
	// intent runs on the loop that delivers intents, so asking there would
	// stall the brain while somebody reads a question about a feature that
	// currently does nothing.
}

// ensureCapability reports whether the ghost may do this now, asking the
// person if the manifest declared the capability and they have not answered
// yet. Blocks while the question is on screen, so callers must not hold a
// lock anything else needs — least of all the one AI slot.
func (g *Ghost) ensureCapability(cap string) bool {
	if g.capabilityActive(cap) {
		return true
	}
	if !g.canAsk(cap) {
		return false
	}
	if !g.asked.begin(cap) {
		g.log.Debug(hlog.Session, "not asking about %s again just yet", cap)
		return false
	}
	allowed := false
	defer func() { g.asked.end(cap, allowed) }()

	// Between the check and here, someone may have answered the same
	// question through `haunt perms`.
	if g.capabilityActive(cap) {
		return true
	}

	what := KnownCapabilities[cap]
	question := g.manifest.Ghost.Name + " would like to " + what + ". Allow?"
	g.log.Info(hlog.Session, "asking whether %s may %s", g.manifest.Ghost.Name, cap)

	switch g.ask(question, consentAllow, consentNotNow, consentNever) {
	case consentAllow:
		if err := g.opts.Consent(cap, true); err != nil {
			g.log.Warn(hlog.Session, "recording the grant for %s: %v", cap, err)
			return false
		}
		g.log.Info(hlog.Session, "granted: %s — asked for and allowed in the moment", cap)
		allowed = true
		return true
	case consentNever:
		if err := g.opts.Consent(cap, false); err != nil {
			g.log.Warn(hlog.Session, "recording the refusal for %s: %v", cap, err)
		}
		g.log.Info(hlog.Session, "refused for good: %s — it will not ask again", cap)
		return false
	default:
		// "Not now", dismissed, or timed out: all the same answer, and all
		// of them mean ask again another time rather than never.
		g.log.Info(hlog.Session, "not allowed this time: %s", cap)
		return false
	}
}

// canAsk reports whether an in-the-moment question about cap is possible
// and appropriate.
func (g *Ghost) canAsk(cap string) bool {
	if !mayAsk[cap] || g.opts.Consent == nil {
		return false
	}
	// Dev-mode runs grant everything already, so a question would be
	// theatre; and a capability the manifest never asked for is not the
	// ghost's to raise.
	if g.opts.Granted == nil || !g.declares(cap) {
		return false
	}
	if g.opts.Refused != nil && g.opts.Refused(cap) {
		g.log.Debug(hlog.Session, "%s was refused for good; not asking", cap)
		return false
	}
	if muted, why := g.quiet.Active(); muted {
		g.log.Debug(hlog.Session, "not asking about %s (%s)", cap, why)
		return false
	}
	// Nowhere to show the question is nowhere to answer it.
	return g.surface != nil
}

// declares reports whether the manifest asked for this capability.
func (g *Ghost) declares(cap string) bool {
	if g.manifest == nil {
		return false
	}
	for _, c := range g.manifest.Ghost.Capabilities {
		if strings.EqualFold(c, cap) {
			return true
		}
	}
	return false
}
