package defaultbrain

import (
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// The rule that makes the world outside the desktop reliable: a poll takes
// something on rather than saying it. A schedule silently skips the runs a
// closed laptop was away for, so anything that must happen has to be held
// as an intention — which survives a restart, is still owed if its moment
// lands in a quiet hour, and is discharged deliberately.
func TestAPollIsTakenOnRatherThanSaid(t *testing.T) {
	intents := run(t, proto.Event{
		Type: proto.EventCron, Name: "calendar", Text: "standup at 15:00",
	})

	held := only(intents, proto.IntentIntend)
	if len(held) != 1 {
		t.Fatalf("intend intents = %+v, want the one the poll led to", held)
	}
	if !strings.Contains(held[0].Text, "standup at 15:00") {
		t.Errorf("intention = %q, want what the poll found in it", held[0].Text)
	}
	if held[0].DelayMS == 0 {
		t.Error("the intention has no moment, which makes it a goal rather than an errand")
	}
	if said := only(intents, proto.IntentSay); len(said) != 0 {
		t.Errorf("the poll spoke: %+v", said)
	}
	if chatted := only(intents, proto.IntentChat); len(chatted) != 0 {
		t.Errorf("the poll asked a model about itself: %+v", chatted)
	}
}

// The next change to the same calendar replaces what the ghost was meaning
// to say about the last one, rather than queueing a second copy behind it.
func TestPollsOfOneTriggerShareAName(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventCron, Name: "calendar", Text: "standup at 15:00"},
		proto.Event{Type: proto.EventCron, Name: "calendar", Text: "standup moved to 15:30"},
	)
	held := only(intents, proto.IntentIntend)
	if len(held) != 2 {
		t.Fatalf("intend intents = %+v, want one per change", held)
	}
	if held[0].Name != held[1].Name {
		t.Errorf("names = %q and %q, want one name per trigger", held[0].Name, held[1].Name)
	}
}

// And an occasion is still an occasion: a trigger with no tool on it is a
// moment the manifest named, and the model finds the words.
func TestAnOccasionStillSpeaks(t *testing.T) {
	intents := run(t, proto.Event{Type: proto.EventCron, Name: "morning"})
	if held := only(intents, proto.IntentIntend); len(held) != 0 {
		t.Errorf("an occasion was taken on rather than said: %+v", held)
	}
	if chats := only(intents, proto.IntentChat); len(chats) != 1 {
		t.Errorf("occasion chats = %+v, want the one line about it", chats)
	}
}
