package ghost

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/quiet"
	"github.com/aaronjauregui/haunt/internal/wlclient"
)

// consentGhost is a ghost whose manifest asks for the world and whose user
// has granted none of it — the state an install with "[y/N]" answered N
// leaves behind, and the one this feature exists for.
func consentGhost(t *testing.T) (*Ghost, *recorder) {
	t.Helper()
	g, _ := choosingGhost(t)
	g.manifest = &Manifest{Ghost: GhostSection{
		Name:         "wisp",
		Capabilities: []string{"message", "llm", "imagine", "screen"},
	}}
	r := &recorder{}
	g.opts.Granted = func(cap string) bool { return r.granted(cap) }
	g.opts.Refused = func(cap string) bool { return r.refusedCap(cap) }
	g.opts.Consent = r.record
	// Somewhere to draw a question. There is no compositor here, so this is
	// only the driver's own answer to "is there a screen at all" — the
	// panel itself stays bookkeeping, which is what the state machine acts
	// on.
	g.surface = &wlclient.SpriteSurface{}
	return g, r
}

// recorder stands in for the grants file.
type recorder struct {
	mu      sync.Mutex
	allowed map[string]bool
	refused map[string]bool
	err     error
	calls   int
}

func (r *recorder) record(cap string, allow bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return r.err
	}
	if r.allowed == nil {
		r.allowed, r.refused = map[string]bool{}, map[string]bool{}
	}
	if allow {
		r.allowed[cap] = true
		delete(r.refused, cap)
	} else {
		r.refused[cap] = true
		delete(r.allowed, cap)
	}
	return nil
}

func (r *recorder) granted(cap string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allowed[cap]
}

func (r *recorder) refusedCap(cap string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refused[cap]
}

// answer picks an option as soon as a question appears, as a person would.
func answer(t *testing.T, g *Ghost, option string) {
	t.Helper()
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for !g.choiceOpen() {
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		g.choiceMu.Lock()
		at := -1
		for i, o := range g.choice.options {
			if o == option {
				at = i
			}
		}
		g.choiceMu.Unlock()
		if at >= 0 {
			g.answerChoice(at)
		} else {
			g.dismissChoices()
		}
	}()
}

// The whole point: a capability the ghost asked for in its manifest but was
// not granted is not simply refused. The person is asked, in the moment,
// about the thing that is happening — and "Allow" both writes the grant and
// lets the request through, so the answer arrives as if nothing happened.
func TestAllowGrantsAndLetsTheRequestThrough(t *testing.T) {
	g, rec := consentGhost(t)
	answer(t, g, consentAllow)

	if !g.ensureCapability("llm") {
		t.Fatal("allowing did not let the request through")
	}
	if !rec.granted("llm") {
		t.Error("the grant was not written down")
	}
	// And it is not asked again, since the grant now answers for itself.
	rec.mu.Lock()
	calls := rec.calls
	rec.mu.Unlock()
	if !g.ensureCapability("llm") || func() int { rec.mu.Lock(); defer rec.mu.Unlock(); return rec.calls }() != calls {
		t.Error("it asked again about something already granted")
	}
}

// "Not now" refuses this once and leaves the question open for later.
func TestNotNowRefusesWithoutRecording(t *testing.T) {
	g, rec := consentGhost(t)
	answer(t, g, consentNotNow)

	if g.ensureCapability("imagine") {
		t.Error("'not now' let the request through")
	}
	if rec.calls != 0 {
		t.Errorf("'not now' wrote %d decisions; it should record nothing", rec.calls)
	}
	if rec.refusedCap("imagine") {
		t.Error("'not now' was recorded as a refusal")
	}
}

// "Never ask again" has to be written down. Without somewhere to record it,
// no means "ask me again in a minute", which is how a companion becomes a
// nuisance.
func TestNeverIsRememberedAndEndsTheAsking(t *testing.T) {
	g, rec := consentGhost(t)
	answer(t, g, consentNever)

	if g.ensureCapability("screen") {
		t.Error("'never' let the request through")
	}
	if !rec.refusedCap("screen") {
		t.Fatal("the refusal was not written down")
	}

	// Asked again later: no question, no answer, no nagging.
	g.asked.mu.Lock()
	g.asked.last = map[string]time.Time{}
	g.asked.mu.Unlock()
	if g.ensureCapability("screen") {
		t.Error("a refused capability came back")
	}
	if g.choiceOpen() {
		t.Error("it asked again after being told never to")
	}
}

// Dismissing the question is "not now": turning away is not consent, and it
// is not a permanent refusal either.
func TestDismissingIsNotConsent(t *testing.T) {
	g, rec := consentGhost(t)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for !g.choiceOpen() {
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		g.dismissChoices()
	}()

	if g.ensureCapability("llm") {
		t.Error("a dismissed question granted the capability")
	}
	if rec.calls != 0 {
		t.Error("a dismissed question wrote a decision")
	}
}

// A ghost may not ask for powers it never shipped asking for. The package
// is the outer bound; the question is only ever about a subset of it.
func TestUndeclaredCapabilitiesAreNeverAskedAbout(t *testing.T) {
	g, rec := consentGhost(t)
	answer(t, g, consentAllow) // if a question appears, allow it

	if g.ensureCapability("window") { // declared by nobody here
		t.Error("an undeclared capability was granted")
	}
	if rec.calls != 0 {
		t.Error("it asked about a capability the manifest never requested")
	}
}

// Asking twice at once would put two questions on one balloon; asking again
// straight after a "not now" would be nagging.
func TestItDoesNotAskAgainImmediately(t *testing.T) {
	g, _ := consentGhost(t)
	answer(t, g, consentNotNow)
	if g.ensureCapability("llm") {
		t.Fatal("setup: expected a refusal")
	}
	// Second attempt, straight away: no question this time.
	if g.ensureCapability("llm") {
		t.Error("granted on a second attempt")
	}
	if g.choiceOpen() {
		t.Error("it asked again within moments of being told 'not now'")
	}
}

// If the answer cannot be written down, it must not be acted on either: a
// grant the file never received would evaporate at the next restart and
// leave the person believing they had allowed something.
func TestAGrantThatCannotBeSavedIsNotHonoured(t *testing.T) {
	g, rec := consentGhost(t)
	rec.err = errors.New("read-only file system")
	answer(t, g, consentAllow)

	if g.ensureCapability("llm") {
		t.Error("a grant that could not be saved was honoured anyway")
	}
}

// Nothing is asked when the driver has nowhere to record the answer — a
// dev-directory run grants everything declared already, and a question
// would be theatre.
func TestNoQuestionWithoutSomewhereToPutTheAnswer(t *testing.T) {
	g, _ := consentGhost(t)
	g.opts.Consent = nil
	if g.ensureCapability("llm") {
		t.Error("granted with nowhere to record it")
	}
	if g.choiceOpen() {
		t.Error("asked a question whose answer would be lost")
	}
}

// Quiet hours mean quiet: a permission question is still an interruption.
func TestQuietHoursHoldTheQuestion(t *testing.T) {
	g, rec := consentGhost(t)
	g.quiet = allHoursQuiet(t)
	answer(t, g, consentAllow)

	if g.ensureCapability("llm") {
		t.Error("asked and granted during quiet hours")
	}
	if rec.calls != 0 {
		t.Error("a question was answered during quiet hours")
	}
}

// The log says what was asked and what came of it: a permission change
// nobody can see afterwards is not much of a record.
func TestConsentIsLogged(t *testing.T) {
	g, _ := choosingGhost(t)
	rec, lines := hlog.Recorder()
	g.log = rec
	g.manifest = &Manifest{Ghost: GhostSection{Name: "wisp", Capabilities: []string{"llm"}}}
	store := &recorder{}
	g.opts.Granted = func(string) bool { return false }
	g.opts.Consent = store.record
	g.surface = &wlclient.SpriteSurface{}
	answer(t, g, consentAllow)

	g.ensureCapability("llm")
	joined := strings.Join(lines(), "\n")
	for _, want := range []string{"asking whether wisp may llm", "granted: llm"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log is missing %q:\n%s", want, joined)
		}
	}
}

// A choice event from a consent question is the driver's business: the
// brain must not see it, or a ghost learns how its user answers questions
// about whether to trust it.
func TestTheBrainNeverHearsTheAnswer(t *testing.T) {
	g, _ := consentGhost(t)
	b := g.bus
	answer(t, g, consentAllow)
	g.ensureCapability("llm")

	for _, e := range drain(b) {
		if e.Type == proto.EventChoice {
			t.Errorf("the brain was told how consent was answered: %+v", e)
		}
	}
}

// allHoursQuiet is a checker for which it is always quiet hours.
func allHoursQuiet(t *testing.T) *quiet.Checker {
	t.Helper()
	// Two windows, the second running over midnight, so every minute of
	// the day is inside one: "00:00-23:59" left out the last minute.
	c, err := quiet.New(quiet.Config{Hours: []string{"00:00-12:00", "12:00-00:00"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
