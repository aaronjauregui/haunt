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
// Two properties this deliberately has. **Nothing is written down**: the
// state lives in memory for the life of the session and is gone when the
// ghost stops, because a log of somebody's working day is the last thing
// that should outlive the reason for having it. And **the ignore list
// applies before any of this**, upstream in the caller: an application
// nobody wants watched must not appear in a duration statistic either, even
// unnamed.
package session

import (
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
	}
}

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
	seen map[string][]time.Time
	// said is when each subject last produced a fact, for the cooldown.
	said map[string]time.Time
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
	return &Model{cfg: cfg, seen: map[string][]time.Time{}, said: map[string]time.Time{}}
}

// Observe records what is focused now and returns anything worth saying
// about it. Call it on every poll, changed or not: a dwell is noticed by
// nothing happening.
func (m *Model) Observe(app, title string, at time.Time) []Fact {
	if app == "" && title == "" {
		return m.Away(at)
	}
	var facts []Fact

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
			prev := m.seen[title]
			if len(prev) == 0 || at.Sub(prev[len(prev)-1]) >= m.cfg.RecurGap {
				m.seen[title] = append(prev, at)
			}
			m.trimSeen(title, at)
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
	times := m.seen[title]
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

// mayReport enforces the cooldown for one subject.
func (m *Model) mayReport(subject string, at time.Time) bool {
	if last, ok := m.said[subject]; ok && at.Sub(last) < m.cfg.Cooldown {
		return false
	}
	m.said[subject] = at
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

func (m *Model) trimSeen(title string, at time.Time) {
	cut := at.Add(-m.cfg.RecurWindow)
	kept := m.seen[title][:0]
	for _, t := range m.seen[title] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	m.seen[title] = kept
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
