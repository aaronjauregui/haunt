package session

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)

// poll feeds the model the same window every three seconds for d, as the
// real poller would, and collects whatever it noticed.
func poll(m *Model, app, title string, from time.Time, d time.Duration) ([]Fact, time.Time) {
	var facts []Fact
	at := from
	for elapsed := time.Duration(0); elapsed <= d; elapsed += 3 * time.Second {
		at = from.Add(elapsed)
		facts = append(facts, m.Observe(app, title, at)...)
	}
	return facts, at
}

func kinds(facts []Fact) []Kind {
	var out []Kind
	for _, f := range facts {
		out = append(out, f.Kind)
	}
	return out
}

// The first of the three shapes the design names: forty minutes on one
// file. Nothing happens, which is precisely the observation.
func TestLongStretchIsNoticedOnce(t *testing.T) {
	m := New(Config{Dwell: 20 * time.Minute})

	facts, at := poll(m, "nvim", "session.go", t0, 19*time.Minute)
	if len(facts) != 0 {
		t.Fatalf("noticed something after 19 minutes: %+v", facts)
	}

	facts, at = poll(m, "nvim", "session.go", at, 2*time.Minute)
	if len(facts) != 1 || facts[0].Kind != Dwell {
		t.Fatalf("facts after 21 minutes = %+v, want one dwell", facts)
	}
	if facts[0].Minutes < 20 {
		t.Errorf("dwell reported %d minutes", facts[0].Minutes)
	}
	if !strings.Contains(facts[0].Summary, "nvim") || !strings.Contains(facts[0].Summary, "session.go") {
		t.Errorf("summary does not say what was being done: %q", facts[0].Summary)
	}

	// And it is not said again for as long as the stretch lasts: a ghost
	// that mentions the same file every poll is a ghost you turn off.
	facts, _ = poll(m, "nvim", "session.go", at, 30*time.Minute)
	if len(facts) != 0 {
		t.Errorf("said it again: %+v", facts)
	}
}

// The second shape: a rapid alt-tab cycle between an editor and a browser,
// which is what debugging looks like from outside.
func TestAlternationBetweenTwoApplications(t *testing.T) {
	m := New(Config{AltWindow: 5 * time.Minute, AltSwitches: 6})

	at := t0
	var facts []Fact
	for i := 0; i < 4; i++ {
		var f []Fact
		f, at = poll(m, "nvim", "session.go", at, 20*time.Second)
		facts = append(facts, f...)
		f, at = poll(m, "firefox", "a stack overflow question", at, 20*time.Second)
		facts = append(facts, f...)
	}
	var alt *Fact
	for i := range facts {
		if facts[i].Kind == Alternation {
			alt = &facts[i]
		}
	}
	if alt == nil {
		t.Fatalf("eight switches in three minutes went unnoticed: %v", kinds(facts))
	}
	if !strings.Contains(alt.App, "nvim") || !strings.Contains(alt.App, "firefox") {
		t.Errorf("alternation names %q", alt.App)
	}
	if alt.Count < 6 {
		t.Errorf("counted %d switches", alt.Count)
	}
}

// Three applications in rotation is a working day, not a pattern. The point
// of the feature is to say something *worth* saying.
func TestThreeApplicationsAreNotAPattern(t *testing.T) {
	m := New(Config{AltWindow: 5 * time.Minute, AltSwitches: 4})
	at := t0
	var facts []Fact
	for i := 0; i < 3; i++ {
		for _, app := range []string{"nvim", "firefox", "slack"} {
			var f []Fact
			f, at = poll(m, app, app+" window", at, 10*time.Second)
			facts = append(facts, f...)
		}
	}
	for _, f := range facts {
		if f.Kind == Alternation {
			t.Errorf("called a three-way rotation an alternation: %+v", f)
		}
	}
}

// The third shape: the same error title coming back across an hour.
func TestRecurringTitle(t *testing.T) {
	m := New(Config{RecurWindow: time.Hour, RecurCount: 3})

	at := t0
	var facts []Fact
	for i := 0; i < 3; i++ {
		var f []Fact
		f, at = poll(m, "firefox", "build failed — CI", at, 30*time.Second)
		facts = append(facts, f...)
		f, at = poll(m, "nvim", "session.go", at, 5*time.Minute)
		facts = append(facts, f...)
	}
	var rec *Fact
	for i := range facts {
		if facts[i].Kind == Recurrence && facts[i].Title == "build failed — CI" {
			rec = &facts[i]
		}
	}
	if rec == nil {
		t.Fatalf("a title seen three times in an hour went unnoticed: %v", kinds(facts))
	}
	if rec.Count < 3 {
		t.Errorf("recurrence = %+v", *rec)
	}
	if !strings.Contains(rec.Summary, "3rd time") {
		t.Errorf("summary reads oddly: %q", rec.Summary)
	}
}

// Old sightings fall out of the window: something seen twice this morning
// and once after lunch is not a pattern.
func TestOldSightingsExpire(t *testing.T) {
	m := New(Config{RecurWindow: 30 * time.Minute, RecurCount: 3})
	at := t0
	for i := 0; i < 2; i++ {
		_, at = poll(m, "firefox", "build failed", at, 10*time.Second)
		_, at = poll(m, "nvim", "code", at, 10*time.Second)
	}
	// Hours later.
	at = at.Add(3 * time.Hour)
	facts, _ := poll(m, "firefox", "build failed", at, 10*time.Second)
	for _, f := range facts {
		if f.Kind == Recurrence {
			t.Errorf("stale sightings made a pattern: %+v", f)
		}
	}
}

// A window the user asked never to be watched must not extend a dwell, and
// must not break it into two either: it is not there at all. The caller
// applies the ignore list and calls Away, which is why that rule can live
// upstream of any of this.
func TestAwayEndsTheStretchWithoutRecordingIt(t *testing.T) {
	m := New(Config{Dwell: 10 * time.Minute})
	_, at := poll(m, "nvim", "session.go", t0, 9*time.Minute)

	m.Away(at) // a password manager took focus
	at = at.Add(time.Minute)

	facts, at := poll(m, "nvim", "session.go", at, 5*time.Minute)
	if len(facts) != 0 {
		t.Errorf("the stretch survived the gap: %+v", facts)
	}
	// And the new stretch is timed from the return, not the original start.
	facts, _ = poll(m, "nvim", "session.go", at, 6*time.Minute)
	if len(facts) != 1 || facts[0].Kind != Dwell {
		t.Fatalf("facts = %+v, want a dwell once the new stretch is long enough", facts)
	}
	if facts[0].Minutes > 12 {
		t.Errorf("dwell of %d minutes counted the time before the gap", facts[0].Minutes)
	}
}

// The same subject is not raised twice in a row: noticing something is
// useful, noticing it repeatedly is nagging.
func TestCooldownPerSubject(t *testing.T) {
	m := New(Config{Dwell: 5 * time.Minute, Cooldown: time.Hour})
	facts, at := poll(m, "nvim", "a.go", t0, 6*time.Minute)
	if len(facts) != 1 {
		t.Fatalf("first dwell = %+v", facts)
	}
	// Away and back, long enough to dwell again, inside the cooldown.
	m.Away(at)
	at = at.Add(time.Minute)
	facts, _ = poll(m, "nvim", "a.go", at, 6*time.Minute)
	if len(facts) != 0 {
		t.Errorf("said the same thing again within the cooldown: %+v", facts)
	}
}

// An empty window is a bare desktop, not a thing to dwell on.
func TestNothingFocusedIsNotADwell(t *testing.T) {
	m := New(Config{Dwell: time.Minute})
	facts, _ := poll(m, "", "", t0, 10*time.Minute)
	if len(facts) != 0 {
		t.Errorf("noticed a bare desktop: %+v", facts)
	}
}

// Coming *back* to something means having left it. Flipping between two
// windows is alternation, and counting it as a recurrence of both would
// report the same behaviour twice under different names.
func TestFlippingIsNotARecurrence(t *testing.T) {
	m := New(Config{RecurWindow: time.Hour, RecurCount: 3, RecurGap: 2 * time.Minute,
		AltSwitches: 99}) // alternation out of the way
	at := t0
	var facts []Fact
	for i := 0; i < 5; i++ {
		var f []Fact
		f, at = poll(m, "nvim", "a.go", at, 15*time.Second)
		facts = append(facts, f...)
		f, at = poll(m, "firefox", "docs", at, 15*time.Second)
		facts = append(facts, f...)
	}
	for _, f := range facts {
		if f.Kind == Recurrence {
			t.Errorf("rapid flipping was reported as a recurrence: %+v", f)
		}
	}
}

// The summary is what a brain hands to a model, so it has to read like
// something a person would say: "0 minutes in foot (foot)" reads as a bug
// even when the arithmetic is right.
func TestSummariesReadLikeSentences(t *testing.T) {
	cases := []struct {
		dwell time.Duration
		app   string
		title string
		want  string
	}{
		{8 * time.Second, "foot", "foot", "seconds in foot"},
		{40 * time.Minute, "nvim", "session.go", "40 minutes in nvim (session.go)"},
		{95 * time.Minute, "nvim", "", "an hour and 35 minutes in nvim"},
	}
	for _, tc := range cases {
		m := New(Config{Dwell: tc.dwell})
		facts, _ := poll(m, tc.app, tc.title, t0, tc.dwell+10*time.Second)
		if len(facts) == 0 {
			t.Fatalf("%v in %s: nothing noticed", tc.dwell, tc.app)
		}
		if !strings.Contains(facts[0].Summary, tc.want) {
			t.Errorf("summary %q, want it to contain %q", facts[0].Summary, tc.want)
		}
		if strings.Contains(facts[0].Summary, "(foot)") {
			t.Errorf("a window titled after its own application reads twice: %q", facts[0].Summary)
		}
	}
}
