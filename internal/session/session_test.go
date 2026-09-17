package session

import (
	"encoding/json"
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

// The fourth shape: coming back to the machine after being away from it.
func TestReturningAfterALongAbsence(t *testing.T) {
	m := New(Config{AwayLong: time.Hour})

	if facts, _ := poll(m, "nvim", "session.go", t0, 0); len(facts) != 0 {
		t.Fatalf("the very first window produced %+v; there is nothing to compare it to", facts)
	}
	// Lunch. The poller keeps running against a locked screen, which is
	// Away, so nothing extends the gap.
	m.Away(t0.Add(10 * time.Minute))

	facts := m.Observe("nvim", "session.go", t0.Add(2*time.Hour))
	if len(facts) != 1 || facts[0].Kind != Returning {
		t.Fatalf("facts on returning = %+v, want one returning", kinds(facts))
	}
	if !strings.Contains(facts[0].Summary, "2 hours") {
		t.Errorf("summary does not say how long: %q", facts[0].Summary)
	}
}

// Ten minutes is making tea, not an absence worth remarking on.
func TestShortGapIsNotAReturn(t *testing.T) {
	m := New(Config{AwayLong: time.Hour})
	m.Observe("nvim", "session.go", t0)

	facts := m.Observe("nvim", "session.go", t0.Add(10*time.Minute))
	if len(facts) != 0 {
		t.Errorf("noticed %+v after ten minutes away", kinds(facts))
	}
}

// The fifth shape: the first window of a new day. A morning and a long
// absence are the same event described twice, and the morning is the better
// description, so only one of them fires.
func TestFirstWindowOfTheDay(t *testing.T) {
	m := New(Config{})
	m.Observe("nvim", "session.go", t0)

	next := t0.Add(24 * time.Hour)
	facts := m.Observe("firefox", "inbox", next)
	if len(facts) != 1 || facts[0].Kind != Morning {
		t.Fatalf("facts on a new day = %+v, want one morning", kinds(facts))
	}
	if !strings.Contains(facts[0].Summary, "firefox") {
		t.Errorf("summary does not say what was opened: %q", facts[0].Summary)
	}
	// And once: the rest of the day is not also the morning.
	if facts := m.Observe("firefox", "inbox2", next.Add(time.Hour)); len(facts) != 0 {
		t.Errorf("said it again: %+v", kinds(facts))
	}
}

// Working past midnight is not a new morning at one minute past twelve.
func TestMidnightIsNotAMorning(t *testing.T) {
	evening := time.Date(2026, 8, 19, 23, 30, 0, 0, time.UTC)
	m := New(Config{})
	m.Observe("nvim", "session.go", evening)

	facts := m.Observe("nvim", "session.go", evening.Add(40*time.Minute))
	for _, k := range kinds(facts) {
		if k == Morning {
			t.Errorf("00:10 reported as a new morning: %+v", facts)
		}
	}
}

// The sixth shape: the clock, not the window. Once per night, where a night
// runs past midnight.
func TestLateIsNoticedOncePerNight(t *testing.T) {
	afternoon := time.Date(2026, 8, 19, 15, 0, 0, 0, time.UTC)
	m := New(Config{LateHour: 23})
	m.Observe("nvim", "session.go", afternoon)

	for _, k := range kinds(m.Observe("nvim", "session.go", afternoon.Add(5*time.Hour))) {
		if k == Late {
			t.Fatal("eight in the evening was reported as late")
		}
	}

	night := time.Date(2026, 8, 19, 23, 10, 0, 0, time.UTC)
	var late bool
	for _, f := range m.Observe("nvim", "session.go", night) {
		if f.Kind == Late {
			late = true
			if !strings.Contains(f.Summary, "11:10pm") {
				t.Errorf("summary does not say the hour: %q", f.Summary)
			}
		}
	}
	if !late {
		t.Fatal("23:10 went unremarked")
	}

	// One in the morning is the same night, and being told twice about it
	// is the nagging the whole file exists to avoid.
	small := time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC)
	for _, k := range kinds(m.Observe("nvim", "session.go", small)) {
		if k == Late {
			t.Error("said it again in the small hours of the same night")
		}
	}

	// The next night is a different night.
	tomorrow := time.Date(2026, 8, 20, 23, 30, 0, 0, time.UTC)
	m.Observe("nvim", "session.go", time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC))
	late = false
	for _, k := range kinds(m.Observe("nvim", "session.go", tomorrow)) {
		late = late || k == Late
	}
	if !late {
		t.Error("the following night went unremarked")
	}
}

// A negative LateHour is for people whose day starts at eleven at night.
func TestLateCanBeSwitchedOff(t *testing.T) {
	m := New(Config{LateHour: -1})
	m.Observe("nvim", "session.go", t0)
	night := time.Date(2026, 8, 19, 23, 30, 0, 0, time.UTC)
	for _, k := range kinds(m.Observe("nvim", "session.go", night)) {
		if k == Late {
			t.Error("noticed the hour with the shape switched off")
		}
	}
}

// The point of persisting anything: a recurrence spanning a restart used to
// be invisible, and a companion restarted at login every day is exactly the
// case that matters.
func TestRecurrenceSurvivesARestart(t *testing.T) {
	cfg := Config{RecurCount: 3, RecurGap: time.Minute, RecurWindow: time.Hour}
	m := New(cfg)

	at := t0
	for i := 0; i < 2; i++ {
		m.Observe("firefox", "build failed — CI", at)
		m.Observe("nvim", "session.go", at.Add(2*time.Minute))
		at = at.Add(5 * time.Minute)
	}

	// The ghost stops and comes back.
	restarted := Restore(cfg, m.Snapshot(), at)

	facts := restarted.Observe("firefox", "build failed — CI", at.Add(time.Minute))
	var recurred bool
	for _, f := range facts {
		if f.Kind == Recurrence {
			recurred = true
			if f.Count != 3 {
				t.Errorf("count = %d, want 3: the sightings before the restart still happened", f.Count)
			}
			// The sentence names the window the ghost is looking at now,
			// which is why the history only has to be countable.
			if !strings.Contains(f.Summary, "build failed") {
				t.Errorf("summary lost the title: %q", f.Summary)
			}
		}
	}
	if !recurred {
		t.Errorf("the restart forgot the recurrence: %+v", kinds(facts))
	}
}

// The cooldown survives too, or restarting the ghost is a way to make it
// say everything again.
func TestCooldownSurvivesARestart(t *testing.T) {
	cfg := Config{Dwell: 10 * time.Minute, Cooldown: time.Hour}
	m := New(cfg)
	facts, at := poll(m, "nvim", "session.go", t0, 11*time.Minute)
	if len(facts) != 1 {
		t.Fatalf("setup produced %+v, want one dwell", kinds(facts))
	}

	restarted := Restore(cfg, m.Snapshot(), at)
	facts, _ = poll(restarted, "nvim", "session.go", at, 11*time.Minute)
	if len(facts) != 0 {
		t.Errorf("the restart reset the cooldown: %+v", kinds(facts))
	}
}

// A machine that was off for a week must not come back believing it is
// halfway through an hour of recurrences.
func TestRestoreDropsWhatIsTooOldToMatter(t *testing.T) {
	cfg := Config{RecurCount: 3, RecurGap: time.Minute, RecurWindow: time.Hour, Cooldown: time.Hour}
	m := New(cfg)
	at := t0
	for i := 0; i < 2; i++ {
		m.Observe("firefox", "build failed — CI", at)
		m.Observe("nvim", "session.go", at.Add(2*time.Minute))
		at = at.Add(5 * time.Minute)
	}
	st := m.Snapshot()

	restarted := Restore(cfg, st, at.Add(7*24*time.Hour))
	if len(restarted.seen) != 0 || len(restarted.said) != 0 {
		t.Errorf("a week-old snapshot was trusted: %d sightings, %d cooldowns",
			len(restarted.seen), len(restarted.said))
	}

	// A snapshot from the future is a clock that moved, not a prediction.
	ahead := Restore(cfg, st, t0.Add(-24*time.Hour))
	if !ahead.lastSeen.IsZero() {
		t.Error("a snapshot from the future was taken at face value")
	}
}

// The privacy claim, asserted rather than promised: what goes to disk holds
// no window titles. Every use of these maps is an equality test, so nothing
// is lost by keeping identities instead.
func TestSnapshotHoldsNoTitles(t *testing.T) {
	m := New(Config{})
	secret := "Re: your test results — Dr Halloway"
	m.Observe("thunderbird", secret, t0)
	m.Observe("nvim", "session.go", t0.Add(5*time.Minute))
	m.Observe("thunderbird", secret, t0.Add(10*time.Minute))

	blob, err := json.Marshal(m.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{secret, "Halloway", "thunderbird", "session.go"} {
		if strings.Contains(string(blob), leak) {
			t.Errorf("the snapshot contains %q:\n%s", leak, blob)
		}
	}
}

// Stretch is the time in front of the current window, for the idle
// envelope: timed like a dwell, and nothing while away.
func TestStretch(t *testing.T) {
	m := New(Config{})
	if d := m.Stretch(t0); d != 0 {
		t.Errorf("stretch before any window = %s", d)
	}
	_, at := poll(m, "nvim", "session.go", t0, 42*time.Minute)
	if d := m.Stretch(at); d != 42*time.Minute {
		t.Errorf("stretch = %s, want 42m", d)
	}
	m.Observe("nvim", "main.go", at)
	if d := m.Stretch(at.Add(time.Minute)); d != time.Minute {
		t.Errorf("stretch after a title change = %s, want 1m", d)
	}
	m.Away(at.Add(2 * time.Minute))
	if d := m.Stretch(at.Add(3 * time.Minute)); d != 0 {
		t.Errorf("stretch while away = %s", d)
	}
}
