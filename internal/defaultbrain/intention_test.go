package defaultbrain

import (
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// "remind me in ten minutes to check the build" is the plainest path to a
// standing intention, and it needs no model: parsing a length of time is a
// table and a number, and a companion that had to call an API to set a
// reminder could not set one with the network down.
func TestRemindMeTakesOnAnIntention(t *testing.T) {
	intents := run(t, proto.Event{
		Type: proto.EventMessage, Text: "remind me in 10 minutes to check the build",
	})
	held := only(intents, proto.IntentIntend)
	if len(held) != 1 {
		t.Fatalf("emitted %+v, want one intention", intents)
	}
	if held[0].Text != "check the build" {
		t.Errorf("text = %q, want the errand without the scaffolding", held[0].Text)
	}
	if held[0].DelayMS != 10*60*1000 {
		t.Errorf("delay = %d ms, want ten minutes", held[0].DelayMS)
	}
	// And it says so, because a reminder you are not sure was taken is one
	// you set again somewhere else.
	said := only(intents, proto.IntentSay)
	if len(said) != 1 || !strings.Contains(said[0].Text, "10 minutes") {
		t.Errorf("said %+v, want it repeating the time back", said)
	}
	// Never to the model: this path exists so it works offline.
	if chats := only(intents, proto.IntentChat); len(chats) != 0 {
		t.Errorf("setting a reminder went to the model: %+v", chats)
	}
}

// With no time given, none is invented: the ghost carries it and brings it
// up when it is relevant. Promising a moment nobody asked for would be a
// promise it has no way to keep.
func TestRemindMeWithNoMoment(t *testing.T) {
	intents := run(t, proto.Event{
		Type: proto.EventMessage, Text: "remind me to water the plants",
	})
	held := only(intents, proto.IntentIntend)
	if len(held) != 1 {
		t.Fatalf("emitted %+v, want one intention", intents)
	}
	if held[0].DelayMS != 0 {
		t.Errorf("delay = %d, want none invented", held[0].DelayMS)
	}
	if held[0].Text != "water the plants" {
		t.Errorf("text = %q", held[0].Text)
	}
}

// Two reminders are two reminders. They are named by a counter precisely so
// the second does not replace the first.
func TestTwoRemindersAreBothKept(t *testing.T) {
	intents := run(t,
		proto.Event{Type: proto.EventMessage, Text: "remind me in 5 minutes to stretch"},
		proto.Event{Type: proto.EventMessage, Text: "remind me in 20 minutes to eat"},
	)
	held := only(intents, proto.IntentIntend)
	if len(held) != 2 {
		t.Fatalf("emitted %d intentions, want two", len(held))
	}
	if held[0].Name == held[1].Name {
		t.Errorf("both are called %q, so the second replaced the first", held[0].Name)
	}
}

// An errand is discharged when it has actually been handed back, not when
// its moment arrived — a reminder the driver refused during quiet hours is
// still owed.
func TestAnErrandIsResolvedOnlyOnceSaid(t *testing.T) {
	intents := run(t, proto.Event{
		Type: proto.EventIntention, Name: "default/errand-1", Text: "check the build",
	})
	chats := only(intents, proto.IntentChat)
	if len(chats) != 1 {
		t.Fatalf("emitted %+v, want it handing the errand back", intents)
	}
	if !chats[0].SelfPrompt {
		t.Error("a reminder nobody just asked for must be marked unprompted")
	}
	if !strings.HasPrefix(chats[0].Tag, intentionTag) {
		t.Errorf("tag = %q, want it traceable back to the errand", chats[0].Tag)
	}
	// Nothing is discharged yet: the driver may still refuse to say it.
	if done := only(intents, proto.IntentResolve); len(done) != 0 {
		t.Errorf("discharged before it was said: %+v", done)
	}

	// Once the words actually landed, it is done with.
	intents = run(t,
		proto.Event{Type: proto.EventIntention, Name: "default/errand-1", Text: "check the build"},
		proto.Event{Type: proto.EventAIReply, Tag: intentionTag + "default/errand-1", Text: "the build is done"},
	)
	done := only(intents, proto.IntentResolve)
	if len(done) != 1 || done[0].Name != "default/errand-1" {
		t.Errorf("resolved %+v, want the errand it just handed back", done)
	}
}

func TestLeadingDelay(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		rest string
	}{
		{"in 10 minutes to check", 10 * time.Minute, "to check"},
		{"in 2 hours to eat", 2 * time.Hour, "to eat"},
		{"in 1 day to call", 24 * time.Hour, "to call"},
		{"in 30 s to blink", 30 * time.Second, "to blink"},
		// Not understood, so left alone and carried with no moment rather
		// than rejected: "the morning" is a real thing to be asked and this
		// has no way to work out when it is.
		{"in the morning to call", 0, "in the morning to call"},
		{"to water the plants", 0, "to water the plants"},
		{"in 0 minutes to nap", 0, "in 0 minutes to nap"},
	} {
		got, rest := leadingDelay(tc.in)
		if got != tc.want || rest != tc.rest {
			t.Errorf("leadingDelay(%q) = %v, %q; want %v, %q", tc.in, got, rest, tc.want, tc.rest)
		}
	}
}

func TestPlainly(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{90 * time.Second, "in a minute"},
		{10 * time.Minute, "in 10 minutes"},
		{time.Hour, "in an hour"},
		{3 * time.Hour, "in 3 hours"},
		{48 * time.Hour, "in 2 days"},
	} {
		if got := plainly(tc.d); got != tc.want {
			t.Errorf("plainly(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
