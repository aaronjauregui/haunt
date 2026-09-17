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
	// maxEntityFacts is how many *standing* facts are kept about one
	// subject. Six short lines is what fits in a prompt beside everything
	// else, and a subject that needs more than six is really several.
	//
	// It counts standing facts only, which is the point: if a correction
	// could push a true fact off the end, the cap would punish the person
	// for correcting the ghost, which is the failure revision exists to
	// fix.
	maxEntityFacts = 6
	// maxEntityFactSlots bounds the list as a whole, superseded facts
	// included. Without it "kept past the cap" is an unbounded write into a
	// store where every other quantity is fixed — correct one subject
	// twenty times and its row grows twenty entries, invisibly, until a
	// prompt gets long.
	//
	// So a subject costs a bounded amount and standing beliefs always win
	// the slots: a standing fact is never given up to make room for a
	// superseded one, and past this ceiling the oldest superseded fact is
	// dropped. That loses the head of a long correction chain before its
	// tail, which is the counter-intuitive half and is still right — the
	// original wrong belief has already done its work, since the person saw
	// it and corrected it. The cost is that the audit trail is bounded like
	// everything else here: "you used to think X" is answerable for recent
	// X and not for all X.
	maxEntityFactSlots = 12
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

// FactSource is where a fact came from. It is an *ordering* rather than a
// label, and the ordering is the whole reason the field exists.
type FactSource string

const (
	// FactInference is consolidation's reading of a cluster: the ghost
	// thinking about the person rather than speaking to them. It is the
	// zero value deliberately, so that every fact written before this
	// struct existed decodes as the weakest kind — which is what lets a
	// correction retire one without a migration pass.
	FactInference FactSource = ""
	// FactExchange is the ghost's own reply on the About path: a judgement
	// made while talking to the person.
	FactExchange FactSource = "x"
	// FactPerson is the person saying so, in as many words, through
	// `haunt correct`. The only authoritative source there is.
	FactPerson FactSource = "p"
)

// authority ranks sources. The single rule that reads it: a fact from the
// person is not superseded by a fact from anywhere else. The ghost does not
// talk itself back out of a correction.
func (s FactSource) authority() int {
	switch s {
	case FactPerson:
		return 2
	case FactExchange:
		return 1
	}
	return 0
}

// Fact is one thing the ghost believes about a subject.
//
// This was a bare string, and bare strings carry no time, no provenance and
// no state — so there was no way to say which fact was older than which
// correction, what one was concluded from, or that one had been retired and
// by what. Every mechanism in the memory design is additive with decay, and
// none of them had a path for *that is wrong*; this struct is what a path
// needs underneath it.
//
// Two states and no more. It stands, or it was superseded and by what. Not
// a confidence score: a number attached to a belief is not a belief, and it
// invites arithmetic on quantities that do not add.
type Fact struct {
	Text string `json:"t"`
	// At is when this was concluded, or when the person said it.
	At time.Time `json:"a,omitempty"`
	// From is where it came from; see FactSource.
	From FactSource `json:"s,omitempty"`
	// Src is the store key this was drawn from — the summary or the
	// exchange that produced it. A hint rather than a handle: the Store has
	// no delete, slots are recycled, and a key kept here for six months is
	// that hazard six months older, so anything acting on it must check it
	// against the entry it names first.
	//
	// Nothing populates it yet. Its only consumer would be demoting a
	// denied summary, which is deferred behind a measurement — see
	// docs/hermes-brain.md. The field goes in now on the strength of the migration
	// argument alone: a format field is cheap today and a migration across
	// installed ghosts later is what gets a direction deferred forever. If
	// that demotion is never built, this is the field to question first.
	Src string `json:"r,omitempty"`
	// ID identifies this fact within its subject, so By can point at one
	// without using a slice position — positions shift as the caps trim.
	ID int `json:"i,omitempty"`
	// Until is when this stopped standing. Zero while it stands.
	Until time.Time `json:"u,omitempty"`
	// By is the ID of the fact that retired this one.
	By int `json:"b,omitempty"`
}

// Standing reports whether this fact is still believed. Exported: the
// journal CLI renders retired facts differently from live ones.
func (f Fact) Standing() bool { return f.Until.IsZero() }

// normalize gives ids to facts that have none, which is every fact written
// before this struct existed: they decode from bare strings with ID zero,
// and a correction has to be able to name one. Ids are 1-based so that zero
// keeps meaning "unset", and this is idempotent, so it can run on every
// decode without caring whether it has run before.
func (e *Entity) normalize() {
	if e.NextFact == 0 {
		e.NextFact = 1
	}
	for i := range e.Facts {
		if e.Facts[i].ID == 0 {
			e.Facts[i].ID = e.NextFact
			e.NextFact++
		}
	}
}

// UnmarshalJSON accepts both shapes: the object above, and the bare string
// every fact was before this struct existed. That is the whole of the
// migration — an old index decodes as facts with no time, no provenance and
// no state, which is exactly what they had.
func (f *Fact) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		f.Text = s
		return nil
	}
	// A named type, so this does not recurse into itself.
	type plain Fact
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = Fact(v)
	return nil
}

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
	// Facts are what the ghost believes, newest last. Superseded ones stay
	// in the list — a ghost that silently erased what it used to believe
	// would be unauditable exactly where auditability matters most — and
	// are excluded from context by standingFacts.
	Facts []Fact `json:"f"`
	// NextFact is the id the next fact added to this subject will take.
	// Ids rather than positions, because the caps trim the list.
	NextFact int `json:"d,omitempty"`
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

// loadEntities returns the whole index — a linear scan over at most
// maxEntities subjects, for the same reason memory brute-forces its ring,
// and over the resident copy for the same reason it does (see index.go).
func (g *Ghost) loadEntities() []Entity {
	if err := g.index.ensure(g.store); err != nil {
		g.log.Warn(hlog.Store, "reading the entity index: %v", err)
		return nil
	}
	return g.index.entities()
}

// clone copies a subject deeply enough to hand out. Facts is the reason:
// recordEntity appends to it, and an append with spare capacity would write
// into the resident copy's array before the store had accepted anything.
func (e Entity) clone() Entity {
	e.Facts = append([]Fact(nil), e.Facts...)
	return e
}

// recordEntity merges what a consolidation learned into the index. Callers
// hold memMu: this runs alongside memory writes and shares their store.
//
// src says who is speaking, and it is not decoration — see addFacts, which
// refuses to re-file something the person has already retired.
func (g *Ghost) recordEntity(name, kind string, src FactSource, facts []string) {
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
	e.addFacts(src, now, facts)
	e.trimFacts()
	if len(e.Facts) == 0 {
		return // nothing worth a slot
	}
	if err := g.putEntity(e); err != nil {
		g.log.Warn(hlog.Memory, "recording entity %q: %v", name, err)
		return
	}
	g.log.Debug(hlog.Memory, "entity %q (%s): %d facts", e.Name, e.Kind, len(e.Facts))
}

// addFacts appends what a source is offering, minus what is already there
// and minus what the person has already thrown out.
func (e *Entity) addFacts(src FactSource, now time.Time, facts []string) {
	e.normalize()
	for _, text := range facts {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if len([]rune(text)) > maxEntityFactChars {
			text = string([]rune(text)[:maxEntityFactChars])
		}
		if e.blocks(src, text) {
			continue
		}
		e.Facts = append(e.Facts, Fact{
			Text: text,
			At:   now,
			From: src,
			ID:   e.NextFact,
		})
		e.NextFact++
	}
}

// blocks reports whether a fact should not be added at all.
//
// Two reasons, and the second is the interesting one. A model asked about
// the same subject twice tends to volunteer the same fact twice, and that
// is agreement rather than news. And a fact the *person* has retired is not
// re-filed by anything with less authority than the person: without this,
// the ghost concludes something, is corrected, and files the same
// conclusion again on the next pass — which is the whole failure revision
// exists to prevent, reachable inside the entity index without going near
// the memory index at all.
//
// This is the cheap, non-speculative part of "a denial has to reach the
// material". The expensive parts — demoting a denied summary, telling
// consolidation — wait on a measurement; this one does not, because it
// costs a string compare against a list of at most twelve.
func (e *Entity) blocks(src FactSource, text string) bool {
	for _, f := range e.Facts {
		if !strings.EqualFold(f.Text, text) {
			continue
		}
		if f.Standing() {
			return true
		}
		// Retired. Only something at least as authoritative as whatever
		// retired it may raise it again.
		if src.authority() < e.retiredBy(f).authority() {
			return true
		}
	}
	return false
}

// retiredBy returns the source of the fact that superseded f, or f's own
// source if that fact has since been trimmed away.
func (e *Entity) retiredBy(f Fact) FactSource {
	for _, c := range e.Facts {
		if c.ID == f.By {
			return c.From
		}
	}
	return f.From
}

// trimFacts enforces both caps: at most maxEntityFacts standing, and at
// most maxEntityFactSlots in all. Standing facts are never given up to make
// room for superseded ones.
func (e *Entity) trimFacts() {
	// Oldest standing first, until six are left.
	for standing := e.countStanding(); standing > maxEntityFacts; standing-- {
		for i, f := range e.Facts {
			if f.Standing() {
				e.Facts = append(e.Facts[:i], e.Facts[i+1:]...)
				break
			}
		}
	}
	// Then oldest superseded first, until the list as a whole fits.
	for len(e.Facts) > maxEntityFactSlots {
		cut := -1
		for i, f := range e.Facts {
			if !f.Standing() {
				cut = i
				break
			}
		}
		if cut < 0 {
			break // all standing, and standing is already capped below this
		}
		e.Facts = append(e.Facts[:cut], e.Facts[cut+1:]...)
	}
}

// countStanding is how many of a subject's facts are still believed.
func (e *Entity) countStanding() int {
	n := 0
	for _, f := range e.Facts {
		if f.Standing() {
			n++
		}
	}
	return n
}

// StandingFacts returns the facts still believed, oldest first.
func (e Entity) StandingFacts() []Fact {
	out := make([]Fact, 0, len(e.Facts))
	for _, f := range e.Facts {
		if f.Standing() {
			out = append(out, f)
		}
	}
	return out
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
	if err := g.dropEntity(weakest.key); err != nil {
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
		concluded, told := e.factsBySource()
		switch {
		case len(told) == 0:
			fmt.Fprintf(&b, "- %s: %s\n", who, strings.Join(concluded, "; "))
		case len(concluded) == 0:
			fmt.Fprintf(&b, "- %s — they told you: %s\n", who, strings.Join(told, "; "))
		default:
			fmt.Fprintf(&b, "- %s: %s — and they told you: %s\n",
				who, strings.Join(concluded, "; "), strings.Join(told, "; "))
		}
	}
	return b.String()
}

// factsBySource splits a subject's standing facts into what the ghost
// worked out and what the person said.
//
// Blocking supersession is only half of the ordering, and this is the other
// half. It is enforced when a fact is *written*, which does nothing at the
// moment a prompt is built: handed one undifferentiated list, the model
// cannot tell which line the person authored and weighs them equally —
// which is the behaviour the ordering exists to prevent, surviving one
// layer down.
//
// It matters most because supersession is blocked but appending is not, so
// a subject can hold a person's fact and a later inference that flatly
// contradict each other, both standing, both printed. As two anonymous
// clauses the model picks one. As "what you know" and "what they told you"
// it has what a person would have had.
//
// The distinction stops there. Not the provenance value, not the store key,
// not a date on every line — only what you were told against what you
// worked out, which is the only part of the ordering the model has any
// business acting on.
func (e Entity) factsBySource() (concluded, told []string) {
	for _, f := range e.Facts {
		if !f.Standing() {
			continue
		}
		if f.From == FactPerson {
			told = append(told, f.Text)
		} else {
			concluded = append(concluded, f.Text)
		}
	}
	return concluded, told
}

// knownIn returns the entities whose names appear in text, the
// most-talked-about first. Plain substring matching on word boundaries: the
// point of this path is that it costs nothing at query time, and a name is
// the one thing an utterance spells out.
func (g *Ghost) knownIn(text string) []Entity {
	lower := strings.ToLower(text)
	var hit []Entity
	for _, e := range g.loadEntities() {
		if e.countStanding() > 0 && mentions(lower, strings.ToLower(e.Name)) {
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
	e.normalize()
	return e, nil
}

// EntityKeyPrefix exposes the entity index's key space to the CLI.
const EntityKeyPrefix = entityKeyPrefix
