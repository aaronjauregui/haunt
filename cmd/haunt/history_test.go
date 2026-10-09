package main

import (
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/hermes"
)

// The session's lines group into turns, each final reply takes its own
// reasons, and a turn older than the reasons' ring is still recorded, from
// the session alone.
func TestHistoryTurns(t *testing.T) {
	at := time.Unix(1789614336, 0)
	msgs := []hermes.Message{
		// The tail of a turn the window cut into.
		{Role: "assistant", Text: "…and that was that.", Time: at},
		{Role: "user", Text: hermes.Envelope("idle"), Time: at},
		{Role: "assistant", Text: hermes.NoReply, Time: at},
		{Role: "user", Text: "what's on my list?", Time: at},
		{Role: "assistant", Text: "Let me look.", Interim: true, Time: at},
		{Role: "assistant", Text: "Eggs, and the oven.", Time: at.Add(time.Second),
			Acted:  []hermes.Lookup{{Tool: "notes_read", Args: `{"file":"list"}`, Result: "eggs\noven"}},
			Looked: []hermes.Lookup{{Tool: "skill_view", Args: `{"name":"shopping-list"}`, Result: "…"}}},
	}
	// Only the last turn is still in the ring.
	traces := []ghost.Why{{Time: at.Add(2 * time.Second), User: "what's on my list?", Reply: "Eggs, and the oven.",
		Acted: []string{"mcp__haunt__notes_read"}, Window: ghost.WhyWindow{App: "foot", Title: "~"}}}

	turns := mergeTurns(msgs, traces)
	if len(turns) != 3 || turns[0].input != nil || turns[1].final() == nil || len(turns[2].replies) != 2 {
		t.Fatalf("turns %+v", turns)
	}
	if turns[1].whyKept || !turns[2].whyKept {
		t.Errorf("reasons matched to the wrong turns: %+v", turns)
	}

	idle := turns[1].record()
	if idle.ProvokedBy != "event" || idle.Input != "kind=idle" || !idle.Silent || idle.Reply != "" ||
		!idle.SelfPrompt || idle.Window != nil || !idle.Time.Equal(at) || idle.Acted == nil || idle.Looked == nil {
		t.Errorf("idle turn, older than the ring: %+v", idle)
	}
	list := turns[2].record()
	if list.ProvokedBy != "person" || list.Silent || list.SelfPrompt || list.Reply != "Let me look.\nEggs, and the oven." ||
		list.Window == nil || list.Window.App != "foot" || !list.Time.Equal(at.Add(time.Second)) {
		t.Errorf("list turn %+v", list)
	}
	failed := historyTurn{input: &hermes.Message{Role: "user", Text: "hello?", Time: at},
		replies: []hermes.Message{{Role: "assistant", Failed: true, Time: at}}}.record()
	if !failed.Failed || failed.Silent || failed.Reply != "" {
		t.Errorf("failed turn %+v", failed)
	}
	if len(list.Acted) != 1 || list.Acted[0] != (traceCall{"notes_read", `{"file":"list"}`, "eggs\noven"}) ||
		len(list.Looked) != 1 || list.Looked[0].Tool != "skill_view" {
		t.Errorf("list turn calls %+v %+v", list.Acted, list.Looked)
	}
}
