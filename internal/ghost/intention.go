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
//   - **It is carried, not merely scheduled.** Open intentions ride along
//     in the ghost's context, the way the journal does, so the ghost knows
//     what it is holding and can say so if asked. A durable timer that the
//     ghost could not talk about would be a cron job with a face.
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

const intentionsKey = internalKeyPrefix + "intentions"

const (
	// maxIntentions bounds what a ghost may be holding. Small, because
	// these are pasted into every prompt and because a companion carrying
	// forty errands is a task list, which is a different application and a
	// worse one. The oldest gives way.
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
	list := g.loadIntentions()
	kept := make([]Intention, 0, len(list)+1)
	for _, it := range list {
		if it.Name == in.Name {
			made = it.Made // taken on when it was first taken on
			continue
		}
		kept = append(kept, it)
	}
	next := Intention{Name: in.Name, Text: text, Made: made}
	if in.DelayMS > 0 {
		next.Due = now.Add(time.Duration(in.DelayMS) * time.Millisecond)
	}
	kept = append(kept, next)
	// Oldest out first. Not by strength as memory does: an intention is not
	// remembered, it is owed, and the one that has been owed longest is the
	// one that has most obviously not been discharged.
	sort.SliceStable(kept, func(a, b int) bool { return kept[a].Made.Before(kept[b].Made) })
	if len(kept) > maxIntentions {
		for _, dropped := range kept[:len(kept)-maxIntentions] {
			g.log.Info(hlog.Memory, "letting go of an intention: %s", dropped.Text)
			g.cancelIntention(dropped.Name)
		}
		kept = kept[len(kept)-maxIntentions:]
	}
	g.saveIntentions(kept)
	g.log.Info(hlog.Memory, "intending: %s", text)
	g.armIntention(next)
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
	g.publish(proto.Event{
		Type: proto.EventIntention,
		Name: raised.Name,
		Text: raised.Text,
	})
}

// intentionsContext renders what the ghost is holding, for the system
// prompt. This is what separates an intention from a durable timer: the
// ghost knows what it has taken on, so it can be asked about it, and it
// answers from the same place the reminder will come from rather than from
// a guess.
func (g *Ghost) intentionsContext() string {
	list := g.loadIntentions()
	if len(list) == 0 {
		return ""
	}
	now := time.Now()
	var b strings.Builder
	b.WriteString("\n\nWhat you are meaning to do (you took these on; " +
		"mention one only if it is relevant or its moment has come):\n")
	for _, it := range list {
		b.WriteString("- " + it.Text)
		switch {
		case it.Fired:
			b.WriteString(" (its moment has come and you have not dealt with it yet)")
		case it.Due.IsZero():
			b.WriteString(" (no particular moment)")
		default:
			b.WriteString(" (due " + when(it.Due, now) + ")")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// when renders a due time the way a person would say it. Absolute dates
// rather than "in 3h": a model reading "in 3 hours" months later in a
// recalled fragment would have no idea what it meant.
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
	raw, ok, err := storeGet(g.store, intentionsKey)
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
	b, err := json.Marshal(list)
	if err != nil {
		return
	}
	if err := g.store.Set(intentionsKey, string(b)); err != nil {
		g.log.Warn(hlog.Store, "saving intentions: %v", err)
	}
}

// IntentionsStoreKey exposes the list's store key to the CLI.
const IntentionsStoreKey = intentionsKey

// DecodeIntentions parses a stored list, for `haunt intentions`.
func DecodeIntentions(raw string) ([]Intention, error) {
	var list []Intention
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, err
	}
	return list, nil
}
