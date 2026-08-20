package ghost

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

func whyGhost() *Ghost {
	return &Ghost{store: &memStore{m: map[string]string{}}, log: hlog.Discard()}
}

// The record is what makes the ghost interrogable, so it has to survive the
// round trip through the store intact.
func TestWhyRoundTrip(t *testing.T) {
	g := whyGhost()
	when := time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC)
	g.noteWhy(Why{
		Time:  when,
		User:  "what tea should I get",
		Reply: "the smoky one, like last time",
		Recalled: []Recalled{{
			Kind: kindExchange, When: when.Add(-90 * 24 * time.Hour),
			Sim: 0.71, Uses: 2, Text: "The user said: I liked the lapsang",
		}},
		Window:       WhyWindow{App: "firefox", Title: "tea shop"},
		System:       "You are a ghost.\n\nThings this reminds you of:\n- lapsang",
		HistoryTurns: 3,
	})

	raw, ok, err := storeGet(g.store, whyKey)
	if err != nil || !ok {
		t.Fatalf("nothing was stored (%v)", err)
	}
	got, err := DecodeWhy(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("stored %d traces, want 1", len(got))
	}
	w := got[0]
	if w.Reply != "the smoky one, like last time" || w.User != "what tea should I get" {
		t.Errorf("the exchange did not survive: %+v", w)
	}
	if len(w.Recalled) != 1 || w.Recalled[0].Sim != 0.71 || w.Recalled[0].Uses != 2 {
		t.Errorf("the recalled memory did not survive: %+v", w.Recalled)
	}
	if w.Window.App != "firefox" || w.Window.Title != "tea shop" {
		t.Errorf("the window did not survive: %+v", w.Window)
	}
	if !strings.Contains(w.System, "lapsang") {
		t.Errorf("the prompt did not survive: %q", w.System)
	}
}

// The ring is short on purpose — a full system prompt is kilobytes and the
// question is about what was just said.
func TestWhyIsBounded(t *testing.T) {
	g := whyGhost()
	for i := 0; i < whyKept+5; i++ {
		g.noteWhy(Why{Reply: "reply " + string(rune('a'+i))})
	}
	raw, _, _ := storeGet(g.store, whyKey)
	got, err := DecodeWhy(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != whyKept {
		t.Fatalf("kept %d traces, want the ring capped at %d", len(got), whyKept)
	}
	// The newest survive, not the oldest.
	if got[len(got)-1].Reply != "reply "+string(rune('a'+whyKept+4)) {
		t.Errorf("the newest trace is %q; the ring dropped the wrong end", got[len(got)-1].Reply)
	}
	if got[0].Reply == "reply a" {
		t.Error("the oldest trace survived a full ring")
	}
}

// A corrupt trace must cost the trace, not the conversation that produced it.
func TestWhySurvivesACorruptTrace(t *testing.T) {
	g := whyGhost()
	if err := g.store.Set(whyKey, "{not json"); err != nil {
		t.Fatal(err)
	}
	g.noteWhy(Why{Reply: "after the corruption"})
	raw, _, _ := storeGet(g.store, whyKey)
	got, err := DecodeWhy(raw)
	if err != nil {
		t.Fatalf("the trace did not recover: %v", err)
	}
	if len(got) != 1 || got[0].Reply != "after the corruption" {
		t.Errorf("got %+v, want just the new trace", got)
	}
}

// recalledTrace must report the selection recall actually made, scores and
// all — an account reconstructed afterwards would be a plausible story
// rather than what happened.
func TestRecalledTraceKeepsTheScores(t *testing.T) {
	picked := []scored{
		{m: memory{Kind: kindSummary, Text: "you drink tea most mornings", Uses: 4}, sim: 0.81},
		{m: memory{Kind: kindExchange, Text: "the lapsang one"}, sim: 0.62},
	}
	got := recalledTrace(picked)
	if len(got) != 2 {
		t.Fatalf("traced %d, want 2", len(got))
	}
	if got[0].Sim != 0.81 || got[0].Kind != kindSummary || got[0].Uses != 4 {
		t.Errorf("first entry lost its reasons: %+v", got[0])
	}
	if got[1].Sim != 0.62 {
		t.Errorf("second entry lost its score: %+v", got[1])
	}
	if recalledTrace(nil) != nil {
		t.Error("an empty recall should trace as nothing, not an empty list")
	}
}

// Whether the ghost could see the screen is part of the answer, and it must
// go blank when the capability does.
func TestWindowIsForgottenWhenNotWatching(t *testing.T) {
	g := whyGhost()
	if !g.currentWindow().Empty() {
		t.Error("a ghost that has never watched should know of no window")
	}
	g.noteWindow("foot", "vim ghost.go")
	if got := g.currentWindow(); got.App != "foot" || got.Title != "vim ghost.go" {
		t.Errorf("currentWindow = %+v", got)
	}
	g.noteWindow("", "")
	if !g.currentWindow().Empty() {
		t.Error("an ignored app should leave no window behind")
	}
}

// The stored shape is JSON in the ghost's own store, and the CLI decodes it
// without the ghost package's help beyond DecodeWhy.
func TestDecodeWhyRejectsGarbage(t *testing.T) {
	if _, err := DecodeWhy("[[["); err == nil {
		t.Error("garbage decoded as a trace")
	}
	var none []Why
	b, _ := json.Marshal(none)
	got, err := DecodeWhy(string(b))
	if err != nil || len(got) != 0 {
		t.Errorf("an empty trace should decode to nothing: %v %v", got, err)
	}
}
