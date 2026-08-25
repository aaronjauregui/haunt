package defaultbrain

import (
	"strings"

	"github.com/aaronjauregui/haunt/brainkit"
)

// What the built-in brain does with something the world told it.
//
// A trigger that names a tool is a poll: the driver called somebody else's
// server on a schedule and this is what it said (see internal/ghost/poll.go
// for the whole of it). The one rule that matters here is that **a poll
// does not speak.** Both crontab and an in-process scheduler silently skip
// runs that a closed laptop was away for, so anything that has to happen
// must not depend on a poll having fired at the right moment. What the poll
// produces is an *intention*, which survives a restart, is still owed if
// its moment lands in a quiet hour or over a fullscreen game, and is
// discharged deliberately rather than by having gone off.
//
// This brain takes the plainest possible route to that, and deliberately no
// cleverer one: it means to mention what came back, shortly. A brain of its
// own can do far better — read the answer, work out that the standup is at
// three and take on an intention for ten to — and it needs a model call to
// do it, which is exactly the sort of thing a built-in brain should not be
// spending somebody's money on every quarter of an hour in the background.
// The driver has already dropped every answer identical to the last one, so
// what reaches here is a change, and a change is worth one line.

// pollName is what an intention taken on from a poll is filed under: one
// name per trigger, so the next change to the same calendar replaces what
// the ghost was meaning to say about the last one rather than queueing a
// second copy of it behind it.
func pollName(trigger string) string { return KeyPrefix + "poll/" + trigger }

// pollRaise is how long the ghost sits on a poll's answer before it means to
// bring it up. Not zero: an intention with no delay is a goal, which is a
// thing held indefinitely, and this is the opposite — something with a
// moment, and that moment is roughly now. A minute is enough to be safely
// after whatever else was happening when the poll landed.
const pollRaise = 60 * 1000

// onPoll turns a scheduled tool's answer into something the ghost means to
// raise. It says nothing at all: the poll is machinery, and the utterance
// belongs to the moment the intention comes due, in the ghost's own voice.
func (b *Brain) onPoll(trigger, answer string, emit *brainkit.Emitter) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return
	}
	emit.Intend(pollName(trigger), "tell them what the "+trigger+" check turned up: "+answer, pollRaise)
}
