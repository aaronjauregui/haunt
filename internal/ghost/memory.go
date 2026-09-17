package ghost

import (
	"context"
	"encoding/base64"
	"encoding/binary"
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
// The index is a bounded set of KV slots rather than one blob: appending
// rewrites one key instead of the whole corpus, and a full index reuses a
// slot rather than growing, which bounds the store without needing a
// delete. Which slot is the forgetting question, and it is answered by
// strength (below) rather than by arrival order.
const (
	memKeyPrefix = internalKeyPrefix + "mem/"
	memCursorKey = internalKeyPrefix + "mem_cursor"

	maxMemories    = 500
	recallCount    = 5
	maxMemoryChars = 600
	// summaryRecallCount is how many consolidated summaries may ride along
	// with the raw entries. Fewer, because each one stands for a dozen
	// exchanges and two of them is already most of a system prompt.
	summaryRecallCount = 2

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

// The kinds of thing the index holds.
const (
	// kindExchange is something the user said and what the ghost answered.
	kindExchange = "exchange"
	// kindNote is the ghost's own words about itself (notes.go).
	kindNote = "note"
	// kindSummary is what consolidation left behind: one durable note
	// standing for a cluster of older entries (consolidate.go).
	kindSummary = "summary"
)

// Forgetting has a shape. Ring position is arrival order, which is the one
// thing that says nothing about whether a memory is worth keeping: a remark
// made once in March and never returned to should fade, and a thing that
// keeps coming back should not, however old it is. So a full index gives up
// its *weakest* slot, where strength is recency of last relevance decayed
// over time and lifted by how often the memory has actually been useful.
const (
	// forgetHalfLife is how long an untouched memory takes to lose half its
	// claim on a slot. A month: long enough that a fortnight away from the
	// machine costs nothing, short enough that a year-old aside does not
	// outlive a habit.
	forgetHalfLife = 30 * 24 * time.Hour
	// summaryWeight is how much harder a consolidated summary is to forget
	// than a raw entry. It is the distillation of a dozen of them, so
	// losing it loses more; this is what makes consolidation a *ratchet*
	// rather than a lossy copy.
	summaryWeight = 4
	// consolidatedWeight is what a raw entry keeps once its substance has
	// been folded into a summary. Not zero — the detail is still worth
	// recalling while there is room for it — but it is now the first thing
	// the index gives up, which is what "let the raw entries age out"
	// means when the Store has no delete.
	consolidatedWeight = 0.15
)

// memory is one remembered item and its embedding.
type memory struct {
	Time time.Time `json:"t"`
	// Kind is one of kindNote, kindExchange or kindSummary.
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

	// Uses is how many times this memory has been recalled, and Last is
	// when it last was. Together with Time they are the whole of
	// forgetting: something that keeps proving relevant keeps its slot.
	Uses int       `json:"u,omitempty"`
	Last time.Time `json:"l,omitempty"`
	// Folded says a consolidation pass has already put this entry's
	// substance into a summary, so it is the first thing the index gives up
	// when it needs room.
	Folded bool `json:"f,omitempty"`

	// key is the store slot this came from, so an entry can be rewritten
	// in place. Not persisted — it is the name of the thing, not part of
	// it.
	key string `json:"-"`
}

// touched is when this memory last mattered: the last recall if there has
// been one, otherwise when it was written.
func (m memory) touched() time.Time {
	if m.Last.IsZero() {
		return m.Time
	}
	return m.Last
}

// strength is a memory's claim on its slot: recency of last relevance,
// halved every forgetHalfLife, multiplied by how often it has proved
// relevant. Frequency is a log rather than a count so a memory recalled
// forty times does not become unforgettable, and the multipliers for
// summaries and folded entries are what make consolidation a ratchet.
func (m memory) strength(now time.Time) float64 {
	age := now.Sub(m.touched())
	if age < 0 {
		age = 0
	}
	s := math.Pow(0.5, float64(age)/float64(forgetHalfLife))
	s *= 1 + math.Log1p(float64(m.Uses))
	switch {
	case m.Kind == kindSummary:
		s *= summaryWeight
	case m.Folded:
		s *= consolidatedWeight
	}
	return s
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
	// On hermes the belief store is the one memory the driver keeps, and it
	// needs no embeddings: it is written by the person and read by name.
	if g.onHermes {
		return g.capabilityActive("llm")
	}
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
	key, cursor := g.allocMemorySlot()
	if err := g.putMemory(memory{
		Time:  time.Now(),
		Kind:  kind,
		Text:  text,
		Vec:   encodeVec(vec),
		Model: g.aiSvc.EmbedModel(),
		key:   key,
	}); err != nil {
		g.log.Warn(hlog.Memory, "storing: %v", err)
		return
	}
	if cursor >= 0 {
		g.setMemoryCursor(cursor)
	}
}

// allocMemorySlot chooses the store key a new memory goes into, and the
// cursor value to record afterwards (-1 when there is nothing to record).
//
// While the index is filling, that is simply the next unused slot. Once it
// is full the cursor stops meaning anything and the weakest memory gives up
// its slot instead — the whole of "forgetting with a shape". Callers hold
// memMu, so the read and the write it leads to cannot interleave with
// another writer's.
func (g *Ghost) allocMemorySlot() (key string, cursor int) {
	mems := g.loadMemories()
	if len(mems) < maxMemories {
		slot := g.nextMemorySlot()
		return memKeyPrefix + strconv.Itoa(slot), slot + 1
	}
	weakest := mems[0]
	now := time.Now()
	low := weakest.strength(now)
	for _, m := range mems[1:] {
		if s := m.strength(now); s < low {
			weakest, low = m, s
		}
	}
	g.log.Debug(hlog.Memory, "forgetting %q (strength %.3f, %d uses)",
		firstLine(weakest.Text), low, weakest.Uses)
	return weakest.key, -1
}

// nextMemorySlot returns the next unused slot while the index is filling.
func (g *Ghost) nextMemorySlot() int {
	if err := g.index.ensure(g.store); err != nil {
		g.log.Warn(hlog.Store, "reading the memory index: %v", err)
		return 0
	}
	return g.index.nextSlot()
}

// firstLine trims a memory to something a log line can hold.
func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	if len(text) > 60 {
		text = text[:60] + "…"
	}
	return text
}

// loadMemories returns the whole index. Recall is a linear scan over at
// most maxMemories entries — brute force is the right amount of machinery
// for a corpus this size — and the scan is over the resident copy, so it
// costs no read at all. See index.go for why the ring is held rather than
// re-derived from the store on every use.
func (g *Ghost) loadMemories() []memory {
	if err := g.index.ensure(g.store); err != nil {
		g.log.Warn(hlog.Store, "reading the memory index: %v", err)
		return nil
	}
	return g.index.memories()
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
	if err := g.putMemory(m); err != nil {
		g.log.Warn(hlog.Memory, "rewriting %s: %v", m.key, err)
		return false
	}
	return true
}

// recall returns the memories most similar to text, most relevant first.
// skip holds text already present in the prompt (the recency journal), so
// recall spends its slots on things the ghost would otherwise have lost.
func (g *Ghost) recall(text string, skip map[string]bool) []memory {
	picked := g.recallScored(text, skip)
	out := make([]memory, 0, len(picked))
	for _, p := range picked {
		out = append(out, p.m)
	}
	return out
}

// recallScored is recall keeping each memory's similarity, so the driver
// can say afterwards not only what came back but how strongly.
func (g *Ghost) recallScored(text string, skip map[string]bool) []scored {
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
	var raw, summaries []scored
	for _, m := range mems {
		// Entries from another model are not comparable and would score
		// plausible nonsense when the dimensions happen to match. They sit
		// out until reembedStale has rebuilt them.
		if skip[m.Text] || m.Model != model {
			continue
		}
		sc := scored{m, cosine(vec, decodeVec(m.Vec))}
		if m.Kind == kindSummary {
			summaries = append(summaries, sc)
		} else {
			raw = append(raw, sc)
		}
	}
	// Two pools, not one ranked list. A consolidated summary is denser and
	// more abstract than the exchanges it came from, so it scores higher
	// against everything; dropped into one pool it would win every slot on
	// generality rather than relevance, and the concrete detail that makes
	// recall worth having would never get in.
	out := pickRelevantScored(summaries, summaryTuning)
	out = append(out, pickRelevantScored(raw, rawTuning)...)
	picked := make([]memory, 0, len(out))
	for _, o := range out {
		picked = append(picked, o.m)
	}
	g.reinforceLater(picked)
	return out
}

// reinforceLater records, off the conversation's path, that these memories
// came back. This is the frequency half of forgetting: a memory that keeps
// proving relevant keeps its slot, and one that surfaced once and never
// again gives it up.
func (g *Ghost) reinforceLater(mems []memory) {
	if len(mems) == 0 {
		return
	}
	g.go_(hlog.Memory, "reinforcing recalled memories", func() { g.reinforce(mems) })
}

// reinforce bumps the use count and recall time of each memory. Under
// memory's own lock, and each slot is re-read before it is rewritten: a
// slot recycled underneath us belongs to something else now, and writing
// the old entry back would resurrect what the index had decided to forget.
func (g *Ghost) reinforce(mems []memory) {
	g.memMu.Lock()
	defer g.memMu.Unlock()

	current := make(map[string]memory, maxMemories)
	for _, m := range g.loadMemories() {
		current[m.key] = m
	}
	now := time.Now()
	for _, m := range mems {
		cur, ok := current[m.key]
		if !ok || cur.Text != m.Text {
			continue
		}
		cur.Uses++
		cur.Last = now
		if err := g.putMemory(cur); err != nil {
			g.log.Warn(hlog.Memory, "reinforcing %s: %v", cur.key, err)
			return
		}
	}
}

// scored is a memory and its similarity to the utterance being answered.
type scored struct {
	m   memory
	sim float64
}

// tuning is one selection path's thresholds. There are two, because raw
// exchanges and consolidated summaries do not score alike (see
// summaryTuning).
type tuning struct {
	minStandout   float64
	standoutShare float64
	strongMatch   float64
	max           int
}

// rawTuning selects among exchanges and journal notes: the numbers measured
// against nomic-embed-text and documented on the constants above.
var rawTuning = tuning{minStandout, standoutShare, strongMatch, recallCount}

// summaryTuning selects among consolidated summaries. Three things differ.
// A summary is abstract, so it scores respectably against almost any
// utterance and needs a wider standout to prove it is about *this* one. The
// summary pool is small — usually below distributionMin — so in practice
// the absolute is what decides, and it is set above strongMatch for the
// same reason. And fewer get in, because each stands for a dozen exchanges.
//
// These are calibrated the same way as the raw numbers and want
// recalibrating alongside them if the embedding model changes.
var summaryTuning = tuning{0.18, 0.6, 0.62, summaryRecallCount}

// pickRelevant chooses which scored memories are worth the model's
// attention: nothing at all unless the best clearly stands out from the
// pool's own baseline, then everything on the shoulder below it.
func pickRelevant(ranked []scored, t tuning) []memory {
	picked := pickRelevantScored(ranked, t)
	out := make([]memory, 0, len(picked))
	for _, p := range picked {
		out = append(out, p.m)
	}
	return out
}

// pickRelevantScored is pickRelevant keeping the similarity that got each
// memory in. Only `haunt history -why` wants that — the prompt does not —
// but it has to be the same selection, not a re-derivation, or the account
// the ghost gives of itself would be a plausible story rather than what
// actually happened.
func pickRelevantScored(ranked []scored, t tuning) []scored {
	if len(ranked) == 0 {
		return nil
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].sim > ranked[j].sim })

	best := ranked[0].sim
	// Too few memories to have a baseline: nothing to stand out from, so
	// the score has to carry itself.
	cut := t.strongMatch
	if len(ranked) >= distributionMin {
		baseline := median(ranked)
		if best-baseline < t.minStandout && best < t.strongMatch {
			return nil // nothing here is distinctly about this
		}
		cut = baseline + t.standoutShare*(best-baseline)
	}

	out := make([]scored, 0, t.max)
	for _, r := range ranked {
		if r.sim < cut || len(out) == t.max {
			break
		}
		out = append(out, r)
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
		// A summary is labelled as one. It reads as a confident general
		// claim where the raw entries around it read as single moments, and
		// a model given both without the distinction will treat "you talk
		// about tea most mornings" as something that happened on Jan 2.
		when := m.Time.Format("Jan 2")
		if m.Kind == kindSummary {
			when += ", over many conversations"
		}
		// Continuation lines are indented so a multi-line exchange still
		// reads as one item of the list.
		fmt.Fprintf(&b, "- [%s] %s\n", when,
			strings.ReplaceAll(m.Text, "\n", "\n  "))
	}
	return b.String()
}
