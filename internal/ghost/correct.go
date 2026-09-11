package ghost

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// Correcting the ghost.
//
// Every other mechanism in the memory design is additive with decay, and
// not one of them has a path for *that is wrong*. Facts accumulate and the
// oldest falls off the end. The memory index forgets by strength, but
// strength is lifted by how often a memory has proved relevant, so a wrong
// thing that keeps coming up is reinforced. And the entity path retrieves
// by *name*: a wrong fact about Sam is put in front of the model every time
// Sam is mentioned, whether or not it bears on what was said, and each of
// those mentions raises the subject's strength, which protects the slot the
// wrong fact is sitting in. Talking about someone is the thing that keeps
// the ghost's wrong beliefs about them alive.
//
// The reading half was already built and built deliberately: `haunt journal
// -entities` prints every subject and what the ghost has concluded about
// it, on the grounds that this is a thing a person is entitled to read
// rather than infer from behaviour. What was missing was the verb. A
// companion you can interrogate but cannot correct is worse than one you
// can do neither to: it shows you the belief, and then keeps it.
//
// Three things this is careful about.
//
// **It supersedes rather than deletes.** A retired fact stops entering
// context, becomes the first thing its subject gives up when the index
// needs room, and stays readable in the record. A ghost that silently
// erased what it used to believe would be unauditable exactly where
// auditability matters most: "you used to think X" is a thing a person may
// reasonably want to check, and a revision that leaves no trace is
// indistinguishable from a belief that was never there.
//
// **It does not touch the material.** The exchanges a conclusion was drawn
// from are left exactly as they are, because half the time they are *true*
// and only the conclusion has gone stale — Sam really did work at Acme, and
// it is March that has changed. What stops the belief coming back is not
// scrubbing the evidence but outranking it: the fact this writes carries
// FactPerson, and Entity.blocks will not let anything with less authority
// file the same conclusion again.
//
// **The ghost does not argue.** If the person says a fact is wrong then it
// is wrong. There is no weighing of the ghost's own conclusion against the
// correction and no second confirmation, because anything else is the
// failure mode that makes a companion insufferable, and it would be
// arrived at by reasoning that sounds careful.
//
// This is driver-side and reaches no model. A model interpreting "no, Sam
// left in March" is a model that can get the subject wrong, get the
// polarity wrong, or decide the remark was conversational and file it as
// one more fact — which is the bug this exists to fix, arrived at from the
// other end. A correction has to be the one path in this system that cannot
// be misheard.

// applyCorrection retires a fact about a subject and records what is true
// instead. Either half may be omitted: a fact id with no replacement is a
// plain retraction, and a replacement with no id is the legitimate case
// where nothing was wrong and something has merely changed.
//
// It returns a line describing what happened, for the person who asked.
func (g *Ghost) applyCorrection(subject string, factID int, truth string) (string, error) {
	subject = strings.TrimSpace(subject)
	truth = strings.TrimSpace(truth)
	if subject == "" {
		return "", fmt.Errorf("which subject?")
	}
	if factID <= 0 && truth == "" {
		return "", fmt.Errorf("nothing to retire and nothing to record")
	}
	if len([]rune(truth)) > maxEntityFactChars {
		truth = string([]rune(truth)[:maxEntityFactChars])
	}

	g.memMu.Lock()
	defer g.memMu.Unlock()

	slug := entitySlug(subject)
	if slug == "" {
		return "", fmt.Errorf("%q is not a name", subject)
	}
	key := entityKeyPrefix + slug

	var e Entity
	var found bool
	for _, existing := range g.loadEntities() {
		if existing.key == key {
			e, found = existing, true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("it has not concluded anything about %q", subject)
	}

	now := time.Now()
	var retired string
	// The replacement's id has to be known before the retirement is written,
	// so By can point at it.
	newID := e.NextFact
	if newID == 0 {
		newID = 1
	}

	if factID > 0 {
		at := -1
		for i, f := range e.Facts {
			if f.ID == factID && f.Standing() {
				at = i
				break
			}
		}
		if at < 0 {
			return "", fmt.Errorf("%s has no standing fact %d", e.Name, factID)
		}
		retired = e.Facts[at].Text
		e.Facts[at].Until = now
		if truth != "" {
			e.Facts[at].By = newID
		}
	}

	if truth != "" {
		e.addFacts(FactPerson, now, []string{truth})
	}

	// Correcting a subject is engagement with it, and the subject's slot
	// should survive the correction: without this, a name the person cared
	// enough to fix could be the next one evicted. The old objection to
	// mentions raising strength does not apply, since what the strength is
	// now protecting is the corrected belief.
	e.Last = now
	e.Mentions++
	e.trimFacts()

	if err := g.putEntity(e); err != nil {
		return "", fmt.Errorf("recording the correction: %w", err)
	}

	switch {
	case retired != "" && truth != "":
		g.log.Info(hlog.Memory, "corrected %q: %q -> %q", e.Name, retired, truth)
		return fmt.Sprintf("%s: no longer %q; now %q", e.Name, retired, truth), nil
	case retired != "":
		g.log.Info(hlog.Memory, "retracted about %q: %q", e.Name, retired)
		return fmt.Sprintf("%s: no longer %q", e.Name, retired), nil
	default:
		g.log.Info(hlog.Memory, "told about %q: %q", e.Name, truth)
		return fmt.Sprintf("%s: %q", e.Name, truth), nil
	}
}

// The correction dialogue.
//
// The verb has to be reachable without a terminal, or it does not really
// exist. The flow revision is designed around is *see what it thinks* →
// *say that is wrong* → *watch it stop thinking that*, and a first step in
// `haunt journal -entities` followed by a second step in a shell command is
// a flow almost nobody completes. A correction mechanism that is right in
// every particular and used by nobody leaves the failure happening at
// exactly the rate it happens now.
//
// So this is a menu, on the ghost, answered from the keyboard like every
// other menu. The brain proposes it — `brainkit.Emitter.Correct`, offered
// as a menu entry the way the offer to watch is — and every term of the
// dialogue after that is the driver's: the subjects come from the driver's
// index, the questions go through g.ask, and the answer arrives where the
// brain cannot hear it. A brain may propose and must not perform.
//
// It is also the only maintenance path a correction has. A correction can
// go stale on its own — the person said Sam left Acme, and Sam has since
// come back — and the only thing that may retire a person's fact is
// another one. If the sole way to reach the verb were a shell command, a
// belief the person had already taken the trouble to fix would be the
// hardest thing in the system to fix again.
const (
	correctNoSubject = "Nothing, never mind"
	correctNoFact    = "Actually, it's all fine"
	correctJustWrong = "It's just wrong — forget it"
)

// execCorrect runs the whole dialogue: which subject, which belief, and
// what is true instead.
func (g *Ghost) execCorrect() {
	if !g.memoryEnabled() {
		g.ask("I don't keep notes about anyone, so there's nothing to correct.", "Oh, alright")
		return
	}

	// Subjects the person is most likely to mean: the ones talked about
	// most, which is the same order the journal prints them in. Capped
	// because a menu is read, not scrolled.
	ents := g.loadEntities()
	var named []Entity
	for _, e := range ents {
		if e.countStanding() > 0 {
			named = append(named, e)
		}
	}
	if len(named) == 0 {
		g.ask("I haven't decided anything about anyone yet.", "Fair enough")
		return
	}
	sort.SliceStable(named, func(i, j int) bool {
		if named[i].Mentions != named[j].Mentions {
			return named[i].Mentions > named[j].Mentions
		}
		return named[i].Last.After(named[j].Last)
	})
	if len(named) > correctMenuMax {
		named = named[:correctMenuMax]
	}

	options := make([]string, 0, len(named)+1)
	for _, e := range named {
		options = append(options, e.Name)
	}
	options = append(options, correctNoSubject)

	who := g.ask("Who have I got wrong?", options...)
	if who == "" || who == correctNoSubject {
		return
	}
	var subject Entity
	for _, e := range named {
		if e.Name == who {
			subject = e
			break
		}
	}
	if subject.Name == "" {
		return
	}

	// Which belief. The person is choosing from what the ghost actually
	// believes rather than typing a description of it, which is what makes
	// this unambiguous in a way a sentence never is.
	standing := subject.StandingFacts()
	if len(standing) > correctMenuMax {
		standing = standing[len(standing)-correctMenuMax:]
	}
	beliefs := make([]string, 0, len(standing)+1)
	for _, f := range standing {
		beliefs = append(beliefs, shortFact(f.Text))
	}
	beliefs = append(beliefs, correctNoFact)

	picked := g.ask("What have I got wrong about "+subject.Name+"?", beliefs...)
	if picked == "" || picked == correctNoFact {
		return
	}
	target := -1
	for i, b := range beliefs {
		if b == picked && i < len(standing) {
			target = standing[i].ID
			break
		}
	}
	if target < 0 {
		return
	}

	// And what is true instead. Typed, because this is the one thing the
	// person has to supply in their own words — but it may be skipped, and
	// skipping is not a lesser answer. "That is wrong, and I am not going
	// to tell you what is right" is a complete correction.
	truth := g.askForText("So what's true about " + subject.Name + "?")
	line, err := g.applyCorrection(subject.Name, target, truth)
	if err != nil {
		g.log.Warn(hlog.Memory, "correcting %q: %v", subject.Name, err)
		g.ask("I couldn't write that down, sorry.", "Never mind")
		return
	}
	// Said back, so the person sees the belief actually change rather than
	// having to take it on faith — the third step of the flow, and the one
	// that makes the second worth doing.
	g.ask("Noted. "+line, "Good")
}

// correctMenuMax is how many entries a correction menu offers. A menu is
// read at a glance; past this it is a list somebody has to search, and the
// subjects worth correcting are the ones that keep coming up anyway.
const correctMenuMax = 8

// shortFact fits a belief into a menu line.
func shortFact(s string) string {
	const max = 56
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) <= max {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:max-1])) + "…"
}
