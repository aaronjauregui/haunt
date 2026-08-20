package defaultbrain

import (
	"github.com/aaronjauregui/haunt/brainkit"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// The face at rest.
//
// A ghost's expression used to change only when it spoke, so between
// sentences — nearly all of the time a companion is on screen — the face
// was frozen wherever the last line left it. The driver knows the shape of
// the day and had nowhere to put it; the brain heard about that shape and
// could only turn it into words.
//
// So the driver offers the occasion (a `session` event marked MayPose,
// meaning its own rules about faces changing unasked are already
// satisfied) and this brain decides what to wear. Deciding is the brain's
// job because it is the thing that holds the character: a driver picking a
// face with nothing to derive it from would be inventing personality, and
// a ghost with a brain of its own must be able to disagree with this
// table — or ignore the offer entirely, which is what a brain that never
// calls SetExpression already does.
//
// The choice is a table rather than a model call, on the grounds that this
// happens unprompted and often. A face that cost a round trip would be a
// face the ghost could not afford to change, and the whole point is that
// this one is free.

// restPoses maps what was noticed about the day to the faces that suit it,
// best first. Shells declare whatever poses they like — four in the
// built-in placeholder, thirty-seven in a converted ukagaka shell, none at
// all in a single-image one — so every entry is a preference order and the
// first pose the shell actually has wins. A shape whose whole chain is
// missing leaves the face alone, which is the right answer: a ghost that
// cannot look sleepy should not look surprised instead.
var restPoses = map[string][]string{
	// The day should be over. This is the one the design named, and the
	// one a person is most likely to catch: a ghost that looks sleepy
	// after midnight.
	"late": {"asleep", "sad", "thinking"},
	// The first window of a new day, and coming back after being away.
	// Both are arrivals, and a face that brightens for them is the other
	// half of "attentive when you come back from an afternoon out".
	"morning":   {"happy", "surprised"},
	"returning": {"happy", "surprised"},
	// Concentration: a long stretch on one thing, a flip-flop between two,
	// a title that keeps coming back. Nothing cheerful about any of them,
	// and nothing alarming either.
	"dwell":       {"thinking", "asleep"},
	"alternation": {"thinking", "surprised"},
	"recurrence":  {"thinking", "sad"},
}

// wearForOccasion changes the face to suit something noticed about the
// day, when the driver has offered and the shell has a pose for it.
func (b *Brain) wearForOccasion(e proto.Event, emit *brainkit.Emitter) {
	if !e.MayPose || len(b.poses) == 0 {
		return
	}
	pose := pickPose(e.Name, b.poses)
	if pose == "" || pose == b.worn {
		return
	}
	b.worn = pose
	emit.SetExpression(pose)
}

// pickPose returns the best face the shell has for a session shape, or
// empty if it has none of them.
func pickPose(kind string, available []string) string {
	has := map[string]bool{}
	for _, p := range available {
		has[p] = true
	}
	for _, pose := range restPoses[kind] {
		if has[pose] {
			return pose
		}
	}
	return ""
}
