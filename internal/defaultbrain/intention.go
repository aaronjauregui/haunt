package defaultbrain

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/brainkit"
)

// Being asked to remember to do something.
//
// This is the one built-in path to a standing intention, and it is
// deliberately the plainest one: "remind me in ten minutes to check the
// build". A brain of its own can take on intentions for far more
// interesting reasons — after a tool call, when a session shape repeats —
// but a person should be able to hand a ghost an errand without knowing
// that any of that exists.
//
// Nothing here needs a model. Parsing "in ten minutes" is a table and a
// number, and a companion that had to make an API call to set a reminder
// would be a companion that could not set one while the network was down.

// intentionTag marks the remark a due intention provoked, so the brain can
// tell that it was actually said before discharging it. An intention whose
// moment came during quiet hours, or over a fullscreen game, is refused by
// the driver and must stay on the books — a reminder that silently
// evaporates is worse than no reminder at all.
const intentionTag = "intention:"

// remindName is what an errand is filed under. One counter's worth of
// distinct names, so two reminders do not replace each other; the name is
// never shown to anybody.
func remindName(n int) string { return fmt.Sprintf("%serrand-%d", KeyPrefix, n) }

// errandKeyPrefix remembers what each name was actually about.
//
// The names are private and the person only ever knows the words they said,
// so this is what lets them say "never mind about the thesis" and have it
// reach the right one. It is also why the counter is restored at boot:
// a goal is meant to be held for months across a great many restarts, and a
// counter that started at zero every morning would silently replace
// yesterday's goal with today's errand.
const errandKeyPrefix = KeyPrefix + "errand/"

// restoreErrands reads back what this brain was carrying, at boot.
func (b *Brain) restoreErrands(kv map[string]string) {
	for k, v := range kv {
		name, ok := strings.CutPrefix(k, errandKeyPrefix)
		if !ok || v == "" {
			continue
		}
		b.errandTexts[KeyPrefix+name] = v
		if n, err := strconv.Atoi(strings.TrimPrefix(name, "errand-")); err == nil && n > b.errands {
			b.errands = n
		}
	}
}

// onRemind handles "remind me [in <when>] to <what>". Reports whether the
// text was one.
func (b *Brain) onRemind(text string, emit *brainkit.Emitter) bool {
	rest, ok := cutPrefixFold(text, "remind me ")
	if !ok {
		return false
	}
	delay, rest := leadingDelay(rest)
	// "to" is how people write it and carries nothing.
	if what, cut := cutPrefixFold(rest, "to "); cut {
		rest = what
	}
	what := strings.TrimSpace(rest)
	if what == "" {
		emit.SayFor("Remind you to do what?", lingerNote)
		return true
	}

	b.errands++
	name := remindName(b.errands)
	emit.Intend(name, what, int(delay/time.Millisecond))
	// What it was about, under its own name, so "never mind about that" has
	// something to match against later. The driver holds the intention; this
	// is only the brain's index into it.
	b.errandTexts[name] = what
	emit.Remember(errandKeyPrefix+strings.TrimPrefix(name, KeyPrefix), what)
	switch {
	case delay > 0:
		emit.SayFor("All right — I'll remind you "+plainly(delay)+".", lingerNote)
	default:
		// No moment given, so none is invented. The ghost carries it and
		// will bring it up when it is relevant, which is what the context
		// block is for; promising a time nobody asked for would be a
		// promise it has no way to keep.
		emit.SayFor("I'll keep that in mind and bring it up.", lingerNote)
	}
	return true
}

// onNeverMind is the other exit: the person has stopped caring whether this
// happens, which is not the same as it having been done.
//
// It exists because the worst thing a goal can do is outlive the wanting of
// it. A ghost still asking about a thesis that was submitted in March is
// the thing you close, and people punish that far harder than they punish
// silence — so giving up has to be as easy to say as taking it on was, and
// has to be recorded as giving up rather than as success.
//
// Matched on the person's own words, because the names are the driver's and
// nobody has ever seen one.
func (b *Brain) onNeverMind(text string, emit *brainkit.Emitter) bool {
	var subject string
	for _, opener := range []string{
		"stop asking about ", "stop asking me about ", "never mind about ",
		"never mind the ", "forget about ", "let go of ",
	} {
		if rest, ok := cutPrefixFold(text, opener); ok {
			subject = strings.TrimSpace(rest)
			break
		}
	}
	if subject == "" {
		return false
	}
	name, what := b.errandNamed(subject)
	if name == "" {
		emit.SayFor("I don't think I'm holding anything about that.", lingerNote)
		return true
	}
	emit.SetAside(name)
	delete(b.errandTexts, name)
	emit.Remember(errandKeyPrefix+strings.TrimPrefix(name, KeyPrefix), "")
	emit.SayFor("All right — I'll stop bringing up "+what+".", lingerNote)
	return true
}

// errandNamed finds what the person meant, by the words they used. Longest
// match wins, so "the thesis chapter" picks the chapter rather than
// whichever of the two the map happened to yield first.
func (b *Brain) errandNamed(subject string) (name, what string) {
	subject = strings.ToLower(strings.TrimSpace(subject))
	if subject == "" {
		return "", ""
	}
	for n, t := range b.errandTexts {
		lower := strings.ToLower(t)
		if !strings.Contains(lower, subject) && !strings.Contains(subject, lower) {
			continue
		}
		if what == "" || len(t) > len(what) {
			name, what = n, t
		}
	}
	return name, what
}

// onIntentionDue is a standing intention reaching its moment. It is said
// rather than announced: the ghost took this on in its own character and
// should hand it back in the same voice.
func (b *Brain) onIntentionDue(name, text string, emit *brainkit.Emitter) {
	emit.Send(brainkit.Intent{
		Type:       "chat",
		Text:       "You said you would remind them: " + text + ". Do that now, briefly and in your own voice.",
		SelfPrompt: true,
		Tag:        intentionTag + name,
		LingerMS:   lingerReply,
	})
}

// leadingDelay reads an "in <n> <unit>" prefix, returning what is left.
// Anything it does not understand is left alone and becomes part of the
// errand, so "remind me in the morning to call" is carried rather than
// rejected — with no moment, which is honest, since this cannot work out
// when the morning is.
func leadingDelay(s string) (time.Duration, string) {
	rest, ok := cutPrefixFold(strings.TrimSpace(s), "in ")
	if !ok {
		return 0, s
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return 0, s
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil || n <= 0 {
		return 0, s
	}
	unit, ok := units[strings.ToLower(strings.Trim(fields[1], ".,"))]
	if !ok {
		return 0, s
	}
	return time.Duration(n) * unit, strings.Join(fields[2:], " ")
}

// units are what a person writes when they mean a length of time. Weeks and
// beyond are left out: a ghost holding an errand for a fortnight is a
// calendar, and this is not one.
var units = map[string]time.Duration{
	"s": time.Second, "sec": time.Second, "secs": time.Second,
	"second": time.Second, "seconds": time.Second,
	"m": time.Minute, "min": time.Minute, "mins": time.Minute,
	"minute": time.Minute, "minutes": time.Minute,
	"h": time.Hour, "hr": time.Hour, "hrs": time.Hour,
	"hour": time.Hour, "hours": time.Hour,
	"d": 24 * time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour,
}

// plainly renders a delay the way somebody would say it back.
func plainly(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return count(int(d/(24*time.Hour)), "day")
	case d >= time.Hour:
		return count(int(d/time.Hour), "hour")
	case d >= time.Minute:
		return count(int(d/time.Minute), "minute")
	default:
		return count(int(d/time.Second), "second")
	}
}

func count(n int, unit string) string {
	if n != 1 {
		return fmt.Sprintf("in %d %ss", n, unit)
	}
	// "in a hour" is the kind of small wrongness that makes a companion
	// read as a machine, and there is exactly one vowel-initial unit here.
	if strings.HasPrefix(unit, "h") {
		return "in an " + unit
	}
	return "in a " + unit
}

// cutPrefixFold is strings.CutPrefix, case-insensitively: people capitalise
// the start of a sentence and mean the same thing by it.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return s, false
	}
	return s[len(prefix):], true
}
