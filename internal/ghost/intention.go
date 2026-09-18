package ghost

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// Somewhere to put an intention.
//
// A ghost could already schedule a timer, remember a fact and write a
// journal entry, and none of those is the thing a person means when they
// say "tell me when the build finishes". A timer dies with the process. A
// fact is something that is true, not something the ghost is *meaning to
// do*. A journal entry is a thing that happened. What was missing is a
// purpose the ghost is carrying: something it has taken on, has not
// discharged, and knows it has not discharged.
//
// That is what makes an ambient companion different from a chat window, and
// the design has said so from the start — an agent that notices the build
// finished twenty minutes after you stopped watching is doing something a
// chat window structurally cannot. It could not do it, though, because
// twenty minutes is longer than a session sometimes lasts and there was
// nowhere to put the intention across a restart.
//
// Three properties, and each of them is the point:
//
//   - **It survives.** An intention is in the store, not in a timer wheel.
//     A ghost restarted at lunchtime still means to tell you at four.
//   - **It is carried, not merely scheduled.** The ghost can list what it
//     is holding (the intentions tool) and say so if asked, and anything
//     that came due and was not dealt with rides along on every turn
//     (turnContext). A durable timer that the ghost could not talk about
//     would be a cron job with a face.
//   - **It is discharged deliberately.** Coming due does not clear it;
//     `resolve` does. So an intention whose moment arrived while the
//     balloon was suppressed — quiet hours, a fullscreen game — is still
//     being held afterwards rather than silently lost, which is the failure
//     mode a fire-and-forget reminder has and the reason people do not
//     trust them.
//
// Not to be confused with proto.Intent, which is a brain asking the driver
// for one thing right now. An intention is the opposite shape: the ghost's
// own, and about later.

// IntentionsStoreKey holds the list, and is exported for `haunt journal
// -intentions`.
const IntentionsStoreKey = internalKeyPrefix + "intentions"

const (
	// maxIntentions bounds what a ghost may be holding. Small, because a
	// companion carrying forty errands is a task list, which is a different
	// application and a worse one. The oldest gives way.
	maxIntentions = 12
	// maxIntentionChars keeps one to about a line, for the same reason.
	maxIntentionChars = 200
	// intentionSettle is how long after boot a due-in-the-past intention
	// waits before it fires. Long enough for the greeting to have happened,
	// because a ghost whose first words are a reminder it has been sitting
	// on has not said hello.
	intentionSettle = 20 * time.Second
)

// Intention is one purpose a ghost is carrying.
type Intention struct {
	// Name identifies it, for resolving and for replacing.
	Name string `json:"n"`
	// Text is what the ghost means to do, in its own words.
	Text string `json:"t"`
	// Due is when it should be raised. Zero means there is no moment: the
	// ghost is simply holding it until something discharges it.
	Due time.Time `json:"d,omitzero"`
	// Made is when it was taken on, so a person reading the list can see
	// what has been hanging around.
	Made time.Time `json:"m"`
	// Fired says its moment has passed and the brain has been told. It stays
	// on the list until it is resolved: an intention raised into a quiet
	// hour is still one the ghost is holding.
	Fired bool `json:"f,omitempty"`
	// Watch is what to look for on screen, for a goal: the goal's own
	// words with the filler taken out (see goal.go). Empty on an errand,
	// which has a moment instead, and on a goal whose words were all
	// filler — a goal nothing can be recorded against, which is honest
	// about what it can do rather than pretending.
	Watch []string `json:"w,omitempty"`
	// Seen is when this goal was noticed on screen, oldest first. It is
	// timestamps and nothing else: the goal is the filter at record time,
	// so what is kept is "this named thing was in front of you then" and
	// never what the window was. A person can read the whole of it back in
	// `haunt journal -intentions`, which is the point.
	Seen []time.Time `json:"e,omitempty"`
	// Said is when this goal last produced a remark, so it does not produce
	// another for a good long while. Nagging is the failure mode this whole
	// mechanism can most easily have.
	Said time.Time `json:"q,omitzero"`
	// Aside says the person has stopped caring whether this happens. It is
	// not resolved — it was never done, and recording it as done would be a
	// lie — it is simply no longer watched, no longer checked and no longer
	// brought up. See execSetAside.
	Aside bool `json:"s,omitempty"`
	// Asked says a person was talking to the ghost when this was taken on,
	// which is what makes raising it an obligation rather than a whim (see
	// markObliged in curiosity.go). Decided driver-side from
	// respondingToUser at the moment of creation and never from anything
	// the brain says, because a brain that could set this could mint itself
	// an allowance out of the user's reminders.
	Asked bool `json:"a,omitempty"`
}

// Goal reports whether this is a goal rather than an errand: something
// taken on with no particular moment, which is what a person means when
// they say the thesis matters. The distinction is one field and no second
// lifecycle — see the top of goal.go for why that is the whole of it.
func (i Intention) Goal() bool { return i.Due.IsZero() }

// Watched reports whether the ghost is still recording sightings of this
// goal: it has to be a goal, it has to have something to look for, and the
// person must not have set it aside.
func (i Intention) Watched() bool { return i.Goal() && !i.Aside && len(i.Watch) > 0 }

// LastSeen is when this goal was last in front of the person, or the zero
// time if it never has been.
func (i Intention) LastSeen() time.Time {
	if len(i.Seen) == 0 {
		return time.Time{}
	}
	return i.Seen[len(i.Seen)-1]
}

// Overdue reports whether this intention's moment has passed unmarked.
func (i Intention) Overdue(now time.Time) bool {
	return !i.Fired && !i.Due.IsZero() && !i.Due.After(now)
}

// execIntend takes on an intention, or replaces one of the same name.
// Replacing rather than duplicating is what lets a brain re-arm the same
// purpose — "check again in ten minutes" — without the list filling with
// copies of one errand.
func (g *Ghost) execIntend(in proto.Intent) {
	text := strings.TrimSpace(in.Text)
	if len([]rune(text)) > maxIntentionChars {
		text = string([]rune(text)[:maxIntentionChars])
	}
	now := time.Now()
	made := now
	var prior Intention
	list := g.loadIntentions()
	kept := make([]Intention, 0, len(list)+1)
	for _, it := range list {
		if it.Name == in.Name {
			made = it.Made // taken on when it was first taken on
			prior = it
			continue
		}
		kept = append(kept, it)
	}
	// Whether this is on the ghost's books or is its own idea. Asked now
	// rather than when it comes due, because by then the answer would be
	// about whoever happens to be at the desk at four o'clock.
	//
	// Two ways to be on the books, and they are the two named in
	// curiosity.go: a person was there when it was taken on, or the ghost
	// was in the middle of discharging something already on them — a
	// manifest trigger's occasion, a poll's answer (poll.go), an errand
	// coming due and being re-armed for later. A brain can forge neither,
	// because both windows are opened driver-side by things a brain cannot
	// cause: the person acting, and the clock.
	next := Intention{Name: in.Name, Text: text, Made: made, Asked: g.respondingToUser() || g.obliged()}
	if in.DelayMS > 0 {
		next.Due = now.Add(time.Duration(in.DelayMS) * time.Millisecond)
	}
	if next.Goal() {
		// What the ghost will look for on screen, worked out once here
		// rather than on every poll or every pass. Re-stating a goal in
		// different words is a person saying the same thing again, so the
		// evidence and the quiet period carry over rather than a rephrasing
		// silently buying a fresh week of not being asked about it.
		next.Watch = watchWords(text)
		next.Seen, next.Said, next.Aside = prior.Seen, prior.Said, prior.Aside
	}
	kept = append(kept, next)
	// Oldest out first. Not by strength as memory does: an intention is not
	// remembered, it is owed, and the one that has been owed longest is the
	// one that has most obviously not been discharged.
	sort.SliceStable(kept, func(a, b int) bool { return kept[a].Made.Before(kept[b].Made) })
	kept = g.capIntentions(kept)
	g.saveIntentions(kept)
	g.log.Info(hlog.Memory, "intending: %s", text)
	g.armIntention(next)
}

// capIntentions enforces both caps on a list already sorted oldest first:
// the goal cap, which is small and has its own argument, and then the
// overall one. Shared with the goal check, because a run of findings the
// brain never resolves would otherwise be the one path that could grow the
// list past the bound everything else respects.
func (g *Ghost) capIntentions(kept []Intention) []Intention {
	kept = g.trimGoals(kept)
	if len(kept) <= maxIntentions {
		return kept
	}
	for _, dropped := range kept[:len(kept)-maxIntentions] {
		g.log.Info(hlog.Memory, "letting go of an intention: %s", dropped.Text)
		g.cancelIntention(dropped.Name)
	}
	return kept[len(kept)-maxIntentions:]
}

// execResolve discharges an intention. Unknown names are ignored: a brain
// resolving something twice, or something the driver dropped, is not an
// error worth reporting to it.
func (g *Ghost) execResolve(name string) {
	list := g.loadIntentions()
	kept := make([]Intention, 0, len(list))
	found := false
	for _, it := range list {
		if it.Name == name {
			found = true
			continue
		}
		kept = append(kept, it)
	}
	if !found {
		return
	}
	g.cancelIntention(name)
	g.saveIntentions(kept)
	g.log.Info(hlog.Memory, "intention discharged: %s", name)
}

// execSetAside stops a goal being watched, without discharging it.
//
// The other exit, and deliberately not the same one. `resolve` says the
// thing was done; this says the person has stopped caring whether it is.
// A goal that is finished or abandoned and still producing "you have not
// touched this in eleven days" is the worst thing goals can do, and users
// punish it much harder than they punish silence — so giving up has to be
// sayable, and has to be recorded as giving up rather than as success.
//
// What is set aside stays on the list. Deleting it would write a discharge
// into the record that never happened; it just stops being watched, stops
// being checked and stops reaching a prompt.
func (g *Ghost) execSetAside(name string) {
	list := g.loadIntentions()
	found := false
	for i := range list {
		if list[i].Name != name || list[i].Aside {
			continue
		}
		list[i].Aside = true
		// Nothing more will be recorded against it, and what was recorded
		// was only ever useful for the check that is no longer going to
		// run. Dropped rather than kept, because the smallest honest amount
		// of history is the one nobody has to be reassured about.
		list[i].Seen = nil
		found = true
	}
	if !found {
		return
	}
	g.cancelIntention(name)
	g.saveIntentions(list)
	g.log.Info(hlog.Memory, "no longer watching: %s", name)
}

// armIntentions schedules everything the ghost was still holding when it
// last stopped. Called once, at boot, after the brain exists to hear the
// results.
//
// An intention whose moment passed while the ghost was not running fires
// shortly after boot rather than at once, and rather than being dropped:
// being switched off is not a reason to forget something you were asked to
// do, and firing during startup would put it under the greeting.
func (g *Ghost) armIntentions() {
	list := g.loadIntentions()
	// Before the early return: a ghost with nothing but goals still has
	// something to watch for, and the watch loop is about to start.
	g.refreshGoalWatch(list)
	if len(list) == 0 {
		return
	}
	now := time.Now()
	var held, late int
	for _, it := range list {
		if it.Fired || it.Due.IsZero() {
			held++
			continue
		}
		if it.Overdue(now) {
			late++
		}
		g.armIntention(it)
	}
	g.log.Info(hlog.Memory, "carrying %d intention(s); %d came due while it was away, %d have no moment",
		len(list), late, held)
}

// armIntention schedules one, if it has a moment still to come.
func (g *Ghost) armIntention(it Intention) {
	if it.Fired || it.Due.IsZero() {
		return
	}
	wait := time.Until(it.Due)
	if wait < intentionSettle {
		// Either overdue or nearly so. Both get the settling delay, which
		// is what keeps a reminder from landing on top of a greeting.
		wait = intentionSettle
	}
	name := it.Name
	g.timersMu.Lock()
	defer g.timersMu.Unlock()
	if g.intentTimers == nil {
		g.intentTimers = map[string]*time.Timer{}
	}
	if t, ok := g.intentTimers[name]; ok {
		t.Stop()
	}
	g.intentTimers[name] = time.AfterFunc(wait, func() {
		defer g.recoverPanic(hlog.Memory, "intention "+name)
		select {
		case <-g.done:
		default:
			g.fireIntention(name)
		}
	})
}

// cancelIntention stops a pending timer.
func (g *Ghost) cancelIntention(name string) {
	g.timersMu.Lock()
	defer g.timersMu.Unlock()
	if t, ok := g.intentTimers[name]; ok {
		t.Stop()
		delete(g.intentTimers, name)
	}
}

// fireIntention raises one with the brain and marks it as raised. It stays
// on the list: what the ghost *does* about it is the brain's business, and
// discharging it is a separate decision (see execResolve).
func (g *Ghost) fireIntention(name string) {
	list := g.loadIntentions()
	var raised Intention
	found := false
	for i := range list {
		if list[i].Name != name {
			continue
		}
		list[i].Fired = true
		raised = list[i]
		found = true
	}
	if !found {
		return
	}
	g.saveIntentions(list)
	g.cancelIntention(name)
	g.log.Info(hlog.Memory, "intention due: %s", raised.Text)
	// An errand the person asked for is theirs, and raising it spends from
	// the obligation allowance rather than competing with the ghost's small
	// talk. One the ghost set itself is its own idea and is charged like
	// one. Marked here rather than in publishExternal because the answer is
	// on the intention, and putting it on the event would put it where a
	// brain could read it.
	if raised.Asked {
		g.markObliged()
	}
	g.publish(proto.Event{
		Type: proto.EventIntention,
		Name: raised.Name,
		Text: raised.Text,
	})
}

// when renders a due time the way a person would say it. Absolute dates
// rather than "in 3h": a model reading "in 3 hours" later in its session
// would have no idea what it meant.
func when(due, now time.Time) string {
	switch d := due.Sub(now); {
	case d < 0:
		return "already, on " + due.Format("2 Jan at 15:04")
	case due.YearDay() == now.YearDay() && due.Year() == now.Year():
		return "today at " + due.Format("15:04")
	default:
		return due.Format("2 Jan at 15:04")
	}
}

func (g *Ghost) loadIntentions() []Intention {
	raw, ok, err := g.store.Get(IntentionsStoreKey)
	if err != nil || !ok {
		return nil
	}
	var list []Intention
	if json.Unmarshal([]byte(raw), &list) != nil {
		return nil
	}
	return list
}

func (g *Ghost) saveIntentions(list []Intention) {
	// Whatever the watch loop should be looking for now, worked out here
	// because this is every path by which the list changes. It costs a
	// short slice; the alternative is the poll reading the store.
	g.refreshGoalWatch(list)
	b, err := json.Marshal(list)
	if err != nil {
		return
	}
	if err := g.store.Set(IntentionsStoreKey, string(b)); err != nil {
		g.log.Warn(hlog.Store, "saving intentions: %v", err)
	}
}

// DecodeIntentions parses a stored list, for `haunt intentions`.
func DecodeIntentions(raw string) ([]Intention, error) {
	var list []Intention
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, err
	}
	return list, nil
}
