package ghost

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
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

// TestMemoryRingWraps: the index is bounded, and wrapping overwrites the
// oldest slot rather than growing the store forever.
func TestMemoryRingWraps(t *testing.T) {
	s := &memStore{m: map[string]string{}}
	g := &Ghost{store: s}

	for i := 0; i < maxMemories+3; i++ {
		slot := g.nextMemorySlot()
		entry, _ := json.Marshal(memory{Kind: "note", Text: fmt.Sprintf("note %d", i)})
		if err := s.Set(memKeyPrefix+strconv.Itoa(slot), string(entry)); err != nil {
			t.Fatal(err)
		}
		if err := s.Set(memCursorKey, strconv.Itoa(slot+1)); err != nil {
			t.Fatal(err)
		}
	}

	mems := g.loadMemories()
	if len(mems) != maxMemories {
		t.Errorf("stored %d memories, want the ring capped at %d", len(mems), maxMemories)
	}
	texts := map[string]bool{}
	for _, m := range mems {
		texts[m.Text] = true
	}
	if texts["note 0"] {
		t.Error("oldest memory survived the wrap")
	}
	for _, want := range []string{"note 500", "note 501", "note 502"} {
		if !texts[want] {
			t.Errorf("newest memory %q missing", want)
		}
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
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := texts(pickRelevant(tc.in))
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
