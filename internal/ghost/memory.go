package ghost

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Long-term memory: everything the ghost hears and everything it writes
// about itself is embedded and kept, so an utterance months later can pull
// back the relevant scrap regardless of how far outside the history window
// it fell. The journal (notes.go) is recency — the last 20 notes, always
// present. This is similarity — the few most relevant items from all of it.
//
// The index is a ring of KV slots rather than one blob: appending rewrites
// one key instead of the whole corpus, and the oldest slot is reused when
// the ring wraps, which bounds the store without needing a delete.
const (
	memKeyPrefix = internalKeyPrefix + "mem/"
	memCursorKey = internalKeyPrefix + "mem_cursor"

	maxMemories    = 500
	recallCount    = 5
	minSimilarity  = 0.35
	maxMemoryChars = 600
)

// memory is one remembered item and its embedding.
type memory struct {
	Time time.Time `json:"t"`
	// Kind is "note" (the ghost's own words about itself) or "exchange"
	// (something the user said and what it answered).
	Kind string `json:"k"`
	Text string `json:"x"`
	// Vec is base64 little-endian float32s: a third the size of the JSON
	// array and still one line in the store.
	Vec string `json:"v"`
}

// encodeVec packs an embedding for storage.
func encodeVec(v []float32) string {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// decodeVec unpacks a stored embedding.
func decodeVec(s string) []float32 {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// cosine is the similarity of two embeddings, 0 when either is empty or
// their dimensions disagree (which happens when the embedding model is
// swapped under an existing index).
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// memoryEnabled reports whether long-term memory is configured and allowed.
func (g *Ghost) memoryEnabled() bool {
	return g.aiSvc != nil && g.aiSvc.EmbedEnabled() && g.capabilityActive("llm")
}

// remember embeds text and stores it. Failures are logged and swallowed:
// memory is an enhancement, and a missing embedding backend must never cost
// the ghost its voice.
func (g *Ghost) remember(kind, text string) {
	if !g.memoryEnabled() {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(text) > maxMemoryChars {
		text = text[:maxMemoryChars]
	}
	vec, err := g.aiSvc.Embed(context.Background(), text)
	if err != nil {
		g.opts.Log(fmt.Sprintf("haunt: memory: embedding failed: %v", err))
		return
	}
	entry, err := json.Marshal(memory{Time: time.Now(), Kind: kind, Text: text, Vec: encodeVec(vec)})
	if err != nil {
		return
	}
	slot := g.nextMemorySlot()
	if err := g.store.Set(memKeyPrefix+strconv.Itoa(slot), string(entry)); err != nil {
		g.opts.Log(fmt.Sprintf("haunt: memory: storing: %v", err))
		return
	}
	if err := g.store.Set(memCursorKey, strconv.Itoa(slot+1)); err != nil {
		g.opts.Log(fmt.Sprintf("haunt: memory: cursor: %v", err))
	}
}

// nextMemorySlot returns the ring slot to write, wrapping at maxMemories so
// the oldest entry is the one overwritten.
func (g *Ghost) nextMemorySlot() int {
	raw, ok, err := storeGet(g.store, memCursorKey)
	if err != nil || !ok {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n % maxMemories
}

// loadMemories reads the whole index. It is a linear scan over at most
// maxMemories entries — brute force is the right amount of machinery for a
// corpus this size, and it keeps the store a plain KV.
func (g *Ghost) loadMemories() []memory {
	all, err := g.store.All()
	if err != nil {
		return nil
	}
	out := make([]memory, 0, len(all))
	for k, v := range all {
		if !strings.HasPrefix(k, memKeyPrefix) {
			continue
		}
		var m memory
		if json.Unmarshal([]byte(v), &m) != nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

// recall returns the memories most similar to text, most relevant first.
// skip holds text already present in the prompt (the recency journal), so
// recall spends its slots on things the ghost would otherwise have lost.
func (g *Ghost) recall(text string, skip map[string]bool) []memory {
	if !g.memoryEnabled() {
		return nil
	}
	mems := g.loadMemories()
	if len(mems) == 0 {
		return nil
	}
	vec, err := g.aiSvc.Embed(context.Background(), text)
	if err != nil {
		g.opts.Log(fmt.Sprintf("haunt: memory: recall failed: %v", err))
		return nil
	}

	type scored struct {
		m   memory
		sim float64
	}
	ranked := make([]scored, 0, len(mems))
	for _, m := range mems {
		if skip[m.Text] {
			continue
		}
		if sim := cosine(vec, decodeVec(m.Vec)); sim >= minSimilarity {
			ranked = append(ranked, scored{m, sim})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].sim > ranked[j].sim })
	if len(ranked) > recallCount {
		ranked = ranked[:recallCount]
	}
	out := make([]memory, len(ranked))
	for i, r := range ranked {
		out[i] = r.m
	}
	return out
}

// recallContext renders recalled memories for the system prompt. Kept
// visibly separate from the journal block so the model can tell "what I
// wrote down" from "what came back to me just now".
func (g *Ghost) recallContext(mems []memory) string {
	if len(mems) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nThings this reminds you of (from further back):\n")
	for _, m := range mems {
		fmt.Fprintf(&b, "- [%s] %s\n", m.Time.Format("Jan 2"), m.Text)
	}
	return b.String()
}
