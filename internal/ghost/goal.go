package ghost

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// Goals, and the shape of a week.
//
// The ghost can answer, remember, notice a long afternoon and carry an
// errand until four o'clock. What it could not do is hold what you said you
// were trying to do and notice when your week stopped matching it — which
// is the one join nothing else on the machine can make. A calendar knows
// your appointments and a task manager knows what you wrote down; neither
// of them is watching, so neither can tell that you said the thesis
// mattered and then spent the fortnight somewhere else. The ghost is the
// only thing holding both halves.
//
// # A goal is an intention with no moment
//
// No new type, no second store slot, no second lifecycle. An Intention with
// a zero Due is already a purpose the ghost is carrying with no particular
// moment attached, `resolve` already discharges it, and the list already
// caps and persists itself. That is a goal, and grouping them is the point
// rather than an economy: a goal and an errand are the same shape —
// something taken on, not yet discharged, known to be not yet discharged.
//
// One thing has to change, and it is one conditional at one seam: an errand
// rides into every prompt, which is right for something due in an hour and
// wrong for a goal that would pay rent on six months of requests to be
// relevant on one of them. See turnContext in prompt.go, which carries
// only what has come due.
//
// # The goal is the query, not the result
//
// This is the place haunt's shape genuinely differs from a chat assistant.
// Every retrieval mechanism in the field is conditioned on an utterance —
// something the person just said, to hang relevance off. The hardest case
// here is the opposite one, the ghost speaking first, where there is no
// query to embed at all. So a goal is not a memory to be retrieved when it
// happens to resemble something; it is a **standing query that gets run**.
// A pass takes one goal, asks what actually happened, and produces
// something only when the two have come apart.
//
// # Because the goal is the query, it is also the filter at record time
//
// The session model keeps a rolling hour (trimSeen, RecurWindow) and its
// titles are identities rather than text. The barrier is retention, not
// readability: even in the clear there would be nothing behind an hour ago
// to ask a question of. Rather than start keeping a fortnight of everything
// you looked at — by a wide margin the largest and most sensitive thing
// this project would ever write to disk — the goal itself says what to
// watch for, and what is recorded is nothing more than *this goal was seen
// at this time*: one named subject the person declared, and a list of
// timestamps.
//
// That is small, it is legible — `haunt journal -intentions` prints the
// whole of it — and it is opt-in in the strongest sense, because nothing is
// watched that the person did not name.
//
// # It starts knowing nothing, and that is the feature
//
// A goal accumulates evidence only from the moment it is stated, so the
// first useful remark is a week or two out. That is deliberate, and the
// instinct to fill the silence should be resisted. A ghost that answers
// "remind me the thesis matters" with "you have barely touched it in three
// weeks" is revealing that it was watching before it was asked, which is
// exactly the thing the consent design exists to make untrue. The silence
// is what makes the promise honest: it watches what you named, from when
// you named it, and nothing else.

const (
	// goalMinAge is how long a goal must have been held before it may
	// produce anything at all. Day one is silent by design; so is day
	// three. A first week that says nothing is this working.
	goalMinAge = 7 * 24 * time.Hour
	// driftAfter is how long nothing to do with a goal must have been in
	// front of the person before that is worth remarking on. A week is a
	// week of work missed, which is the unit the person themselves thinks
	// in; a shorter one turns a quiet Thursday into an accusation.
	driftAfter = 7 * 24 * time.Hour
	// driftQuiet is how long a goal stays quiet after it has produced a
	// remark. Long, for the reason reactCooldown in brain_model.go is long: the
	// failure mode this direction can most easily have is becoming the
	// thing you close.
	driftQuiet = 14 * 24 * time.Hour
	// sightingGap is the least time between two recorded sightings of one
	// goal. The watch loop polls every three seconds and an afternoon at
	// the thesis is one fact, not four thousand.
	sightingGap = 5 * time.Minute
	// maxSightings bounds what is kept per goal. Only the last one is ever
	// read; the rest are there so a person can see what their ghost has on
	// them, which stops being reassuring somewhere well before it becomes a
	// log file.
	maxSightings = 40
	// maxGoals is the cap, and it is small on purpose: a person with thirty
	// goals has no goals. Oldest gives way, as with intentions generally.
	maxGoals = 7
	// maxWatchWords is how many words one goal looks for. Past a handful
	// the goal stops being a subject and starts being a sentence, and a
	// sentence matches everything.
	maxWatchWords = 5
	// minWatchWord is the shortest word worth looking for. Two letters
	// match half the titles on a desktop.
	minWatchWord = 3
	// driftName prefixes the errand a finding turns into, so it is
	// recognisable in the list and so re-checking the same goal replaces
	// its finding rather than adding a second.
	driftName = internalKeyPrefix + "drift/"
)

// filler is the words a goal's own text carries that say nothing about what
// to look for. Two groups: ordinary English glue, and the framing a person
// or a ghost wraps a goal in — "remind me", "keep an eye on", "I should
// work on". What is left is the subject, which is the whole trick.
//
// Deliberately not a stemmer or a stopword corpus. The alternative worth
// considering is asking a model once, at the moment the goal is taken on,
// to turn "the thesis" into a few things to look for; that belongs at
// goal-creation time rather than in the check, and is the fallback if this
// turns out not to be enough. It is not a regex, and never will be: the
// audience for this typed "remind me the thesis matters", not `\.tex$`.
var filler = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "but": true, "or": true,
	"of": true, "to": true, "in": true, "on": true, "at": true, "for": true,
	"with": true, "from": true, "about": true, "into": true, "over": true,
	"my": true, "me": true, "i": true, "you": true, "your": true, "them": true,
	"they": true, "it": true, "its": true, "this": true, "that": true,
	"is": true, "are": true, "was": true, "be": true, "been": true, "am": true,
	"do": true, "does": true, "did": true, "have": true, "has": true, "had": true,
	"will": true, "would": true, "should": true, "could": true, "can": true,
	"more": true, "less": true, "some": true, "any": true, "not": true,
	"remind": true, "reminder": true, "remember": true, "keep": true,
	"eye": true, "watch": true, "watching": true, "mind": true, "bring": true,
	"up": true, "tell": true, "say": true, "make": true, "sure": true,
	"matter": true, "matters": true, "important": true, "really": true,
	"work": true, "working": true, "spend": true, "time": true, "day": true,
	"week": true, "get": true, "back": true, "going": true, "go": true,
	"want": true, "need": true, "try": true, "trying": true, "stop": true,
}

// watchWords turns a goal's own words into the few things to look for in a
// window title. Empty when nothing survives — a goal that says only "be
// better about this" names no subject, and inventing one for it would be
// the ghost deciding for itself what it was allowed to watch.
func watchWords(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !isWordRune(r)
	}) {
		if len([]rune(w)) < minWatchWord || filler[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) == maxWatchWords {
			break
		}
	}
	return out
}

func isWordRune(r rune) bool {
	return r == '_' || r == '\'' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
		r > 127
}

// trimGoals enforces the goal cap, which is separate from the intention cap
// because it is a different argument: twelve errands is a busy afternoon,
// and seven goals is somebody who has not chosen. Goals the person has set
// aside go first — they are already over — and then the oldest, which is
// the one they have most obviously stopped meaning.
//
// Callers pass a list already sorted oldest first.
func (g *Ghost) trimGoals(list []Intention) []Intention {
	goals := 0
	for _, it := range list {
		if it.Goal() {
			goals++
		}
	}
	if goals <= maxGoals {
		return list
	}
	drop := map[string]bool{}
	for _, aside := range []bool{true, false} {
		for _, it := range list {
			if goals <= maxGoals {
				break
			}
			if !it.Goal() || it.Aside != aside || drop[it.Name] {
				continue
			}
			drop[it.Name] = true
			goals--
			g.log.Info(hlog.Memory, "letting go of a goal: %s", it.Text)
		}
	}
	kept := make([]Intention, 0, len(list))
	for _, it := range list {
		if drop[it.Name] {
			continue
		}
		kept = append(kept, it)
	}
	return kept
}

// watcher is one goal reduced to what the watch loop needs: a name, the
// words, and when a sighting was last written down. Held in memory so the
// three-second poll costs a map walk rather than a store scan.
type watcher struct {
	name  string
	words []string
	last  time.Time
}

// refreshGoalWatch rebuilds the in-memory watch list from the intentions.
// Called wherever the list is written, so the watch loop never has to read
// the store to find out what it is watching.
func (g *Ghost) refreshGoalWatch(list []Intention) {
	next := make([]watcher, 0, len(list))
	for _, it := range list {
		if it.Watched() {
			next = append(next, watcher{name: it.Name, words: it.Watch, last: it.LastSeen()})
		}
	}
	g.goalMu.Lock()
	defer g.goalMu.Unlock()
	g.goalWatch = next
}

// noteGoalSighting is the record-time filter, called from the watch loop on
// every poll. It writes down nothing about the window: the only thing that
// leaves this function is *which named goal* was in front of the person,
// and the time.
//
// The ignore list has already been applied upstream, and an ignored
// application arrives here as an empty title — so a goal is never recorded
// against a window the person said was none of the ghost's business.
func (g *Ghost) noteGoalSighting(app, title string, now time.Time) {
	if title == "" && app == "" {
		return
	}
	haystack := strings.ToLower(app + " " + title)

	// Under the lock: decide. Outside it: write. The store write takes
	// other locks, and no lock here is ever held while another is acquired.
	var hit []string
	g.goalMu.Lock()
	for i := range g.goalWatch {
		w := &g.goalWatch[i]
		if now.Sub(w.last) < sightingGap {
			continue
		}
		for _, word := range w.words {
			if mentions(haystack, word) {
				w.last = now
				hit = append(hit, w.name)
				break
			}
		}
	}
	g.goalMu.Unlock()
	if len(hit) == 0 {
		return
	}
	g.recordSightings(hit, now)
}

// recordSightings appends the timestamps and saves.
func (g *Ghost) recordSightings(names []string, now time.Time) {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	g.intentionsMu.Lock()
	defer g.intentionsMu.Unlock()
	list := g.loadIntentions()
	touched := false
	for i := range list {
		if !want[list[i].Name] || !list[i].Watched() {
			continue
		}
		list[i].Seen = append(list[i].Seen, now)
		if len(list[i].Seen) > maxSightings {
			list[i].Seen = list[i].Seen[len(list[i].Seen)-maxSightings:]
		}
		touched = true
		g.log.Debug(hlog.Screen, "goal seen: %s", list[i].Text)
	}
	if touched {
		g.saveIntentions(list)
	}
}

// The check.
//
// It wants no scheduling machinery of its own: it runs when the ghost is
// idle, which is both the honest time and the time it costs nothing. Three
// rules.
//
//   - **It must be able to find nothing**, and finding nothing must cost
//     nothing. That is the common case. Every ambient assistant that has
//     become intolerable became intolerable by making the scheduled check
//     feel obliged to produce output. This one costs no model call at all,
//     in either direction: what a goal has been measured against is
//     arithmetic over its own timestamps, and there is nothing here to ask
//     a model about.
//   - **At most one goal per pass.** Two observations at once is a status
//     report, and a ghost that delivers status reports is a dashboard with
//     a face.
//   - **A finding becomes an intention, not a sentence.** The pass runs
//     when the ghost is idle, which is exactly when it should not be
//     interrupting. What it produces is something the ghost is now meaning
//     to raise, which comes up when there is a person to raise it with.
//
// What it spends is the obligation allowance rather than curiosity's, and
// the rule that decides it is *who decided*: the ghost choosing to speak is
// curiosity, and the ghost discharging something a person put on its books
// is an obligation. A goal is on the books by definition. The finding
// inherits Asked from the goal it came from, which is the same driver-side
// bit set when the goal was taken on — so a goal a brain invented for
// itself while nobody was there produces a finding charged to curiosity,
// and cannot say otherwise.

// drifter is the state of the pass. Only a timer: there is no model call
// to cancel, because there is no model call.
type drifter struct {
	mu    sync.Mutex
	timer *time.Timer
}

// mentions reports whether name occurs in text as a whole word, so "Sam"
// does not match "same" and a project called "go" does not match every
// sentence containing "going".
func mentions(text, name string) bool {
	if name == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(text[i:], name)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(name)
		if !wordish(text, start-1) && !wordish(text, end) {
			return true
		}
		i = start + 1
	}
}

// wordish reports whether the byte at i is part of a word, treating
// out-of-range as not.
func wordish(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	r := rune(s[i])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// goalCheckDelay is how long after the person goes idle the goal check
// runs. Idle fires on a threshold the person may have set to seconds, and
// "away from the desk" is not the same as "stepped out to make tea": a
// person who comes back in ninety seconds should not find a fortnight's
// verdict waiting for them.
const goalCheckDelay = 5 * time.Minute

// checkDelay is goalCheckDelay, overridable for tests and demos.
func (g *Ghost) checkDelay() time.Duration {
	if g.opts.GoalCheckDelay > 0 {
		return g.opts.GoalCheckDelay
	}
	return goalCheckDelay
}

// beginGoalCheck is the idle hook.
func (g *Ghost) beginGoalCheck() {
	g.drifter.mu.Lock()
	defer g.drifter.mu.Unlock()
	if g.drifter.timer != nil {
		return
	}
	g.drifter.timer = time.AfterFunc(g.checkDelay(), func() {
		g.go_(hlog.Memory, "checking a goal", g.checkGoalsOnce)
	})
}

// endGoalCheck is the active hook: the person is back, so the pass does not
// happen this time.
func (g *Ghost) endGoalCheck() {
	g.drifter.mu.Lock()
	defer g.drifter.mu.Unlock()
	if g.drifter.timer != nil {
		g.drifter.timer.Stop()
		g.drifter.timer = nil
	}
}

// checkGoalsOnce runs one pass.
func (g *Ghost) checkGoalsOnce() {
	g.drifter.mu.Lock()
	g.drifter.timer = nil
	g.drifter.mu.Unlock()

	g.intentionsMu.Lock()
	list := g.loadIntentions()
	now := time.Now()
	which, ok := drifted(list, now)
	if !ok {
		g.intentionsMu.Unlock()
		return // the common case, and it cost nothing
	}
	goal := list[which]
	name := driftName + goal.Name
	found := Intention{
		Name: name,
		Text: driftText(goal, now),
		Made: now,
		// Due now, and deliberately not armed here: the pass is running
		// because nobody is there, and a reminder raised into an empty room
		// is a reminder spent. It is armed when the person comes back (see
		// armLateIntentions), which is the "there is somebody to raise it
		// with" the rule asks for.
		Due: now,
		// Inherited, never asked afresh: the question is who put this on
		// the ghost's books, and the answer was settled when the goal was.
		Asked: goal.Asked,
	}
	list[which].Said = now

	kept := make([]Intention, 0, len(list)+1)
	for _, it := range list {
		if it.Name != name {
			kept = append(kept, it)
		}
	}
	kept = append(kept, found)
	sort.SliceStable(kept, func(a, b int) bool { return kept[a].Made.Before(kept[b].Made) })
	kept, gone := g.capIntentions(kept)
	g.saveIntentions(kept)
	g.intentionsMu.Unlock()
	g.takeDownNotes(gone)
	// The one thing that has to be visible from outside this file: there is
	// now something owed that nothing is holding a timer for.
	g.late.Store(true)
	g.log.Info(hlog.Memory, "a goal has gone quiet: %s", goal.Text)
}

// drifted picks the one goal, if any, that the person has stopped touching.
// Pure, so the thresholds can be argued with in a test rather than waited
// out. It returns an index into list.
//
// Oldest first among the candidates, and only one: the goal that has been
// held longest and neglected longest is the one worth a word, and a second
// one in the same breath would be a report.
func drifted(list []Intention, now time.Time) (int, bool) {
	best, found := -1, false
	for i, it := range list {
		if !it.Watched() {
			continue // an errand, one set aside, or one with nothing to look for
		}
		if now.Sub(it.Made) < goalMinAge {
			continue // day one is silent by design, and so is day six
		}
		if !it.Said.IsZero() && now.Sub(it.Said) < driftQuiet {
			continue // it has said this recently; saying it again is nagging
		}
		// Never seen counts, and counts from when the goal was stated
		// rather than from the beginning of time: a goal taken on this
		// morning has not been neglected, it has barely been held.
		since := it.LastSeen()
		if since.IsZero() {
			since = it.Made
		}
		if now.Sub(since) < driftAfter {
			continue
		}
		if !found || it.Made.Before(list[best].Made) {
			best, found = i, true
		}
	}
	return best, found
}

// driftText is what the ghost will be meaning to say. Not the sentence
// itself — the ghost says that in its own voice when the moment comes —
// but what the finding was, plainly enough that a person reading
// `haunt journal -intentions` sees exactly the observation that was made
// about them.
func driftText(goal Intention, now time.Time) string {
	since := goal.LastSeen()
	how := "nothing to do with it has been in front of them since they said so"
	if !since.IsZero() {
		how = "they have not been near it in " + roughly(now.Sub(since))
	}
	text := "bring up what they said mattered — " + goal.Text + " — because " + how
	if len([]rune(text)) > maxIntentionChars {
		text = string([]rune(text)[:maxIntentionChars])
	}
	return text
}

// roughly says how long ago in the units a person would use. Never more
// precise than the observation deserves: "eleven days" is a fact, "11.4
// days" is a spreadsheet.
func roughly(d time.Duration) string {
	switch days := int(d / (24 * time.Hour)); {
	case days >= 60:
		return "a couple of months"
	case days >= 28:
		return "about a month"
	case days >= 12:
		return "a fortnight or so"
	case days <= 1:
		return "a day"
	default:
		return fmt.Sprintf("%d days", days)
	}
}

// armLateIntentions starts the timers for anything the goal check left
// deliberately unarmed. Called when the person comes back.
//
// This is how a drift finding waits for somebody to raise it with. It is
// gated on a flag rather than on reading the store, because it sits on the
// path of every EventActive — which is every time the person touches the
// machine — and in the overwhelmingly common case there is nothing to do.
// Intentions with a live timer are left alone either way, so a person going
// idle and active again does not push their four o'clock reminder back by
// the settling delay each time.
func (g *Ghost) armLateIntentions() {
	if !g.late.Swap(false) {
		return
	}
	now := time.Now()
	for _, it := range g.loadIntentions() {
		if !it.Overdue(now) {
			continue
		}
		g.timersMu.Lock()
		_, armed := g.intentTimers[it.Name]
		g.timersMu.Unlock()
		if armed {
			continue
		}
		g.log.Debug(hlog.Memory, "raising something that came due while they were away: %s", it.Text)
		g.armIntention(it)
	}
}
