package defaultbrain

import (
	"testing"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// placeholderPoses is what the built-in shell declares. Deliberately not
// the rich set: the interesting case is a shell that has some of the faces
// this brain would like and not others.
var placeholderPoses = []string{"asleep", "happy", "sad", "surprised"}

func boot(poses []string) proto.Event {
	return proto.Event{Type: proto.EventBoot, Poses: poses}
}

func session(kind string, mayPose bool) proto.Event {
	return proto.Event{
		Type: proto.EventSession, Name: kind, Text: kind, MayPose: mayPose,
		Session: &proto.SessionFact{Kind: kind, Summary: kind},
	}
}

// The whole point of the direction: the shape of the day reaches the face,
// not only the balloon. A ghost that looks sleepy after midnight is two
// things that both already existed and had never been connected.
func TestTheDayShapesTheFace(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want string
	}{
		{"late", "asleep"},
		{"morning", "happy"},
		{"returning", "happy"},
		// The placeholder has no "thinking", so concentration falls
		// through the chain to the next face it does have.
		{"dwell", "asleep"},
		{"alternation", "surprised"},
		{"recurrence", "sad"},
	} {
		intents := run(t, boot(placeholderPoses), session(tc.kind, true))
		poses := only(intents, proto.IntentSetExpression)
		if len(poses) != 1 || poses[0].Name != tc.want {
			t.Errorf("%s: expressions = %+v, want one %q", tc.kind, poses, tc.want)
		}
	}
}

// The offer is the driver's to make. It withholds MayPose over a
// fullscreen window and under reduced motion, and a brain that changed its
// face anyway would be pulling faces over somebody's film.
func TestNoFaceWithoutTheOffer(t *testing.T) {
	intents := run(t, boot(placeholderPoses), session("late", false))
	if got := only(intents, proto.IntentSetExpression); len(got) != 0 {
		t.Errorf("wore a face with no offer: %+v", got)
	}
	// The remark is unaffected: words are not motion, and the driver
	// decides separately whether initiative may be spent.
	if got := only(intents, proto.IntentChat); len(got) != 1 {
		t.Errorf("chats = %+v, want the observation to still provoke one", got)
	}
}

// A shell with none of the faces a shape suggests keeps the one it has on.
// Substituting something from another shape would be worse than doing
// nothing: a ghost that cannot look sleepy should not look surprised.
func TestAShellWithoutTheFaceIsLeftAlone(t *testing.T) {
	intents := run(t, boot([]string{"happy"}), session("recurrence", true))
	if got := only(intents, proto.IntentSetExpression); len(got) != 0 {
		t.Errorf("substituted a face it had for one it did not: %+v", got)
	}
}

// A single-image shell has no face to arrange, and nothing about it
// reaches the brain as a pose list.
func TestNoPosesNoExpressions(t *testing.T) {
	intents := run(t, boot(nil), session("late", true))
	if got := only(intents, proto.IntentSetExpression); len(got) != 0 {
		t.Errorf("chose a pose for a shell that declares none: %+v", got)
	}
}

// The same shape twice running is one face change, not two. Repeating it
// costs nothing the driver would not drop, but a brain emitting intents
// nobody can observe is a brain whose logs lie about what it did.
func TestTheSameShapeIsNotWornTwice(t *testing.T) {
	intents := run(t, boot(placeholderPoses),
		session("dwell", true), session("dwell", true), session("morning", true))
	poses := only(intents, proto.IntentSetExpression)
	if len(poses) != 2 || poses[0].Name != "asleep" || poses[1].Name != "happy" {
		t.Errorf("expressions = %+v, want the dwell once and then the morning", poses)
	}
}
