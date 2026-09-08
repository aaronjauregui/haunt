package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aaronjauregui/haunt/internal/ai"
	"github.com/aaronjauregui/haunt/internal/hlog"
)

// Consolidation: the ghost sleeping.
//
// A raw index of 500 exchanges is a firehose at one end of its life and
// empty at the other. Everything that was ever said is in there with equal
// weight, none of it has been thought about, and the only thing that ever
// happened to it was arriving. What is missing is the pass that turns a
// month of small exchanges into the few sentences they were actually about
// — the thing sleep is for.
//
// So: when the user has gone away, take a cluster of related old memories,
// ask the model what they amount to, and write the answer down as a durable
// note. The journal is where it goes, because a consolidated memory is
// exactly what the journal has always held — things the ghost chose to
// keep — and because it means a person can read what their ghost concluded
// about them in `haunt journal` rather than having to take it on faith.
//
// Three properties this deliberately has.
//
// **It is scheduled on idle, not on a clock.** A cron pass would fire while
// someone was mid-sentence and take the ghost's one AI slot with it. Idle
// is both the honest time to do it and the time it costs nothing; the user
// coming back cancels it mid-request, because an answer nobody is waiting
// for is not worth finishing.
//
// **It is charged to an allowance the user controls**, and to its own
// rather than to curiosity's. Both ration things the user did not ask for,
// but they ration different costs: curiosity rations *attention*, and
// spending it here would mean a ghost that thought all night and had
// nothing left to say in the morning. This rations the bill.
//
// **It is a ratchet, not a lossy copy.** The raw entries are not deleted
// when they are folded into a summary — they are marked, which makes them
// the first slots the index gives up and leaves them recallable in the
// meantime. Consolidation therefore cannot lose anything that eviction was
// not about to take anyway.
const (
	// consolidateAfter is how old a memory must be before it is material.
	// Recent conversation is still live in the history window and the
	// journal, so summarising it would be summarising something the ghost
	// can already see.
	consolidateAfter = 48 * time.Hour
	// clusterMin is how many related memories it takes to be worth a
	// model call. Two exchanges are a coincidence.
	clusterMin = 3
	// clusterMax is how many go into one summary. Past a dozen the model
	// starts producing a table of contents rather than a conclusion.
	clusterMax = 12
	// clusterSim is how close to the seed a memory must be to belong with
	// it. Calibrated like recall's thresholds against nomic-embed-text,
	// where unrelated exchanges sit around 0.44: this is well clear of the
	// baseline, because a loose cluster summarises to "you talked about
	// several things", which is true of every cluster and worth nothing.
	clusterSim = 0.62
	// clusterSeeds bounds the search for a cluster. Every seed is a linear
	// scan of the index, and if forty of the oldest memories have nothing
	// around them there is nothing here to consolidate tonight.
	clusterSeeds = 40
	// consolidateDelay is how long after the user goes away before the
	// ghost starts. Idle fires on a threshold the user may have set to
	// seconds, and "away from the desk" is not the same as "stepped out of
	// the room to make tea".
	consolidateDelay = 5 * time.Minute
	// maxSummaryChars bounds what the model may write back. A summary
	// longer than the entries it replaces has failed at its one job.
	maxSummaryChars = 400
)

// sleeper holds the state of the pass: at most one runs at a time, and the
// user coming back cancels it.
type sleeper struct {
	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	timer   *time.Timer
}

// beginSleeping is the idle hook. It arms a delay rather than starting at
// once — see consolidateDelay — and does nothing if a pass is already
// under way.
func (g *Ghost) beginSleeping() {
	if g.sleep == nil || g.sleep.limit <= 0 {
		return
	}
	g.sleeper.mu.Lock()
	defer g.sleeper.mu.Unlock()
	if g.sleeper.running || g.sleeper.timer != nil {
		return
	}
	g.sleeper.timer = time.AfterFunc(g.consolidateDelay(), func() {
		g.go_(hlog.Memory, "consolidating", g.sleepOnce)
	})
}

// wakeUp is the active hook: the user is back, so stop thinking. A pass
// half-finished has cost a model call and produced nothing, which is the
// right trade — the alternative is holding the ghost's AI slot through the
// first thing the person says.
func (g *Ghost) wakeUp() {
	g.sleeper.mu.Lock()
	defer g.sleeper.mu.Unlock()
	if g.sleeper.timer != nil {
		g.sleeper.timer.Stop()
		g.sleeper.timer = nil
	}
	if g.sleeper.cancel != nil {
		g.sleeper.cancel()
	}
}

// consolidateDelay is the wait before a pass, overridable for tests.
func (g *Ghost) consolidateDelay() time.Duration {
	if g.opts.ConsolidateDelay > 0 {
		return g.opts.ConsolidateDelay
	}
	return consolidateDelay
}

// sleepOnce runs one consolidation, start to finish, if there is anything
// to consolidate and anything left to spend on it.
func (g *Ghost) sleepOnce() {
	ctx, ok := g.enterSleep()
	if !ok {
		return
	}
	defer g.leaveSleep()

	if err := g.consolidate(ctx); err != nil {
		g.log.Debug(hlog.Memory, "consolidation: %v", err)
	}
}

// enterSleep claims the single consolidation slot and returns the context
// the pass runs under.
func (g *Ghost) enterSleep() (context.Context, bool) {
	g.sleeper.mu.Lock()
	defer g.sleeper.mu.Unlock()
	g.sleeper.timer = nil
	if g.sleeper.running {
		return nil, false
	}
	ctx, cancel := context.WithCancel(g.sessionContext())
	g.sleeper.running, g.sleeper.cancel = true, cancel
	return ctx, true
}

func (g *Ghost) leaveSleep() {
	g.sleeper.mu.Lock()
	defer g.sleeper.mu.Unlock()
	if g.sleeper.cancel != nil {
		g.sleeper.cancel()
	}
	g.sleeper.running, g.sleeper.cancel = false, nil
}

// consolidate is one pass: find a cluster, ask what it amounts to, write
// the answer into the journal and the index, and mark the material folded.
func (g *Ghost) consolidate(ctx context.Context) error {
	if !g.memoryEnabled() {
		return fmt.Errorf("memory is not configured")
	}
	if g.aiSvc == nil || !g.aiSvc.LLMEnabled() {
		return fmt.Errorf("no LLM backend configured")
	}
	// The same rule imagine follows: a local model is the whole GPU for a
	// few seconds, which is a visible stutter in whatever is fullscreen.
	// Nobody asked for this one, so it can simply wait for another night.
	if g.desktop != nil && g.desktop.Fullscreen() {
		return fmt.Errorf("something is fullscreen")
	}

	cluster := g.findCluster(time.Now())
	if len(cluster) < clusterMin {
		return fmt.Errorf("nothing has settled enough to summarise")
	}
	// Spent only once there is real material: a night with nothing to think
	// about must not cost the week's allowance.
	ok, remaining := g.sleep.spend()
	if !ok {
		return fmt.Errorf("no consolidations left today")
	}
	g.log.Info(hlog.Memory, "consolidating %d memories (%d passes left today)",
		len(cluster), remaining)

	// Behind the ghost's one AI slot, as every other model call is: this
	// is unhurried work and it must not overtake something a person is
	// waiting for. It yields the slot the moment the user comes back,
	// because wakeUp cancels ctx and the request with it.
	g.aiMu.Lock()
	summary, err := g.askForSummary(ctx, cluster)
	g.aiMu.Unlock()
	if err != nil {
		return err
	}
	if summary.Note == "" {
		return fmt.Errorf("the model had nothing to say about it")
	}
	g.applySummary(cluster, summary)
	return nil
}

// findCluster picks the oldest group of related memories worth summarising.
// Brute force over the index, like recall: at this size the machinery to do
// it properly would cost more than the scan.
func (g *Ghost) findCluster(now time.Time) []memory {
	model := g.aiSvc.EmbedModel()
	cutoff := now.Add(-g.consolidateAge())

	var pool []memory
	for _, m := range g.loadMemories() {
		// Summaries are not material for further summarising: a summary of
		// summaries is where a memory turns into a rumour. Folded entries
		// have already been through this. And an entry from another
		// embedding model cannot be clustered at all until reembedStale has
		// rebuilt it.
		if m.Kind == kindSummary || m.Folded || m.Model != model || m.Text == "" {
			continue
		}
		if m.Time.After(cutoff) {
			continue
		}
		pool = append(pool, m)
	}
	if len(pool) < clusterMin {
		return nil
	}
	// Oldest first: the memories most in need of being turned into
	// something durable are the ones closest to being evicted.
	sort.SliceStable(pool, func(i, j int) bool { return pool[i].Time.Before(pool[j].Time) })

	vecs := make([][]float32, len(pool))
	for i, m := range pool {
		vecs[i] = decodeVec(m.Vec)
	}
	seeds := len(pool)
	if seeds > clusterSeeds {
		seeds = clusterSeeds
	}
	for i := 0; i < seeds; i++ {
		cluster := []memory{pool[i]}
		for j := range pool {
			if len(cluster) == clusterMax {
				break
			}
			if j != i && cosine(vecs[i], vecs[j]) >= clusterSim {
				cluster = append(cluster, pool[j])
			}
		}
		if len(cluster) >= clusterMin {
			return cluster
		}
	}
	return nil
}

// consolidateAge is how old material must be, overridable for tests.
func (g *Ghost) consolidateAge() time.Duration {
	if g.opts.ConsolidateAfter > 0 {
		return g.opts.ConsolidateAfter
	}
	return consolidateAfter
}

// summary is what one consolidation produced.
type summary struct {
	// Note is the durable sentence or two, in the ghost's own voice.
	Note string `json:"note"`
	// Entities are the subjects the model found while it was reading (see
	// entity.go). Free, because it was reading anyway.
	Entities []struct {
		Name  string   `json:"name"`
		Kind  string   `json:"kind"`
		Facts []string `json:"facts"`
	} `json:"entities"`
}

// consolidatePrompt is the instruction. Two things it is careful about.
// The note goes into the ghost's own journal, so it is written in the
// ghost's voice with the persona in context — a summary that reads like a
// report would be a foreign object in there. And it asks for what was
// *concluded*, not what was said: a list of the exchanges back is a smaller
// firehose rather than a memory.
const consolidatePrompt = `You are looking back over some of your older memories, the way someone does when they are half asleep. Below are exchanges and notes from a while ago that seem to belong together.

Write down what they amount to — the durable thing worth keeping once the individual conversations have faded. One or two sentences, in your own voice, as a journal entry. Not a list of what was said: what it means about the person you are talking to, or about you.

Then name the people, projects and places that came up, with what you now know about each.

Reply with JSON only, in this shape:
{"note": "...", "entities": [{"name": "...", "kind": "person|project|place|thing", "facts": ["...", "..."]}]}

If nothing here amounts to anything worth keeping, reply {"note": ""}.`

// askForSummary puts the cluster to the model.
func (g *Ghost) askForSummary(ctx context.Context, cluster []memory) (summary, error) {
	system := g.manifest.Persona.System
	if system == "" {
		system = "You are " + g.manifest.Ghost.Name + ", a small desktop companion."
	}
	var b strings.Builder
	for _, m := range cluster {
		fmt.Fprintf(&b, "- [%s] %s\n", m.Time.Format("Jan 2"), m.Text)
	}
	msgs := []ai.Message{
		{Role: "system", Content: system + "\n\n" + consolidatePrompt},
		{Role: "user", Content: b.String()},
	}
	// Silent: this is machinery, it wants the colder sampling, and there is
	// nobody to show a balloon to.
	raw, err := g.aiSvc.Chat(ctx, msgs, nil, ai.Options{Silent: true, Tag: "consolidate"})
	if err != nil {
		return summary{}, err
	}
	return parseSummary(raw), nil
}

// parseSummary reads the model's reply, forgivingly. Small local models
// wrap JSON in prose or a code fence about as often as they do not, and a
// consolidation thrown away over a stray backtick is a model call spent for
// nothing — so the object is cut out of whatever came back. A reply with no
// JSON in it at all is still usable: the prose *is* the note, which is all
// that was really being asked for.
func parseSummary(raw string) summary {
	raw = strings.TrimSpace(raw)
	if start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); start >= 0 && end > start {
		var s summary
		if json.Unmarshal([]byte(raw[start:end+1]), &s) == nil {
			s.Note = trimSummary(s.Note)
			return s
		}
	}
	// Not JSON. Anything that looks like an attempt at the format but did
	// not parse is discarded rather than pasted into the journal as prose.
	if strings.Contains(raw, "\"note\"") || strings.HasPrefix(raw, "{") {
		return summary{}
	}
	return summary{Note: trimSummary(raw)}
}

// trimSummary bounds and tidies what the model wrote.
func trimSummary(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxSummaryChars {
		s = strings.TrimSpace(string(r[:maxSummaryChars]))
	}
	return s
}

// applySummary commits a consolidation: the note into the journal, the
// summary into the index, the subjects into the entity index, and the
// material marked as folded.
//
// Order matters. The material is marked first, so the summary that replaces
// it is written into an index where those slots are already the weakest and
// its own arrival cannot evict something else. And the journal is written
// last, so a failure anywhere before it does not leave the person reading a
// conclusion the ghost does not actually hold.
func (g *Ghost) applySummary(cluster []memory, s summary) {
	g.foldMemories(cluster)

	// remember takes memMu itself, so it is called outside the marking.
	g.remember(kindSummary, s.Note)

	g.memMu.Lock()
	for _, e := range s.Entities {
		g.recordEntity(e.Name, e.Kind, FactInference, e.Facts)
	}
	g.memMu.Unlock()

	g.appendNote(s.Note)
	g.log.Info(hlog.Memory, "consolidated %d memories: %s", len(cluster), firstLine(s.Note))
}

// foldMemories marks a cluster as summarised. Each slot is re-read before
// it is rewritten, for the same reason reinforce does: a slot recycled
// underneath the pass belongs to something else now, and the pass has been
// away at a model for several seconds.
func (g *Ghost) foldMemories(cluster []memory) {
	g.memMu.Lock()
	defer g.memMu.Unlock()

	current := make(map[string]memory, maxMemories)
	for _, m := range g.loadMemories() {
		current[m.key] = m
	}
	for _, m := range cluster {
		cur, ok := current[m.key]
		if !ok || cur.Text != m.Text {
			continue
		}
		cur.Folded = true
		if err := g.putMemory(cur); err != nil {
			g.log.Warn(hlog.Memory, "folding %s: %v", cur.key, err)
			return
		}
	}
}
