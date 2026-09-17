// Package session turns a stream of focused windows into the shape of
// someone's working day.
//
// The raw stream is not interesting on its own. A ghost that is told
// "firefox: build failed — CI" every three seconds knows a great deal and
// understands nothing; the signal is never in one observation, it is in the
// shape over time. Forty minutes on one file. A rapid alt-tab cycle between
// an editor and a browser, which is what debugging looks like from outside.
// The same error title coming back across an hour.
//
// So this sits between the poller and the bus and answers a different
// question: not "what is focused" but "what has been going on". It is
// counters and timestamps — no model, no network, nothing per observation
// worth measuring — and the answers are published as their own events
// alongside the raw ones. What a brain does with them is its business; the
// point is that "still on that bug?" becomes a thing it can know rather
// than guess.
//
// Two properties this deliberately has. **No window title is ever written
// down**, and **the ignore list applies before any of this**, upstream in
// the caller: an application nobody wants watched must not appear in a
// duration statistic either, even unnamed.
//
// The first of those used to be "nothing is written down at all", which was
// simpler and cost the model the facts most worth having. A recurrence is
// about an hour of history and a morning is about yesterday, so a ghost
// that forgets everything when it stops can notice neither — and a
// companion restarted at login every day is exactly the case that matters.
// What survives is Snapshot/Restore, and it is careful about what it holds:
// titles are stored as *identities* (a truncated hash) rather than as text,
// because every one of these counters only ever compares titles for
// equality. The sentence a fact is written into names the window the ghost
// is looking at right now; the history behind it only has to be countable.
// So "back at session.go for the fourth time" survives a reboot, and what
// is on disk says nothing about what you were reading.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Kind names what was noticed.
type Kind string

const (
	// Dwell is a long unbroken stretch in one window.
	Dwell Kind = "dwell"
	// Alternation is flipping between the same two applications, which is
	// what debugging, translating, or copying between two things looks
	// like from outside.
	Alternation Kind = "alternation"
	// Recurrence is the same window title coming back after a gap: the
	// error you keep returning to, the page you keep rereading.
	Recurrence Kind = "recurrence"
	// Returning is coming back to the machine after being away from it for
	// a long while — lunch, a meeting, an afternoon out.
	Returning Kind = "returning"
	// Morning is the first window of a new day: what you opened first,
	// which is a different question from what you have been doing.
	Morning Kind = "morning"
	// Late is the time-of-day shape: still at it when the day should be
	// over.
	Late Kind = "late"
)

// Fact is something noticed about the day, ready to be published.
type Fact struct {
	Kind Kind
	// App and Title are the window it is about. Alternation has two
	// applications, so App holds both, joined.
	App   string
	Title string
	// Minutes is how long, for Dwell; Count is how many, for Alternation
	// and Recurrence.
	Minutes int
	Count   int
	// Summary is the fact in a sentence — the form a brain can hand
	// straight to a model without composing anything.
	Summary string
}

// Config is what counts as worth noticing. The defaults are deliberately
// unexciting: a companion that remarks on every context switch is a
// companion nobody keeps.
type Config struct {
	// Dwell is how long in one window before that is worth mentioning.
	Dwell time.Duration
	// AltWindow and AltSwitches: this many switches between the same two
	// applications inside this long is alternation.
	AltWindow   time.Duration
	AltSwitches int
	// RecurWindow and RecurCount: a title seen this many times inside this
	// long is a recurrence. RecurGap is how long you have to have been away
	// from it for a return to count as one — coming *back* to something
	// means having left, and without this every alt-tab would read as a
	// recurrence of both windows.
	RecurWindow time.Duration
	RecurCount  int
	RecurGap    time.Duration
	// Cooldown is the least time between two facts of the same kind about
	// the same subject. Noticing something twice in a minute is nagging.
	Cooldown time.Duration
	// AwayLong is how long a gap in observation has to be before coming
	// back counts as a return. Below it you went to make tea.
	AwayLong time.Duration
	// LateHour is the hour after which still working is itself worth
	// noticing, in local time, and runs until the small hours of the next
	// morning. Zero takes the default; negative disables the shape, which
	// is the setting for people whose day starts at eleven at night.
	// Anything below EarliestLateHour would make the whole day late.
	LateHour int
}

// Defaults are tuned for a working day rather than a demo: they are the
// difference between a ghost that notices things and one that comments on
// everything.
func Defaults() Config {
	return Config{
		Dwell:       25 * time.Minute,
		AltWindow:   5 * time.Minute,
		AltSwitches: 6,
		RecurWindow: time.Hour,
		RecurCount:  3,
		RecurGap:    2 * time.Minute,
		Cooldown:    30 * time.Minute,
		AwayLong:    90 * time.Minute,
		LateHour:    23,
	}
}

// smallHours is when "late" stops being tonight and starts being tomorrow.
// Working at two in the morning is the same night as working at midnight,
// and a person who starts at six is not working late.
const smallHours = 5

// EarliestLateHour is the earliest LateHour that means anything. Late runs
// from LateHour until smallHours the next morning, so setting it any lower
// would make every waking moment late — which is not a shape, it is a
// ticker. Callers that take the hour from a person should say so.
const EarliestLateHour = smallHours + 1

// Model accumulates observations. Not safe for concurrent use: one poller
// feeds it.
type Model struct {
	cfg Config

	// The stretch in progress.
	app, title string
	since      time.Time
	dwellSaid  bool

	// switches are recent application changes, for spotting alternation.
	switches []switchAt
	// seen counts recent sightings of each title, for spotting recurrence.
	// Keyed by identity rather than by the title itself, so a snapshot of
	// it can be written to disk without being a reading list.
	seen map[string][]time.Time
	// said is when each subject last produced a fact, for the cooldown.
	// Keyed the same way, since a subject contains a title.
	said map[string]time.Time

	// lastSeen is when a real window was last observed — not when the
	// model was last called, which is every poll whether anyone is there
	// or not. The gap between two of these is what "away" means, and the
	// date it falls on is what "a new day" means.
	lastSeen time.Time
	// lateNight and morningDay are the nights and days already remarked
	// on, so those two shapes happen once each rather than on every poll.
	lateNight, morningDay string
}

// identity is how a window title is remembered: enough to count sightings
// of the same thing, not enough to be a record of what it was. Every use of
// these maps is an equality test, so nothing is lost by it. Truncated
// because 48 bits is far past collision for the few hundred titles a
// working day passes through, and the whole point is to keep the snapshot
// small and dull.
func identity(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

type switchAt struct {
	app string
	at  time.Time
}

// New builds a model. Zero fields in cfg take their default.
func New(cfg Config) *Model {
	d := Defaults()
	if cfg.Dwell <= 0 {
		cfg.Dwell = d.Dwell
	}
	if cfg.AltWindow <= 0 {
		cfg.AltWindow = d.AltWindow
	}
	if cfg.AltSwitches <= 0 {
		cfg.AltSwitches = d.AltSwitches
	}
	if cfg.RecurWindow <= 0 {
		cfg.RecurWindow = d.RecurWindow
	}
	if cfg.RecurCount <= 0 {
		cfg.RecurCount = d.RecurCount
	}
	if cfg.RecurGap <= 0 {
		cfg.RecurGap = d.RecurGap
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = d.Cooldown
	}
	if cfg.AwayLong <= 0 {
		cfg.AwayLong = d.AwayLong
	}
	switch {
	case cfg.LateHour == 0:
		cfg.LateHour = d.LateHour
	case cfg.LateHour > 0 && cfg.LateHour < EarliestLateHour:
		// Rather than let a nonsense hour turn the shape into a ticker.
		cfg.LateHour = EarliestLateHour
	}
	return &Model{cfg: cfg, seen: map[string][]time.Time{}, said: map[string]time.Time{}}
}

// Observe records what is focused now and returns anything worth saying
// about it. Call it on every poll, changed or not: a dwell is noticed by
// nothing happening.
func (m *Model) Observe(app, title string, at time.Time) []Fact {
	if app == "" && title == "" {
		return m.Away(at)
	}
	// The shapes that are about arriving rather than about a window: they
	// are read off the gap since the last real observation, so they come
	// first, before that gap is closed.
	facts := m.arrival(app, title, at)

	if app != m.app || title != m.title {
		if app != m.app {
			m.switches = append(m.switches, switchAt{app: app, at: at})
			m.trimSwitches(at)
		}
		m.app, m.title, m.since, m.dwellSaid = app, title, at, false
		if title != "" {
			// Only a real return counts: flipping back and forth between
			// two windows is alternation, which is a different fact about
			// a different thing.
			id := identity(title)
			prev := m.seen[id]
			if len(prev) == 0 || at.Sub(prev[len(prev)-1]) >= m.cfg.RecurGap {
				m.seen[id] = append(prev, at)
			}
			m.trimSeen(id, at)
			if f, ok := m.recurrence(app, title, at); ok {
				facts = append(facts, f)
			}
		}
		if f, ok := m.alternation(at); ok {
			facts = append(facts, f)
		}
		return facts
	}

	// Nothing changed, which is itself the observation.
	if !m.dwellSaid && at.Sub(m.since) >= m.cfg.Dwell {
		if f, ok := m.dwell(at); ok {
			facts = append(facts, f)
		}
	}
	return facts
}

// Stretch is how long the window in front now has been in front, as of
// at: zero when nothing is (the person is away, or the window is one the
// ghost ignores). The same stretch a dwell is measured on, so a title
// change starts it again.
func (m *Model) Stretch(at time.Time) time.Duration {
	if m.app == "" && m.title == "" || m.since.IsZero() || at.Before(m.since) {
		return 0
	}
	return at.Sub(m.since)
}

// Away ends the current stretch without recording anything: the screen was
// locked, the desktop was bare, or the focused application is one the user
// asked never to be watched. An ignored window must not extend a dwell, and
// must not break it into two either — it simply is not there.
func (m *Model) Away(at time.Time) []Fact {
	m.app, m.title, m.dwellSaid = "", "", false
	m.since = at
	return nil
}

// dwell reports a long stretch in one window.
func (m *Model) dwell(at time.Time) (Fact, bool) {
	subject := "dwell:" + m.app + "\x00" + m.title
	if !m.mayReport(subject, at) {
		return Fact{}, false
	}
	m.dwellSaid = true
	spent := at.Sub(m.since)
	f := Fact{Kind: Dwell, App: m.app, Title: m.title, Minutes: int(spent.Minutes())}
	f.Summary = fmt.Sprintf("%s in %s", spell(spent), describe(m.app, m.title))
	return f, true
}

// alternation reports flipping between the same two applications.
func (m *Model) alternation(at time.Time) (Fact, bool) {
	if len(m.switches) < m.cfg.AltSwitches {
		return Fact{}, false
	}
	apps := map[string]int{}
	for _, s := range m.switches {
		apps[s.app]++
	}
	// Exactly two: three applications in rotation is a working day, not a
	// pattern worth remarking on.
	if len(apps) != 2 {
		return Fact{}, false
	}
	names := make([]string, 0, 2)
	for a := range apps {
		names = append(names, a)
	}
	sort.Strings(names)
	pair := strings.Join(names, " and ")
	if !m.mayReport("alt:"+pair, at) {
		return Fact{}, false
	}
	count := len(m.switches)
	// Spent: the pattern has been reported, so start counting afresh rather
	// than reporting it again on the next switch.
	m.switches = nil
	return Fact{
		Kind:  Alternation,
		App:   pair,
		Count: count,
		Summary: fmt.Sprintf("switching between %s — %d times in the last %s",
			pair, count, plainDuration(m.cfg.AltWindow)),
	}, true
}

// recurrence reports a title that keeps coming back.
func (m *Model) recurrence(app, title string, at time.Time) (Fact, bool) {
	times := m.seen[identity(title)]
	if len(times) < m.cfg.RecurCount {
		return Fact{}, false
	}
	if !m.mayReport("recur:"+title, at) {
		return Fact{}, false
	}
	return Fact{
		Kind:  Recurrence,
		App:   app,
		Title: title,
		Count: len(times),
		Summary: fmt.Sprintf("back at %s for the %s time in the last %s",
			describe(app, title), ordinal(len(times)), plainDuration(m.cfg.RecurWindow)),
	}, true
}

// mayReport enforces the cooldown for one subject. The subject is hashed
// for the same reason the recurrence table is: it has a window title in it,
// and this map is written to disk.
func (m *Model) mayReport(subject string, at time.Time) bool {
	id := identity(subject)
	if last, ok := m.said[id]; ok && at.Sub(last) < m.cfg.Cooldown {
		return false
	}
	m.said[id] = at
	return true
}

func (m *Model) trimSwitches(at time.Time) {
	cut := at.Add(-m.cfg.AltWindow)
	kept := m.switches[:0]
	for _, s := range m.switches {
		if s.at.After(cut) {
			kept = append(kept, s)
		}
	}
	m.switches = kept
}

func (m *Model) trimSeen(id string, at time.Time) {
	cut := at.Add(-m.cfg.RecurWindow)
	kept := m.seen[id][:0]
	for _, t := range m.seen[id] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	m.seen[id] = kept
	// Forget titles nobody has seen in a while, so a long session does not
	// accumulate a map of everything ever focused.
	if len(m.seen) > maxTitles {
		for k, ts := range m.seen {
			if len(ts) == 0 || ts[len(ts)-1].Before(cut) {
				delete(m.seen, k)
			}
		}
	}
}

// maxTitles bounds the recurrence table. A working day passes through a few
// hundred windows; this is generous and still finite.
const maxTitles = 512

// describe names a window the way a person would: the application, and the
// title when it says something the application does not. A terminal whose
// title is its own name should read as "foot", not "foot (foot)".
func describe(app, title string) string {
	switch {
	case title == "" || strings.EqualFold(title, app):
		return app
	case app == "":
		return quoted(title)
	default:
		return app + " (" + title + ")"
	}
}

// spell writes a length of time the way a sentence would. The structured
// Minutes field is there for a brain that would rather count; this is for
// the one that hands the summary to a model, where "0 minutes" reads as a
// mistake and "half an hour" reads as an observation.
func spell(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < 2*time.Minute:
		return "a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 2*time.Hour:
		mins := int(d.Minutes()) % 60
		if mins < 5 {
			return "an hour"
		}
		return fmt.Sprintf("an hour and %d minutes", mins)
	default:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
}

func quoted(s string) string { return "\"" + s + "\"" }

// plainDuration is "5 minutes", "an hour" — the way a sentence would say it.
func plainDuration(d time.Duration) string {
	switch {
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d >= time.Hour:
		return "hour"
	default:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
}

func ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return fmt.Sprintf("%d%s", n, suffix)
}

// The shapes that are about the clock rather than about a window.
//
// The first three facts are all "what have you been doing"; these three are
// "when is this happening", which a brain cannot work out for itself
// because it is not the thing being told when nothing happened. They all
// come off one number — the gap since a real window was last seen — which
// is why they are computed together and before that gap is closed.
//
// A first observation produces none of them. There is no baseline yet, and
// a ghost that greets its own installation with "back after a long absence"
// is describing the moment before it existed.
func (m *Model) arrival(app, title string, at time.Time) []Fact {
	prev := m.lastSeen
	m.lastSeen = at
	if prev.IsZero() {
		// Nothing to compare against, but the day and night are now known,
		// so tomorrow is a real morning rather than the first one.
		m.morningDay = at.Format(dayKey)
		return nil
	}

	var facts []Fact
	// A new day and a long absence are the same event described twice, and
	// the morning is the better description, so only one of them fires.
	// The hour guard is what stops working past midnight from reading as a
	// new morning at one minute past twelve.
	switch {
	case at.Format(dayKey) != prev.Format(dayKey) && at.Hour() >= smallHours:
		if f, ok := m.morning(app, title, at); ok {
			facts = append(facts, f)
		}
	case at.Sub(prev) >= m.cfg.AwayLong:
		if f, ok := m.returning(prev, at); ok {
			facts = append(facts, f)
		}
	}
	if f, ok := m.late(app, title, at); ok {
		facts = append(facts, f)
	}
	return facts
}

// dayKey is a calendar date, which is what "a new day" means here.
const dayKey = "2006-01-02"

// morning reports the first window of a new day. Once per day by its own
// bookkeeping rather than by the cooldown: the cooldown is half an hour and
// a morning is not something that can happen twice.
func (m *Model) morning(app, title string, at time.Time) (Fact, bool) {
	day := at.Format(dayKey)
	if m.morningDay == day {
		return Fact{}, false
	}
	m.morningDay = day
	return Fact{
		Kind:    Morning,
		App:     app,
		Title:   title,
		Summary: fmt.Sprintf("the first thing today is %s", describe(app, title)),
	}, true
}

// returning reports coming back after being away. The duration is what
// makes it worth saying: "back after ten minutes" is not an observation,
// which is what AwayLong is for.
func (m *Model) returning(prev, at time.Time) (Fact, bool) {
	away := at.Sub(prev)
	if !m.mayReport("returning", at) {
		return Fact{}, false
	}
	return Fact{
		Kind:    Returning,
		Minutes: int(away.Minutes()),
		Summary: fmt.Sprintf("back after %s away", spell(away)),
	}, true
}

// late reports that the day should be over. Once per night, where a night
// runs past midnight — working at one in the morning is still tonight, and
// being told twice about it is exactly the nagging the cooldown exists to
// prevent.
func (m *Model) late(app, title string, at time.Time) (Fact, bool) {
	if m.cfg.LateHour < 0 {
		return Fact{}, false
	}
	if h := at.Hour(); h < m.cfg.LateHour && h >= smallHours {
		return Fact{}, false
	}
	night := nightKey(at)
	if m.lateNight == night {
		return Fact{}, false
	}
	m.lateNight = night
	return Fact{
		Kind:  Late,
		App:   app,
		Title: title,
		Summary: fmt.Sprintf("still going at %s — %s",
			strings.ToLower(at.Format("3:04pm")), describe(app, title)),
	}, true
}

// nightKey names the night a moment belongs to, so midnight does not split
// one evening into two.
func nightKey(at time.Time) string {
	return at.Add(-smallHours * time.Hour).Format(dayKey)
}

// State is the part of the model worth surviving a restart, and no more.
//
// What is here: the recurrence table, the cooldowns, when a window was last
// seen, and which day and night have already been remarked on. What is
// deliberately not: the stretch in progress and the recent switches. A
// dwell interrupted by the ghost stopping was interrupted — the person got
// up — and an alternation window is five minutes, which is shorter than
// any restart worth reasoning about. Resuming either would be inventing
// continuity rather than remembering it.
//
// Titles appear only as identities (see identity). This is written to disk
// by the caller, so it is built to be dull reading.
type State struct {
	// Seen is the recurrence table: title identity to when it was seen.
	Seen map[string][]time.Time `json:"seen,omitempty"`
	// Said is the cooldown table: subject identity to when it last fired.
	Said map[string]time.Time `json:"said,omitempty"`
	// LastSeen is when a real window was last observed.
	LastSeen time.Time `json:"last_seen,omitempty"`
	// MorningDay and LateNight are the day and night already remarked on.
	MorningDay string `json:"morning_day,omitempty"`
	LateNight  string `json:"late_night,omitempty"`
}

// Snapshot returns what is worth keeping. Safe to call between Observes;
// the maps are copied, so the caller may take its time writing them.
func (m *Model) Snapshot() State {
	st := State{
		LastSeen:   m.lastSeen,
		MorningDay: m.morningDay,
		LateNight:  m.lateNight,
		Seen:       make(map[string][]time.Time, len(m.seen)),
		Said:       make(map[string]time.Time, len(m.said)),
	}
	for k, v := range m.seen {
		if len(v) == 0 {
			continue // trimmed to nothing; there is no fact left in it
		}
		st.Seen[k] = append([]time.Time(nil), v...)
	}
	for k, v := range m.said {
		st.Said[k] = v
	}
	return st
}

// Restore builds a model that carries on from a snapshot. Anything already
// too old to matter is dropped on the way in rather than trusted: a machine
// that was off for a week must not come back believing it is halfway
// through an hour of recurrences, and the clock may have moved in ways this
// package has no business reasoning about.
func Restore(cfg Config, st State, now time.Time) *Model {
	m := New(cfg)
	m.lastSeen = st.LastSeen
	m.morningDay = st.MorningDay
	m.lateNight = st.LateNight

	recurCut := now.Add(-m.cfg.RecurWindow)
	for k, times := range st.Seen {
		var kept []time.Time
		for _, t := range times {
			if t.After(recurCut) && !t.After(now) {
				kept = append(kept, t)
			}
		}
		if len(kept) > 0 {
			m.seen[k] = kept
		}
	}
	saidCut := now.Add(-m.cfg.Cooldown)
	for k, t := range st.Said {
		if t.After(saidCut) && !t.After(now) {
			m.said[k] = t
		}
	}
	// A snapshot from the future is a clock that moved; the safe reading is
	// that nothing is known about when anything happened.
	if m.lastSeen.After(now) {
		m.lastSeen = time.Time{}
	}
	return m
}
