package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/ai"
	"github.com/aaronjauregui/haunt/internal/hlog"
)

func TestVectorRoundTrip(t *testing.T) {
	in := []float32{0, 1, -1, 0.125, 3.4e38}
	got := decodeVec(encodeVec(in))
	if len(got) != len(in) {
		t.Fatalf("length = %d, want %d", len(got), len(in))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("[%d] = %v, want %v", i, got[i], in[i])
		}
	}
	if decodeVec("not base64!") != nil {
		t.Error("garbage decoded to a vector")
	}
}

func TestCosine(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"scaled is identical in direction", []float32{1, 2, 3}, []float32{2, 4, 6}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposed", []float32{1, 0}, []float32{-1, 0}, -1},
		{"dimension mismatch is not a match", []float32{1, 0}, []float32{1, 0, 0}, 0},
		{"empty", nil, nil, 0},
		{"zero vector", []float32{0, 0}, []float32{1, 1}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cosine(tc.a, tc.b); got-tc.want > 1e-9 || tc.want-got > 1e-9 {
				t.Errorf("cosine = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMemoryIndexIsBounded: however long a ghost runs, the index reuses
// slots rather than growing the store forever, and what has just been
// written is in it. *Which* slot a full index gives up is the separate
// question forget_test.go asks.
func TestMemoryIndexIsBounded(t *testing.T) {
	s := &memStore{m: map[string]string{}}
	g := &Ghost{store: s, log: hlog.Discard()}

	now := time.Now()
	for i := 0; i < maxMemories+3; i++ {
		key, cursor := g.allocMemorySlot()
		entry, _ := json.Marshal(memory{
			Kind: kindNote, Text: fmt.Sprintf("note %d", i),
			// Ascending, so the newest entry is always the strongest and
			// the pass never immediately evicts what it just wrote.
			Time: now.Add(time.Duration(i) * time.Minute),
		})
		if err := s.Set(key, string(entry)); err != nil {
			t.Fatal(err)
		}
		if cursor >= 0 {
			if err := s.Set(memCursorKey, strconv.Itoa(cursor)); err != nil {
				t.Fatal(err)
			}
		}
	}

	mems := g.loadMemories()
	if len(mems) != maxMemories {
		t.Errorf("stored %d memories, want the index capped at %d", len(mems), maxMemories)
	}
	texts := map[string]bool{}
	for _, m := range mems {
		texts[m.Text] = true
	}
	for _, want := range []string{"note 500", "note 501", "note 502"} {
		if !texts[want] {
			t.Errorf("newest memory %q missing", want)
		}
	}
	if texts["note 0"] {
		t.Error("the weakest memory survived a full index")
	}
}

// TestPickRelevant drives the selection with similarity scores actually
// measured from nomic-embed-text (ollama, 768d) against a small index, so
// the thresholds are tuned to how a real model behaves rather than to how
// cosine ought to behave in principle.
func TestPickRelevant(t *testing.T) {
	score := func(sims ...float64) []scored {
		out := make([]scored, len(sims))
		for i, s := range sims {
			out[i] = scored{memory{Text: fmt.Sprintf("m%d", i)}, s}
		}
		return out
	}
	texts := func(mems []memory) []string {
		out := make([]string, len(mems))
		for i, m := range mems {
			out[i] = m.Text
		}
		return out
	}

	for _, tc := range []struct {
		name string
		in   []scored
		tune tuning
		want []string
	}{{
		// "what tea should I buy": the tea memories stand out; "my socks
		// are wet" at 0.436 scores above any usable absolute floor and
		// must still be excluded.
		name: "clear match takes the shoulder, not the pack",
		in:   score(0.620, 0.520, 0.436, 0.386, 0.335, 0.312, 0.281),
		want: []string{"m0", "m1"},
	}, {
		// "how is Biscuit doing": one memory is the answer.
		name: "single standout recalls alone",
		in:   score(0.631, 0.384, 0.383, 0.375, 0.374, 0.363, 0.315),
		want: []string{"m0"},
	}, {
		// "this build keeps failing": a lower best, but it still stands
		// clear of the baseline — absolute thresholds would have missed
		// this while admitting the socks above.
		name: "modest scores still count when they stand out",
		in:   score(0.488, 0.470, 0.350, 0.340, 0.329, 0.305, 0.303),
		want: []string{"m0", "m1"},
	}, {
		// "I'm going to bed": nothing in the index is about it, and the
		// scores are one undifferentiated clump.
		name: "flat distribution recalls nothing",
		in:   score(0.461, 0.455, 0.407, 0.365, 0.364, 0.344, 0.332),
		want: nil,
	}, {
		// Below distributionMin there is no baseline to stand out from,
		// so only an outright strong score gets in: measured against
		// nomic-embed-text, an unrelated exchange still scores ~0.44.
		name: "small index needs a strong match",
		in:   score(0.62, 0.44),
		want: []string{"m0"},
	}, {
		name: "small index with nothing strong recalls nothing",
		in:   score(0.498, 0.44),
		want: nil,
	}, {
		name: "empty index",
		in:   nil,
		want: nil,
	}, {
		name: "recall is capped",
		in:   score(0.9, 0.89, 0.88, 0.87, 0.86, 0.85, 0.2, 0.2, 0.2, 0.2, 0.2, 0.2, 0.2),
		want: []string{"m0", "m1", "m2", "m3", "m4"},
	}, {
		// A ghost that only ever discusses one subject has no baseline to
		// stand out from; a strong score has to carry itself.
		name: "homogeneous index still recalls its best",
		in:   score(0.65, 0.64, 0.62, 0.61, 0.60),
		want: []string{"m0", "m1"},
	}, {
		// The same flatness, but nothing scores well: still silence.
		name: "flat and weak stays silent",
		in:   score(0.40, 0.39, 0.38, 0.37, 0.36),
		want: nil,
	}, {
		// Summaries have their own path because they score high against
		// everything. A cluster of them at 0.58 — a match that would carry
		// a raw exchange — is the generality of the summary talking, not
		// relevance to the utterance.
		name: "summary pool ignores what would pass as a raw match",
		in:   score(0.58, 0.56),
		tune: summaryTuning,
		want: nil,
	}, {
		name: "a summary plainly about the utterance still gets in",
		in:   score(0.71, 0.55),
		tune: summaryTuning,
		want: []string{"m0"},
	}, {
		// Two is the ceiling however many summaries score well: each one
		// stands for a dozen exchanges.
		name: "summaries are capped tighter than raw entries",
		in:   score(0.90, 0.88, 0.86),
		tune: summaryTuning,
		want: []string{"m0", "m1"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			tune := tc.tune
			if tune.max == 0 {
				tune = rawTuning
			}
			got := texts(pickRelevant(tc.in, tune))
			if len(got) != len(tc.want) {
				t.Fatalf("recalled %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %s, want %s", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestRecallContextEmpty: no memories means no prompt section at all, so a
// ghost without an embedding backend gets its original system prompt.
func TestRecallContextEmpty(t *testing.T) {
	g := &Ghost{}
	if got := g.recallContext(nil); got != "" {
		t.Errorf("recallContext(nil) = %q, want empty", got)
	}
}

// fakeEmbedServer embeds text as a length-derived vector, tagged so the
// test can tell one "model" from another.
func fakeEmbedServer(t *testing.T, dims int, calls *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		atomic.AddInt32(calls, 1)
		vec := make([]float32, dims)
		for i := range vec {
			vec[i] = float32(len(req.Input) + i)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": vec}},
		})
	}))
}

// testGhost wires the minimum a Ghost needs for memory: a store, an AI
// service, a granted llm capability.
func testGhost(t *testing.T, store Store, embedURL, model string) *Ghost {
	t.Helper()
	cfg := &ai.Config{Embed: ai.EmbedConfig{BaseURL: embedURL, Model: model}}
	return &Ghost{
		store:    store,
		aiSvc:    ai.NewService(cfg, hlog.Discard()),
		opts:     Options{},
		log:      hlog.Discard(),
		manifest: &Manifest{Ghost: GhostSection{Capabilities: []string{"llm"}}},
	}
}

// TestReembedOnModelChange: changing the embedding model must not quietly
// cost the ghost its memories. Vectors from the old model are rebuilt from
// the stored text, and until they are, they sit out rather than scoring
// nonsense.
func TestReembedOnModelChange(t *testing.T) {
	var calls int32
	srv := fakeEmbedServer(t, 8, &calls)
	defer srv.Close()

	// An index written by an earlier model, plus one pre-dating model
	// tagging at all.
	s := &memStore{m: map[string]string{}}
	for i, m := range []memory{
		{Kind: "note", Text: "the user likes oolong", Vec: encodeVec([]float32{1, 2, 3}), Model: "old-model"},
		{Kind: "exchange", Text: "the compiler is angry", Vec: encodeVec([]float32{3, 2, 1}), Model: "old-model"},
		{Kind: "note", Text: "written before models were recorded", Vec: encodeVec([]float32{9, 9, 9})},
	} {
		entry, _ := json.Marshal(m)
		s.m[memKeyPrefix+strconv.Itoa(i)] = string(entry)
	}
	g := testGhost(t, s, srv.URL, "new-model")

	// Before repair, stale entries are invisible to recall — no scoring of
	// vectors whose meaning belongs to another model.
	if got := g.recall("tell me about tea", nil); len(got) != 0 {
		t.Errorf("stale entries were recalled: %v", got)
	}

	g.reembedStale(context.Background())

	mems := g.loadMemories()
	if len(mems) != 3 {
		t.Fatalf("loaded %d memories, want 3", len(mems))
	}
	for _, m := range mems {
		if m.Model != "new-model" {
			t.Errorf("memory %q still tagged %q", m.Text, m.Model)
		}
		if len(decodeVec(m.Vec)) != 8 {
			t.Errorf("memory %q kept a %d-dimension vector", m.Text, len(decodeVec(m.Vec)))
		}
	}
	// Three re-embeddings, plus the one recall query above.
	if n := atomic.LoadInt32(&calls); n != 4 {
		t.Errorf("embed calls = %d, want 4", n)
	}

	// A second boot has nothing to do.
	g.reembedStale(context.Background())
	if n := atomic.LoadInt32(&calls); n != 4 {
		t.Errorf("re-embedding repeated itself: %d calls", n)
	}
}

// TestReembedStopsOnBackendFailure: an unreachable backend leaves entries
// stale for the next boot rather than destroying them.
func TestReembedStopsOnBackendFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", 500)
	}))
	defer srv.Close()

	s := &memStore{m: map[string]string{}}
	entry, _ := json.Marshal(memory{Kind: "note", Text: "keep me", Vec: encodeVec([]float32{1}), Model: "old"})
	s.m[memKeyPrefix+"0"] = string(entry)

	g := testGhost(t, s, srv.URL, "new-model")
	g.reembedStale(context.Background())

	mems := g.loadMemories()
	if len(mems) != 1 || mems[0].Text != "keep me" || mems[0].Model != "old" {
		t.Errorf("failed re-embedding damaged the index: %+v", mems)
	}
}
