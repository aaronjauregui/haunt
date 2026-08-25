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
//     of it. A permission popup at login, about nothing in particular, is
//     exactly the pattern this is meant to replace.
//
// A manifest trigger that polls a tool is the case that tests that last
// rule, since it is a background loop by construction and has to be
// consented to somehow. It asks, and what makes the question honest is that
// it is asked only while the person is at the desk and says out loud what it
// is about — "would like to check calendar.upcoming on a schedule, without
// being asked each time" is a question about a standing arrangement, not a
// popup about nothing. A poll that fires while they are away asks nothing
// and does nothing; the next one asks. See poll.go.
//
// Window awareness was the awkward case for that last rule, since the thing
// that needs it *is* a background loop, and it sat behind `haunt perms`
// alone for exactly that reason. It is asked for now, but only through
// `watch` (see awareness.go), which the driver honours only when the person
// has just acted — the menu entry offering to watch is them raising the
// question, which is a better moment than either the install sheet or a
// poller could produce.
//
// Acting is the other shape this takes, and the only one that asks twice.
// `tools` is an ordinary capability question, asked exactly as above; each
// individual tool is then asked about the first time it is really about to
// run, keyed `tool:notes.append` in the same grants file (see act.go):
//
//	Wisp would like to use notes.append — Append a line to today's note.
//	Allow?
//	1. Allow
//	2. Just this once
//	3. Not now
//	4. Never ask again
//
// The extra option is there because a tool call is a discrete act with a
// consequence in the world, which is exactly the thing somebody may want to
// allow one of without allowing all of. Standing capabilities have no use
// for it — answering "just this once" to "may it talk to your language
// model" would mean being asked again on the next sentence — so it is
// offered for tools and nothing else.

// consentGap is how long to leave someone alone after a "not now" before
// the same question may be asked again.
const consentGap = 10 * time.Minute

// The answers, in the order they are offered.
const (
	consentAllow  = "Allow"
	consentNotNow = "Not now"
	consentNever  = "Never ask again"
	// consentOnce is offered for tools and nothing else. Every other
	// capability is a standing condition — a ghost either may talk to your
	// language model or may not — and answering "just this once" to one of
	// those would mean being asked again on the next sentence. A tool call
	// is a discrete act with a consequence in the world, which is exactly
	// the thing a person may want to allow one of without allowing all of.
	consentOnce = "Just this once"
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

// verdict is what a person said, in the three shapes that matter to what
// is remembered rather than to what happens next.
type verdict int

const (
	// verdictNo covers "not now", a dismissal and a refusal for good: all of
	// them mean leave the question alone for a while.
	verdictNo verdict = iota
	// verdictYes was written to the grants file, so nothing needs holding
	// off — the grant itself answers from here on.
	verdictYes
	// verdictOnce is "just this once": the thing goes ahead and nothing is
	// remembered, so the next time is a fresh question rather than either a
	// silent yes or a silent no. Neither the grace period nor the gap
	// applies, which is the whole difference between this and the other two.
	verdictOnce
)

// end releases the question and records what was said.
func (a *askedFor) end(cap string, said verdict) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.init()
	a.busy[cap] = false
	switch said {
	case verdictYes:
		a.fresh[cap] = time.Now()
		delete(a.last, cap)
	case verdictNo:
		a.last[cap] = time.Now()
	}
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
	// `window` is askable only because the `watch` intent gives it a moment
	// to be asked in, and only when the user just provoked it: see
	// execWatch, which is the sole caller.
	"window": true,
	// `tools` is askable, and is the outer of the two questions acting
	// puts: this one is asked once, and the question about the individual
	// tool follows it every first time (see act.go).
	"tools": true,
	// `tell` became askable once there was a second ghost to talk to. It
	// asks off the intent loop rather than on it (see IntentTell), because
	// blocking that loop while somebody reads would stop the ghost doing
	// anything else, including saying the thing it is asking about.
	"tell": true,
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
	what := KnownCapabilities[cap]
	return g.askPermission(cap,
		g.manifest.Ghost.Name+" would like to "+what+". Allow?",
		func() bool { return g.capabilityActive(cap) },
		consentAllow, consentNotNow, consentNever)
}

// askPermission puts one permission question and records the answer against
// key, which is what goes into the grants file. active re-checks the
// standing answer, since a person may have settled the same question
// through `haunt perms` between the caller's check and this one. Reports
// whether the thing may go ahead now — which "just this once" allows
// without granting anything.
func (g *Ghost) askPermission(key, question string, active func() bool, options ...string) bool {
	if !g.asked.begin(key) {
		g.log.Debug(hlog.Session, "not asking about %s again just yet", key)
		return false
	}
	said := verdictNo
	defer func() { g.asked.end(key, said) }()

	if active() {
		return true
	}
	g.log.Info(hlog.Session, "asking whether %s may %s", g.manifest.Ghost.Name, key)

	switch g.ask(question, options...) {
	case consentAllow:
		if err := g.opts.Consent(key, true); err != nil {
			g.log.Warn(hlog.Session, "recording the grant for %s: %v", key, err)
			return false
		}
		g.log.Info(hlog.Session, "granted: %s — asked for and allowed in the moment", key)
		said = verdictYes
		return true
	case consentOnce:
		// Nothing is written down, so the next call asks again — which is
		// the whole of what this answer means. It does not count as an
		// allowance for the purposes of the ask-again gap either: the
		// person said yes, so a second request a minute later deserves a
		// question rather than a silent refusal.
		g.log.Info(hlog.Session, "allowed once: %s — nothing recorded", key)
		said = verdictOnce
		return true
	case consentNever:
		if err := g.opts.Consent(key, false); err != nil {
			g.log.Warn(hlog.Session, "recording the refusal for %s: %v", key, err)
		}
		g.log.Info(hlog.Session, "refused for good: %s — it will not ask again", key)
		return false
	default:
		// "Not now", dismissed, or timed out: all the same answer, and all
		// of them mean ask again another time rather than never.
		g.log.Info(hlog.Session, "not allowed this time: %s", key)
		return false
	}
}

// canAsk reports whether an in-the-moment question about cap is possible
// and appropriate.
func (g *Ghost) canAsk(cap string) bool {
	if !mayAsk[cap] {
		return false
	}
	return g.canAskAbout(cap) && g.declares(cap)
}

// canAskAbout is the half of canAsk that is not about capabilities at all:
// is there a screen, is the person being left alone, have they already said
// never. A tool grant shares every one of those and none of the rest — it
// is not in KnownCapabilities, and its equivalent of "did the manifest
// declare this" is whether the tool is in the catalogue at all, which
// runTool has already settled before it asks (see act.go).
func (g *Ghost) canAskAbout(key string) bool {
	if g.opts.Consent == nil {
		return false
	}
	// Dev-mode runs grant everything already, so a question would be
	// theatre.
	if g.opts.Granted == nil {
		return false
	}
	if g.opts.Refused != nil && g.opts.Refused(key) {
		g.log.Debug(hlog.Session, "%s was refused for good; not asking", key)
		return false
	}
	if muted, why := g.quiet.Active(); muted {
		g.log.Debug(hlog.Session, "not asking about %s (%s)", key, why)
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
