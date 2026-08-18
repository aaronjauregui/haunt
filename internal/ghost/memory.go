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

	"github.com/aaronjauregui/haunt/internal/hlog"
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
	maxMemoryChars = 600

	// Selecting by absolute similarity does not work: embedding models
	// have a high baseline, and nomic-embed-text scores "my socks are wet"
	// at 0.44 against "what tea should I buy" — well above any floor loose
	// enough to admit real matches. What distinguishes a real match is that
	// it stands out from the rest of the index, so selection is relative to
	// the scores actually observed, which also survives changing models.
	//
	// minStandout is how far the best score must exceed the median before
	// anything is recalled at all; below it, nothing here is about the
	// utterance and the honest answer is silence.
	minStandout = 0.12
	// standoutShare keeps everything at least this far from the median
	// toward the best score — the shoulder of the distribution, not just
	// its peak, so a second genuinely relevant memory still gets in.
	standoutShare = 0.5
	// strongMatch is the one absolute threshold in the system, used only
	// where the distribution cannot answer: an index too small to have a
	// baseline, or one so homogeneous that nothing can stand out from it.
	// It is calibrated against nomic-embed-text, where a genuine match on
	// an exchange scores ~0.57-0.77 and an unrelated one ~0.40-0.50;
	// another embedding model will want another number, which is exactly
	// why the primary mechanism is relative instead.
	strongMatch = 0.55
	// distributionMin is how many scores it takes before a median means
	// anything.
	distributionMin = 4
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
	// Model is the embedding model that produced Vec. Vectors from
	// different models are not comparable, so this is what lets a model
	// change be noticed and repaired instead of silently emptying the
	// ghost's memory. Empty means an index written before this was
	// recorded, which is treated as stale.
	Model string `json:"m,omitempty"`

	// key is the store slot this came from, so an entry can be rewritten
	// in place. Not persisted — it is the name of the thing, not part of
	// it.
	key string `json:"-"`
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

// embedTimeout bounds an embedding call. Memory is best-effort by design —
// a missing embedding backend must never cost the ghost its voice — and a
// backend that *hangs* rather than failing is the same thing by another
// route: without a deadline, one wedged embeddings endpoint holds the
// ghost's single AI slot and it goes quiet altogether. Observed against a
// live Ollama whose chat endpoint answered in milliseconds while its
// embeddings endpoint never answered at all.
const embedTimeout = 10 * time.Second

// embedContext bounds an embedding call and follows the session, so
// shutting the ghost down does not wait for one either.
func (g *Ghost) embedContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(g.sessionContext(), embedTimeout)
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
	// One memory write at a time, in memory's own slot rather than the
	// conversation's.
	g.memMu.Lock()
	defer g.memMu.Unlock()

	ctx, cancel := g.embedContext()
	defer cancel()
	vec, err := g.aiSvc.Embed(ctx, text)
	if err != nil {
		g.log.Warn(hlog.Memory, "embedding failed: %v", err)
		return
	}
	entry, err := json.Marshal(memory{
		Time:  time.Now(),
		Kind:  kind,
		Text:  text,
		Vec:   encodeVec(vec),
		Model: g.aiSvc.EmbedModel(),
	})
	if err != nil {
		return
	}
	slot := g.nextMemorySlot()
	if err := g.store.Set(memKeyPrefix+strconv.Itoa(slot), string(entry)); err != nil {
		g.log.Warn(hlog.Memory, "storing: %v", err)
		return
	}
	if err := g.store.Set(memCursorKey, strconv.Itoa(slot+1)); err != nil {
		g.log.Warn(hlog.Memory, "cursor: %v", err)
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
		m.key = k
		out = append(out, m)
	}
	return out
}

// reembedStale rebuilds vectors written by a different embedding model.
// Vectors from two models are not comparable — same dimensions or not —
// so without this, changing models silently empties the ghost's memory
// until 500 new entries have pushed the old ones out of the ring. The
// source text is stored alongside every vector precisely so this is
// possible.
//
// Runs in the background after boot, one entry at a time in memory's own
// slot (memMu), so it
// interleaves with conversation rather than blocking it. Failures leave the
// entry stale for the next run to retry.
func (g *Ghost) reembedStale(ctx context.Context) {
	if !g.memoryEnabled() {
		return
	}
	model := g.aiSvc.EmbedModel()
	var stale []memory
	for _, m := range g.loadMemories() {
		if m.Model != model && m.Text != "" {
			stale = append(stale, m)
		}
	}
	if len(stale) == 0 {
		return
	}
	g.log.Info(hlog.Memory, "re-embedding %d entries for model %q", len(stale), model)

	done := 0
	for _, m := range stale {
		if ctx.Err() != nil {
			break
		}
		if !g.reembedOne(ctx, m, model) {
			break // backend trouble; the rest keep for next boot
		}
		done++
	}
	g.log.Info(hlog.Memory, "re-embedded %d/%d entries", done, len(stale))
}

// reembedOne rewrites a single entry's vector, reporting whether to
// continue. memMu is taken per entry, so a long migration interleaves with
// conversation instead of standing in front of it.
func (g *Ghost) reembedOne(ctx context.Context, m memory, model string) bool {
	g.memMu.Lock()
	defer g.memMu.Unlock()

	vec, err := g.aiSvc.Embed(ctx, m.Text)
	if err != nil {
		g.log.Warn(hlog.Memory, "re-embedding stopped: %v", err)
		return false
	}
	m.Vec, m.Model = encodeVec(vec), model
	entry, err := json.Marshal(m)
	if err != nil {
		return true // skip this one, keep going
	}
	if err := g.store.Set(m.key, string(entry)); err != nil {
		g.log.Warn(hlog.Memory, "rewriting %s: %v", m.key, err)
		return false
	}
	return true
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
	ctx, cancel := g.embedContext()
	defer cancel()
	vec, err := g.aiSvc.Embed(ctx, text)
	if err != nil {
		g.log.Warn(hlog.Memory, "recall failed: %v", err)
		return nil
	}

	model := g.aiSvc.EmbedModel()
	ranked := make([]scored, 0, len(mems))
	for _, m := range mems {
		// Entries from another model are not comparable and would score
		// plausible nonsense when the dimensions happen to match. They sit
		// out until reembedStale has rebuilt them.
		if skip[m.Text] || m.Model != model {
			continue
		}
		ranked = append(ranked, scored{m, cosine(vec, decodeVec(m.Vec))})
	}
	return pickRelevant(ranked)
}

// scored is a memory and its similarity to the utterance being answered.
type scored struct {
	m   memory
	sim float64
}

// pickRelevant chooses which scored memories are worth the model's
// attention: nothing at all unless the best clearly stands out from the
// index's own baseline, then everything on the shoulder below it.
func pickRelevant(ranked []scored) []memory {
	if len(ranked) == 0 {
		return nil
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].sim > ranked[j].sim })

	best := ranked[0].sim
	// Too few memories to have a baseline: nothing to stand out from, so
	// the score has to carry itself.
	cut := strongMatch
	if len(ranked) >= distributionMin {
		baseline := median(ranked)
		if best-baseline < minStandout && best < strongMatch {
			return nil // nothing here is distinctly about this
		}
		cut = baseline + standoutShare*(best-baseline)
	}

	out := make([]memory, 0, recallCount)
	for _, r := range ranked {
		if r.sim < cut || len(out) == recallCount {
			break
		}
		out = append(out, r.m)
	}
	return out
}

// median is the typical similarity in a sorted-descending ranking: the
// index's own baseline for "unrelated".
func median(sorted []scored) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2].sim
	}
	return (sorted[n/2-1].sim + sorted[n/2].sim) / 2
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
		// Continuation lines are indented so a multi-line exchange still
		// reads as one item of the list.
		fmt.Fprintf(&b, "- [%s] %s\n", m.Time.Format("Jan 2"),
			strings.ReplaceAll(m.Text, "\n", "\n  "))
	}
	return b.String()
}
