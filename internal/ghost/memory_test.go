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

// TestRecallContextEmpty: no memories means no prompt section at all, so a
// ghost without an embedding backend gets its original system prompt.
func TestRecallContextEmpty(t *testing.T) {
	g := &Ghost{}
	if got := g.recallContext(nil); got != "" {
		t.Errorf("recallContext(nil) = %q, want empty", got)
	}
}
