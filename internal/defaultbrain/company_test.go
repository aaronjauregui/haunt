package defaultbrain

import (
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/proto"
)

func observed(at time.Time, from, action, detail string) proto.Event {
	return proto.Event{Type: proto.EventObserved, Time: at, From: from, Name: action, Text: detail}
}

// The point of the direction: a ghost may react to another ghost without
// being addressed by it. Out loud, because two characters talking where
// the person can see them is the whole of what having two is for.
func TestAGhostRemarksOnItsNeighbour(t *testing.T) {
	intents := run(t, observed(base, "whisper", proto.ActedSpoke, "the build finished"))
	chats := only(intents, proto.IntentChat)
	if len(chats) != 1 || !chats[0].SelfPrompt {
		t.Fatalf("chats = %+v, want one self-prompted", chats)
	}
	if !strings.Contains(chats[0].Text, "the build finished") ||
		!strings.Contains(chats[0].Text, "whisper") {
		t.Errorf("the direction lost what was observed: %q", chats[0].Text)
	}
	// Unprompted by construction: the driver charges this, and nothing
	// here may claim the ghost was spoken to.
	if got := only(intents, proto.IntentSay); len(got) != 0 {
		t.Errorf("it announced the observation directly: %+v", got)
	}
	// And nothing is sent back to the other ghost: a `tell` would hide the
	// one interesting thing about having two of them.
	if got := only(intents, proto.IntentTell); len(got) != 0 {
		t.Errorf("it replied privately: %+v", got)
	}
}

// The brake that the driver's charge alone does not provide. Two ghosts
// reacting to each other's reactions is a loop of individually legal moves
// that would spend a whole day's allowance in a minute.
func TestReactionsAreRationed(t *testing.T) {
	intents := run(t,
		observed(base, "whisper", proto.ActedSpoke, "one"),
		observed(base.Add(time.Minute), "whisper", proto.ActedSpoke, "two"),
		observed(base.Add(30*time.Minute), "whisper", proto.ActedSpoke, "three"),
	)
	chats := only(intents, proto.IntentChat)
	if len(chats) != 1 || !strings.Contains(chats[0].Text, "one") {
		t.Fatalf("chats = %+v, want only the first remark", chats)
	}

	// And it comes back once the hour is up, rather than being spent for
	// the session.
	intents = run(t,
		observed(base, "whisper", proto.ActedSpoke, "one"),
		observed(base.Add(reactCooldown+time.Minute), "whisper", proto.ActedSpoke, "later"),
	)
	if chats := only(intents, proto.IntentChat); len(chats) != 2 {
		t.Errorf("chats = %d, want the cooldown to expire", len(chats))
	}
}

// The first thing a neighbour does gets a reaction: a household feature
// that says nothing for its first hour is one nobody believes works.
func TestTheFirstThingIsWorthReactingTo(t *testing.T) {
	intents := run(t, proto.Event{Type: proto.EventBoot},
		observed(base, "whisper", proto.ActedSpoke, "morning"))
	if chats := only(intents, proto.IntentChat); len(chats) != 1 {
		t.Errorf("chats = %+v, want the neighbour's first line remarked on", chats)
	}
}

// Company, not news. A ghost that moved or blinked is not worth spending
// one of the person's interruptions on — but it is worth knowing about.
func TestSmallMovementsAreNotRemarkedOn(t *testing.T) {
	for _, action := range []string{proto.ActedMoved, proto.ActedExpression, proto.ActedClip} {
		intents := run(t, observed(base, "whisper", action, "happy"))
		if chats := only(intents, proto.IntentChat); len(chats) != 0 {
			t.Errorf("%s provoked a remark: %+v", action, chats)
		}
	}
	// Being poked is different: the person chose somebody, and that is the
	// kind of thing a character has an opinion about.
	intents := run(t, observed(base, "whisper", proto.ActedPoked, ""))
	if chats := only(intents, proto.IntentChat); len(chats) != 1 {
		t.Errorf("being poked went unremarked: %+v", chats)
	}
}

// What was seen outlasts the moment, exactly as what was noticed about the
// day does: when the ghost speaks first later, the room is something it
// can draw on.
func TestWhatTheNeighbourDidFeedsSpeakingFirst(t *testing.T) {
	intents := run(t,
		observed(base, "whisper", proto.ActedMoved, ""),
		proto.Event{Type: proto.EventIdle},
	)
	chats := only(intents, proto.IntentChat)
	if len(chats) != 1 {
		t.Fatalf("chats = %+v, want only the idle one", chats)
	}
	if !strings.Contains(chats[0].Text, "whisper") {
		t.Errorf("speaking first ignored the room: %q", chats[0].Text)
	}
}

// An observation with nothing in it describes nothing anybody saw.
func TestEmptyObservationsAreIgnored(t *testing.T) {
	intents := run(t,
		observed(base, "", proto.ActedSpoke, "who said that?"),
		observed(base, "whisper", proto.ActedSpoke, "   "),
		observed(base, "whisper", "sneezed", "?"),
	)
	if chats := only(intents, proto.IntentChat); len(chats) != 0 {
		t.Errorf("reacted to nothing: %+v", chats)
	}
}
