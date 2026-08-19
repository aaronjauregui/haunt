package ghost

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// Entities: what the memory index cannot answer.
//
// Every memory is a flat blob of text with a vector attached, which makes
// recall very good at "what does this remind you of" and incapable of "who
// is Sam". The question is about a *subject*, and a subject is not a point
// in embedding space — it is a name that appears in a dozen scattered
// exchanges, none of which is individually the answer. Cosine can only
// return the nearest one and hope.
//
// So consolidation, which is already reading clusters of old memories with
// a model, writes down the subjects it finds while it is there. That is the
// whole trick: the index is free, because the expensive part was happening
// anyway. What it buys is a second retrieval path that costs nothing at
// query time — no embedding, no ranking, just "this utterance says a name
// the ghost knows about" — and unlike recall it is *complete*: everything
// the ghost has concluded about Sam is in one place rather than spread
// across whichever exchanges happened to score well.
//
// Entities are kept small and quotable on purpose. This is not a knowledge
// base; it is the handful of things you would say about someone if asked.
const entityKeyPrefix = internalKeyPrefix + "entity/"

const (
	// maxEntities bounds the index. Past this the least-mentioned subject
	// gives way, on the same principle as memory's forgetting: a name that
	// came up once is not a subject.
	maxEntities = 120
	// maxEntityFacts is how much is kept about one subject. Six short
	// lines is what fits in a prompt beside everything else, and a subject
	// that needs more than six is really several.
	maxEntityFacts = 6
	// maxEntityNameChars rejects a "name" that is really a sentence — the
	// usual failure when a small model is asked to extract subjects.
	maxEntityNameChars = 48
	// maxEntityFactChars keeps one fact to about a line.
	maxEntityFactChars = 180
	// entitiesInContext is how many subjects may be described at once. A
	// message naming five people is rare; a model that hallucinated a
	// hundred entity names into one utterance is not, and this is the
	// backstop.
	entitiesInContext = 4
)

// Entity is a subject the ghost has concluded things about: a person, a
// project, a place. Short JSON tags, as with memories — a hundred of these
// share the store with the index.
type Entity struct {
	// Name is as written, for display; the store key holds the slug that
	// identifies it.
	Name string `json:"n"`
	// Kind is a free-form label from the consolidating model ("person",
	// "project", "place"). Not validated: it is shown, never branched on,
	// and a model that says "cat" is telling the truth.
	Kind string `json:"k,omitempty"`
	// Facts are what the ghost believes, newest last.
	Facts []string `json:"f"`
	// First and Last bracket when this subject was being talked about, and
	// Mentions is how many consolidations have touched it — the same
	// recency-and-frequency shape memory uses to decide what to keep.
	First    time.Time `json:"a"`
	Last     time.Time `json:"b"`
	Mentions int       `json:"c"`

	// key is the store slot, not part of the entity itself.
	key string `json:"-"`
}

// entitySlug is the stable identity behind a name: case and punctuation
// vary between one mention and the next ("Sam", "sam,", "SAM") and all of
// them are the same person.
func entitySlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// loadEntities reads the whole index — a linear scan over at most
// maxEntities keys, for the same reason memory brute-forces its ring.
func (g *Ghost) loadEntities() []Entity {
	all, err := g.store.All()
	if err != nil {
		return nil
	}
	out := make([]Entity, 0, len(all))
	for k, v := range all {
		if !strings.HasPrefix(k, entityKeyPrefix) {
			continue
		}
		var e Entity
		if json.Unmarshal([]byte(v), &e) != nil || e.Name == "" {
			continue
		}
		e.key = k
		out = append(out, e)
	}
	return out
}

// recordEntity merges what a consolidation learned into the index. Callers
// hold memMu: this runs alongside memory writes and shares their store.
func (g *Ghost) recordEntity(name, kind string, facts []string) {
	name = strings.TrimSpace(name)
	slug := entitySlug(name)
	if slug == "" || len([]rune(name)) > maxEntityNameChars {
		return
	}
	key := entityKeyPrefix + slug

	// A known subject keeps the name it was first written down under. Later
	// mentions arrive with whatever punctuation the sentence around them
	// had, and "sam," is not an improvement on "Sam".
	e := Entity{Name: name, key: key}
	for _, existing := range g.loadEntities() {
		if existing.key == key {
			e = existing
			break
		}
	}
	now := time.Now()
	if e.First.IsZero() {
		e.First = now
		// Room is made only for a subject that is actually new, so an
		// index at capacity keeps answering about the names it knows.
		if !g.makeRoomForEntity() {
			return
		}
	}
	e.Last = now
	e.Mentions++
	if kind != "" {
		e.Kind = kind
	}
	for _, f := range facts {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if len([]rune(f)) > maxEntityFactChars {
			f = string([]rune(f)[:maxEntityFactChars])
		}
		// A model asked about the same subject twice tends to volunteer the
		// same fact twice; that is agreement, not new information.
		if containsFold(e.Facts, f) {
			continue
		}
		e.Facts = append(e.Facts, f)
	}
	if len(e.Facts) > maxEntityFacts {
		e.Facts = e.Facts[len(e.Facts)-maxEntityFacts:]
	}
	if len(e.Facts) == 0 {
		return // nothing worth a slot
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	if err := g.store.Set(key, string(b)); err != nil {
		g.log.Warn(hlog.Memory, "recording entity %q: %v", name, err)
		return
	}
	g.log.Debug(hlog.Memory, "entity %q (%s): %d facts", e.Name, e.Kind, len(e.Facts))
}

// containsFold reports whether a fact is already present, case-insensitively.
func containsFold(facts []string, want string) bool {
	for _, f := range facts {
		if strings.EqualFold(f, want) {
			return true
		}
	}
	return false
}

// makeRoomForEntity evicts the weakest subject if the index is full,
// reporting whether there is now room. "Weakest" is few mentions and long
// ago — the same judgement memory makes about its own slots, and for the
// same reason: a name mentioned once in March is not a subject the ghost
// knows about.
//
// The Store has no delete, so eviction overwrites the slot with a value
// loadEntities refuses (an entity with no name) rather than removing it.
func (g *Ghost) makeRoomForEntity() bool {
	ents := g.loadEntities()
	if len(ents) < maxEntities {
		return true
	}
	weakest, low := ents[0], entityStrength(ents[0])
	for _, e := range ents[1:] {
		if s := entityStrength(e); s < low {
			weakest, low = e, s
		}
	}
	if err := g.store.Set(weakest.key, "{}"); err != nil {
		g.log.Warn(hlog.Memory, "forgetting entity %q: %v", weakest.Name, err)
		return false
	}
	g.log.Debug(hlog.Memory, "forgetting entity %q (%d mentions)", weakest.Name, weakest.Mentions)
	return true
}

// entityStrength scores a subject's claim on its slot: mentions, decayed by
// how long since anyone talked about it.
func entityStrength(e Entity) float64 {
	m := memory{Time: e.First, Last: e.Last, Uses: e.Mentions}
	return m.strength(time.Now())
}

// entityContext describes the subjects named in an utterance, for the
// system prompt. This is the half of memory that answers a question rather
// than supplying an association, and it is kept in its own section so the
// model can tell "what I know about Sam" from "what this reminds me of".
func (g *Ghost) entityContext(text string) string {
	ents := g.knownIn(text)
	if len(ents) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nWhat you already know about what was just mentioned:\n")
	for _, e := range ents {
		who := e.Name
		if e.Kind != "" {
			who += " (" + e.Kind + ")"
		}
		fmt.Fprintf(&b, "- %s: %s\n", who, strings.Join(e.Facts, "; "))
	}
	return b.String()
}

// knownIn returns the entities whose names appear in text, the
// most-talked-about first. Plain substring matching on word boundaries: the
// point of this path is that it costs nothing at query time, and a name is
// the one thing an utterance spells out.
func (g *Ghost) knownIn(text string) []Entity {
	lower := strings.ToLower(text)
	var hit []Entity
	for _, e := range g.loadEntities() {
		if len(e.Facts) > 0 && mentions(lower, strings.ToLower(e.Name)) {
			hit = append(hit, e)
		}
	}
	sort.SliceStable(hit, func(i, j int) bool {
		if hit[i].Mentions != hit[j].Mentions {
			return hit[i].Mentions > hit[j].Mentions
		}
		return hit[i].Last.After(hit[j].Last)
	})
	if len(hit) > entitiesInContext {
		hit = hit[:entitiesInContext]
	}
	return hit
}

// mentions reports whether name occurs in text as a whole word, so "Sam"
// does not match "same" and a project called "go" does not match every
// sentence containing "going".
func mentions(text, name string) bool {
	if name == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(text[i:], name)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(name)
		if !wordish(text, start-1) && !wordish(text, end) {
			return true
		}
		i = start + 1
	}
}

// wordish reports whether the byte at i is part of a word, treating
// out-of-range as not.
func wordish(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	r := rune(s[i])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// DecodeEntity parses a stored entity; used by the journal CLI.
func DecodeEntity(raw string) (Entity, error) {
	var e Entity
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return Entity{}, err
	}
	return e, nil
}

// EntityKeyPrefix exposes the entity index's key space to the CLI.
const EntityKeyPrefix = entityKeyPrefix
