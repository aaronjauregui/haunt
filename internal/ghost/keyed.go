package ghost

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/memory"
)

// Keyed context: lines from the ghost's memory that match what the person
// just said, put in the turn context without the model asking.
//
// `recall` is pull-only, and a small model does not reach for it when the
// person mentions something it was told a week ago; it answers from the
// transcript, which by then is a summary. So on a turn the person started,
// their words are looked up in the memory topics, and the best few lines
// ride in the turn context as "You remember, relevant now". Only the
// person's words key: an event's fields and a window's title are other
// words, and a line keyed by them would be the desktop steering what the
// ghost has in mind.
//
// A line that fires stays a few turns (keyedSticky), so it is still there
// for the reply to the reply, then cannot fire again for a while
// (keyedCooldown), so a subject the person keeps returning to does not pin
// the same line into every turn. Turns are the ghost's, every kind
// counted. The table is in memory only; a restart forgets it, which costs
// at most one line shown again.

const (
	// keyedHits is how many lines one person's turn may bring in.
	keyedHits = 3
	// keyedMinScore is the least BM25 score a line needs to fire. FTS5
	// floors a word's weight at almost nothing when it is in half the
	// lines or more, so the cutoff drops matches on words every topic has
	// and every match while the memory holds only a line or two (when the
	// topic index already shows it all). A single content word against a
	// dozen lines scores about 2, against three lines about 0.6.
	keyedMinScore = 0.5
	// keyedSticky is how many turns a fired line stays, the turn it fired
	// on included.
	keyedSticky = 3
	// keyedCooldown is how many turns after it leaves a line cannot fire
	// again.
	keyedCooldown = 10
)

// keyedHit is one line that fired, and the turn it fired on.
type keyedHit struct {
	topic, line string
	fired       int
	// rank is its place among the lines that fired with it, best first.
	rank int
}

// keyedState is the sticky and cooldown table: every line that fired and
// has not finished cooling down, keyed by topic and line.
type keyedState struct {
	mu   sync.Mutex
	turn int
	seen map[string]keyedHit
}

// keyedKey names a line in the table.
func keyedKey(topic, line string) string { return topic + "\x00" + line }

// step advances the table one turn and fires the hits that score well
// enough and are not already held or cooling down. hits is empty on a
// turn the person did not start.
func (s *keyedState) step(hits []memory.Hit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turn++
	if s.seen == nil {
		s.seen = map[string]keyedHit{}
	}
	for k, h := range s.seen {
		if s.turn >= h.fired+keyedSticky+keyedCooldown {
			delete(s.seen, k)
		}
	}
	rank := 0
	for _, h := range hits {
		k := keyedKey(h.Topic, h.Line)
		if _, held := s.seen[k]; held || h.Score < keyedMinScore {
			continue
		}
		s.seen[k] = keyedHit{topic: h.Topic, line: h.Line, fired: s.turn, rank: rank}
		rank++
	}
}

// held is the lines still sticky this turn, oldest firing first and best
// first within one.
func (s *keyedState) held() []keyedHit {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []keyedHit
	for _, h := range s.seen {
		if s.turn < h.fired+keyedSticky {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].fired != out[j].fired {
			return out[i].fired < out[j].fired
		}
		return out[i].rank < out[j].rank
	})
	return out
}

// line is the turn context's line for the held lines, empty when none is.
func (s *keyedState) line() string {
	held := s.held()
	if len(held) == 0 {
		return ""
	}
	parts := make([]string, len(held))
	for i, h := range held {
		parts[i] = strings.TrimRight(h.line, ". ") + " (" + h.topic + ")"
	}
	return "You remember, relevant now: " + strings.Join(parts, "; ") + "."
}

// keyedTable is g's table. The brain steps it and the turn context, which
// is the ghost's, renders it, so it lives on the ghost.
func (g *Ghost) keyedTable() *keyedState { return &g.keyed }

// keyedLine is the turn context's line for the lines memory brought in.
func (g *Ghost) keyedLine() string { return g.keyedTable().line() }

// keyed steps the ghost's table for turn t, looking the input up in memory
// when it is the person's words.
func (b *modelBrain) keyed(t *turn) {
	var hits []memory.Hit
	if q := keyedQuery(t.input); b.mem != nil && q != "" && !IsEnvelope(t.input) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var err error
		hits, err = b.mem.Recall(ctx, q, keyedHits)
		cancel()
		if err != nil {
			b.g.log.Warn(hlog.Memory, "looking up what they said: %v", err)
		}
	}
	b.g.keyedTable().step(hits)
}

// keyedQuery is the person's words less the ones that say nothing about a
// subject. Recall matches any word, and against a small memory "the" or
// "you" in one line weighs as much as "apples" in another; a word of one
// or two letters never names a subject either.
func keyedQuery(input string) string {
	words := strings.FieldsFunc(strings.ToLower(input), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	})
	var keep []string
	for _, w := range words {
		w = strings.Trim(w, "'")
		if len([]rune(w)) < 3 || keyedStopwords[w] {
			continue
		}
		keep = append(keep, w)
	}
	return strings.Join(keep, " ")
}

var keyedStopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`about above after again all also and any are aren't because been
		before being below but can can't cannot could couldn't did didn't does doesn't doing don't down
		during each few for from further get got had hadn't has hasn't have haven't having her here hers
		herself him himself his how i'd i'll i'm i've into isn't it's its itself just let's like more most
		much mustn't myself nor not now off once only other ought our ours ourselves out over own really
		same she she'd she'll she's should shouldn't some such than that that's the their theirs them
		themselves then there there's these they they'd they'll they're they've thing things think this
		those through too under until very was wasn't way we'd we'll we're we've well were weren't what
		what's when when's where where's which while who who's whom why why's will with won't would
		wouldn't yeah yes you you'd you'll you're you've your yours yourself yourselves hey hello okay
		today tomorrow yesterday know want need going make maybe still even something anything`) {
		m[w] = true
	}
	return m
}()
