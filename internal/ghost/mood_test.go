package ghost

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// moodRules scripts a turn on "feeling" that sets a mood and then says
// nothing, and silence for anything else.
func moodRules(text string) []fake.Rule {
	args, _ := json.Marshal(map[string]string{"text": text})
	return []fake.Rule{
		{Match: "feeling", AfterTool: "your mood is now", Calls: []fake.Call{{Name: silentName}}},
		{Match: "feeling", Calls: []fake.Call{{Name: "mood", Args: args}}},
		silent(""),
	}
}

func TestMoodIsOffered(t *testing.T) {
	g, b, srv := modelGhost(t, 0, moodRules("calm")...)
	g.markPrompted()
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "how are you feeling"})
	waitTurns(t, b, 1)
	for _, tool := range requests(t, srv)[0].Tools {
		if tool.Function.Name == "mood" {
			return
		}
	}
	t.Error("mood was not among the tools sent")
}

// The model sets a mood; it is in the context of the turn after, and in
// turnContext, and not in the balloon.
func TestMoodSetAndRendered(t *testing.T) {
	g, b, srv := modelGhost(t, 0, moodRules("a little wistful, the rain")...)
	g.markPrompted()
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "how are you feeling"})
	waitTurns(t, b, 1)
	if m, ok := g.mood(); !ok || m.Text != "a little wistful, the rain" {
		t.Fatalf("stored mood %+v %v", m, ok)
	}
	if ctx := g.turnContext(time.Now()); !strings.Contains(ctx, "\nYour mood: a little wistful, the rain") {
		t.Errorf("turn context: %q", ctx)
	}
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "hello again"})
	waitTurns(t, b, 2)
	var last request
	for _, r := range requests(t, srv) {
		if r.opens() {
			last = r
		}
	}
	if ctx := last.Messages[len(last.Messages)-1].Content; last.input() != "hello again" || !strings.Contains(ctx, "Your mood: a little wistful, the rain") {
		t.Errorf("the next turn's context (input %q): %q", last.input(), ctx)
	}
}

// A mood set yesterday is not rendered today; the same one set today is.
func TestMoodClearsWithTheDay(t *testing.T) {
	g, _, _ := modelGhost(t, 0)
	now := time.Date(2026, 10, 11, 8, 30, 0, 0, time.Local)
	if _, err := g.execMood("restless", now.Add(-10*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ctx := g.turnContext(now); strings.Contains(ctx, "mood") {
		t.Errorf("yesterday's mood is in the context: %q", ctx)
	}
	if _, err := g.execMood("restless", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ctx := g.turnContext(now); !strings.Contains(ctx, "Your mood: restless") {
		t.Errorf("today's mood is not in the context: %q", ctx)
	}
}

// Setting a mood replaces the last; one too long or empty is refused and
// leaves it as it was. Newlines are folded into one line.
func TestMoodReplaces(t *testing.T) {
	g, _, _ := modelGhost(t, 0)
	now := time.Now()
	for _, text := range []string{"sleepy", "bright\nand  curious"} {
		if _, err := g.execMood(text, now); err != nil {
			t.Fatal(err)
		}
	}
	ctx := g.turnContext(now)
	if !strings.Contains(ctx, "Your mood: bright and curious") || strings.Contains(ctx, "sleepy") {
		t.Errorf("turn context: %q", ctx)
	}
	for _, bad := range []string{"  ", strings.Repeat("x", maxMoodChars+1)} {
		if _, err := g.execMood(bad, now); err == nil {
			t.Errorf("a mood of %d characters was kept", len(bad))
		}
	}
	if m, _ := g.mood(); m.Text != "bright and curious" {
		t.Errorf("after refusals the mood is %q", m.Text)
	}
}
