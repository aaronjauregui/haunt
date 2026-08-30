package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/ai"
	"github.com/aaronjauregui/haunt/internal/hlog"
)

// fakeSleepBackend serves chat and embeddings. Vectors count vocabulary
// words, so texts sharing a subject cluster and texts that do not score
// zero against each other — enough to drive clustering without a model.
func fakeSleepBackend(t *testing.T, reply func(prompt string) string) (*httptest.Server, *[]string) {
	t.Helper()
	vocab := []string{"oolong", "tea", "compiler", "socks"}
	var asked []string
	mux := http.NewServeMux()
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var b strings.Builder
		for _, m := range req.Messages {
			b.WriteString(m.Content)
			b.WriteString("\n")
		}
		asked = append(asked, b.String())
		// SSE, because that is what the driver speaks.
		chunk, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{
				"delta": map[string]string{"content": reply(b.String())},
			}},
		})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	})
	mux.HandleFunc("/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		vec := make([]float32, len(vocab))
		lower := strings.ToLower(req.Input)
		for i, word := range vocab {
			vec[i] = float32(strings.Count(lower, word))
		}
		if vec[0] == 0 && vec[1] == 0 && vec[2] == 0 && vec[3] == 0 {
			vec[0] = 0.001 // never a zero vector; cosine would be 0 either way
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": vec}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &asked
}

// sleepGhost wires a ghost with both backends and a consolidation
// allowance, ready to be told to sleep.
func sleepGhost(t *testing.T, s Store, url string) *Ghost {
	t.Helper()
	cfg := &ai.Config{
		LLM:   ai.LLMConfig{BaseURL: url, Model: "test"},
		Embed: ai.EmbedConfig{BaseURL: url, Model: "test-embed"},
	}
	log := hlog.Discard()
	return &Ghost{
		store:    s,
		aiSvc:    ai.NewService(cfg, log),
		log:      log,
		sleep:    newAllowance(sleepKey, 2, s, log, hlog.Memory),
		manifest: &Manifest{Ghost: GhostSection{Capabilities: []string{"llm"}}},
	}
}

// clusterOfMemories is three old exchanges about one subject plus one that
// has nothing to do with them, embedded by the fake backend's rules.
func clusterOfMemories() []memory {
	old := time.Now().Add(-30 * 24 * time.Hour)
	mk := func(text string, vec []float32) memory {
		return memory{Kind: kindExchange, Text: text, Time: old,
			Vec: encodeVec(vec), Model: "test-embed"}
	}
	return []memory{
		mk("The user said: what oolong tea should I buy\nYou answered: the darker one", []float32{1, 1, 0, 0}),
		mk("The user said: is this oolong any good\nYou answered: it is tea, so yes", []float32{1, 1, 0, 0}),
		mk("The user said: more tea?\nYou answered: always oolong", []float32{1, 1, 0, 0}),
		mk("The user said: my socks are wet\nYou answered: unfortunate", []float32{0, 0, 0, 1}),
	}
}

// TestConsolidationWritesWhatItConcluded is the pass end to end: a cluster
// of old exchanges becomes one journal entry, one summary in the index, and
// an entity the ghost can now answer questions about — and the material it
// came from is marked rather than destroyed.
func TestConsolidationWritesWhatItConcluded(t *testing.T) {
	srv, asked := fakeSleepBackend(t, func(string) string {
		return `{"note": "They drink oolong most mornings and ask me about it.",
		         "entities": [{"name": "oolong", "kind": "thing", "facts": ["their usual tea"]}]}`
	})
	s := &memStore{m: map[string]string{}}
	fillIndex(t, s, clusterOfMemories()...)
	g := sleepGhost(t, s, srv.URL)

	if err := g.consolidate(context.Background()); err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	if len(*asked) != 1 {
		t.Fatalf("made %d model calls, want 1", len(*asked))
	}
	// The material, not the whole index: the unrelated memory must not be
	// dragged into a summary it has nothing to do with.
	if strings.Contains((*asked)[0], "socks") {
		t.Error("an unrelated memory was pulled into the cluster")
	}

	notes := g.loadNotes()
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "oolong most mornings") {
		t.Errorf("journal = %+v, want the conclusion in it", notes)
	}

	var summaries, folded, untouched int
	for _, m := range g.loadMemories() {
		switch {
		case m.Kind == kindSummary:
			summaries++
			if m.Text != notes[0].Text {
				t.Errorf("the index and the journal disagree: %q vs %q", m.Text, notes[0].Text)
			}
		case m.Folded:
			folded++
		default:
			untouched++
		}
	}
	if summaries != 1 {
		t.Errorf("summaries in the index = %d, want 1", summaries)
	}
	// Folded, not deleted: consolidation is a ratchet, and the detail stays
	// recallable until the index actually needs the room.
	if folded != 3 {
		t.Errorf("folded %d memories, want the 3 that were summarised", folded)
	}
	if untouched != 1 {
		t.Errorf("untouched = %d, want the 1 unrelated memory", untouched)
	}

	if got := g.entityContext("is there any oolong left"); !strings.Contains(got, "their usual tea") {
		t.Errorf("the pass learned nothing it can answer with:\n%s", got)
	}
}

// TestConsolidationLeavesRecentMemoriesAlone: recent conversation is still
// in the history window and the journal, so summarising it would be
// summarising something the ghost can already see.
func TestConsolidationLeavesRecentMemoriesAlone(t *testing.T) {
	srv, asked := fakeSleepBackend(t, func(string) string { return `{"note": "x"}` })
	s := &memStore{m: map[string]string{}}
	mems := clusterOfMemories()
	for i := range mems {
		mems[i].Time = time.Now().Add(-time.Hour)
	}
	fillIndex(t, s, mems...)
	g := sleepGhost(t, s, srv.URL)

	if err := g.consolidate(context.Background()); err == nil {
		t.Error("consolidated memories from an hour ago")
	}
	if len(*asked) != 0 {
		t.Errorf("spent %d model calls on nothing", len(*asked))
	}
}

// TestConsolidationSpendsNothingWithoutMaterial: a night with nothing to
// think about must not cost the week's allowance.
func TestConsolidationSpendsNothingWithoutMaterial(t *testing.T) {
	srv, _ := fakeSleepBackend(t, func(string) string { return `{"note": "x"}` })
	s := &memStore{m: map[string]string{}}
	fillIndex(t, s, clusterOfMemories()[3]) // the lone unrelated one
	g := sleepGhost(t, s, srv.URL)

	g.consolidate(context.Background())
	if spent := len(g.sleep.spends); spent != 0 {
		t.Errorf("allowance spent %d times on an empty pass", spent)
	}
}

// TestConsolidationStopsWhenTheAllowanceIsGone: this is an LLM call the
// user did not ask for, so it is rationed like one.
func TestConsolidationStopsWhenTheAllowanceIsGone(t *testing.T) {
	srv, asked := fakeSleepBackend(t, func(string) string {
		return `{"note": "something worth keeping about oolong"}`
	})
	s := &memStore{m: map[string]string{}}
	fillIndex(t, s, clusterOfMemories()...)
	g := sleepGhost(t, s, srv.URL)
	g.sleep.limit = 1

	if err := g.consolidate(context.Background()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	// Unfold the material so there is something to consolidate again, and
	// check it is the allowance rather than the absence of work that stops
	// the second pass.
	for _, m := range g.loadMemories() {
		if m.Folded {
			m.Folded = false
			g.putMemory(m)
		}
	}
	if err := g.consolidate(context.Background()); err == nil {
		t.Error("consolidated a second time with nothing left to spend")
	}
	if len(*asked) != 1 {
		t.Errorf("model calls = %d, want 1", len(*asked))
	}
}

// TestParseSummary: small local models wrap JSON in prose and code fences
// about as often as they do not, and a consolidation thrown away over a
// stray backtick is a model call spent for nothing.
func TestParseSummary(t *testing.T) {
	for _, tc := range []struct {
		name, in, wantNote string
		wantEntities       int
	}{{
		name:     "plain json",
		in:       `{"note": "they like oolong", "entities": [{"name": "Sam", "facts": ["x"]}]}`,
		wantNote: "they like oolong", wantEntities: 1,
	}, {
		name:     "fenced and prefaced",
		in:       "Sure! Here is the JSON:\n```json\n{\"note\": \"they like oolong\"}\n```",
		wantNote: "they like oolong",
	}, {
		name:     "bare prose is the note it was asked for",
		in:       "They drink oolong most mornings.",
		wantNote: "They drink oolong most mornings.",
	}, {
		name: "an explicit nothing",
		in:   `{"note": ""}`,
	}, {
		name: "a mangled attempt at the format is not prose",
		in:   `{"note": "they like oolong", `,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseSummary(tc.in)
			if got.Note != tc.wantNote {
				t.Errorf("note = %q, want %q", got.Note, tc.wantNote)
			}
			if len(got.Entities) != tc.wantEntities {
				t.Errorf("entities = %d, want %d", len(got.Entities), tc.wantEntities)
			}
		})
	}
}

// TestSummariesAreNotConsolidatedAgain: a summary of summaries is where a
// memory turns into a rumour.
func TestSummariesAreNotConsolidatedAgain(t *testing.T) {
	srv, _ := fakeSleepBackend(t, func(string) string { return `{"note": "x"}` })
	s := &memStore{m: map[string]string{}}
	mems := clusterOfMemories()
	for i := range mems {
		mems[i].Kind = kindSummary
	}
	fillIndex(t, s, mems...)
	g := sleepGhost(t, s, srv.URL)

	if got := g.findCluster(time.Now()); got != nil {
		t.Errorf("found a cluster of %d summaries to re-summarise", len(got))
	}
}
