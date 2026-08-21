package defaultbrain

import (
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/brainkit"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// Keeping company.
//
// When another ghost is resident, this one sees what it does in public:
// it spoke, it drew something, it got poked. Nobody addressed this ghost,
// so anything it does about that is unprompted, and the driver charges it
// from the same allowance that rations speaking first (see
// internal/ghost/sociality.go).
//
// The charge is not by itself enough, which is the thing worth writing
// down. Two ghosts reacting to each other's reactions is a loop where
// every turn is legal — each one is a ghost noticing something and saying
// one short thing about it — and the only brake in the driver is the daily
// allowance. A pair could therefore burn a whole day's interruptions in
// under a minute at nine in the morning and stand mute until midnight,
// having said nothing to the person at all. So the brain rations itself
// too, and the interesting part of this file is how.

// reactCooldown is how long a ghost keeps quiet about its neighbours after
// remarking on one of them.
//
// An hour, which is long, and long on purpose. Two characters noticing
// each other occasionally is the behaviour this is for; two characters
// commenting on everything each other does is a chat log the person did
// not ask for and is paying for. At this rate a pair adds at most a couple
// of remarks an hour against a default allowance of twelve a day, which
// leaves the allowance for what it is really for — the ghost noticing
// something about the *person*.
//
// It starts spent-free rather than fresh from boot, so the first thing a
// neighbour does gets a reaction. A feature nobody can see working in the
// first minute is a feature nobody believes is there.
const reactCooldown = time.Hour

// worthRemarkingOn are the public actions that carry enough to say
// anything about. A ghost that moved or blinked is company, not news: it
// is remembered as what the room is doing (lastSeen, which feeds speaking
// first) and nothing more. Being poked is in because it is the person
// choosing somebody, and a ghost with a character has opinions about that.
var worthRemarkingOn = map[string]bool{
	proto.ActedSpoke: true,
	proto.ActedDrew:  true,
	proto.ActedPoked: true,
}

// onObserved handles another resident ghost doing something in public.
func (b *Brain) onObserved(e proto.Event, emit *brainkit.Emitter) {
	if e.From == "" {
		return
	}
	seen := describe(e)
	if seen == "" {
		return
	}
	// Whatever it was, it is now part of what this ghost knows about the
	// room, whether or not it is worth interrupting anybody over.
	b.lastSeen = seen

	if !worthRemarkingOn[e.Name] {
		return
	}
	if !b.lastReacted.IsZero() && e.Time.Sub(b.lastReacted) < reactCooldown {
		return
	}
	b.lastReacted = e.Time
	// Said aloud rather than told: two ghosts talking where the person can
	// see them is the whole point, and a `tell` would hide the one
	// interesting thing about having two of them. It is also what makes
	// the exchange finite — the reply is itself a public action the other
	// ghost observes, and its own cooldown is what ends the conversation.
	// About the neighbour by name, so what this ghost makes of it is kept
	// under that name rather than dissolving into the pool. Months of
	// occasional remarks then add up to a sense of who the other one is,
	// which comes back the next time anybody mentions them — the difference
	// between banter and a relationship.
	emit.InitiateAbout(e.From, "Another ghost is in the room with you. "+seen+
		". Say one short thing about that, out loud, in your own character — "+
		"or nothing much, if it would be tiresome. They are not you and "+
		"they are not the person you live with.")
}

// describe puts a public action into the words a model can work with. The
// ghost's name is used as it announced it, since that is what the person
// sees on screen too.
func describe(e proto.Event) string {
	switch e.Name {
	case proto.ActedSpoke:
		text := strings.TrimSpace(e.Text)
		if text == "" {
			return ""
		}
		return e.From + " just said: " + text
	case proto.ActedDrew:
		if e.Text == "" {
			return e.From + " just drew something"
		}
		return e.From + " just drew a picture of " + e.Text
	case proto.ActedPoked:
		return "the person just poked " + e.From + ", not you"
	case proto.ActedMoved:
		return e.From + " just moved somewhere else on the screen"
	case proto.ActedExpression:
		if e.Text == "" {
			return ""
		}
		return e.From + " is wearing a " + e.Text + " expression"
	case proto.ActedClip:
		return e.From + " is animating"
	}
	return ""
}
