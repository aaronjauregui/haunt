package ghost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/memory"
)

// Memory: the ghost's own, in files the person can read and edit
// (internal/memory). Every line it keeps says where it came from, and the
// driver writes that, not the model: the source is what the turn had
// read when the line was written (provenance). Recall searches the topics
// and the conversation (transcript.go), so what was said a month ago is
// as reachable as what was saved. The diary (diary.go) is written here
// too, by the reflection turn alone (reflect.go).

func init() {
	register(verb{
		id: "remember",
		desc: "Keep one fact they told you about themselves, in your own words, under a topic (food, work…). " +
			"Not for a goal they drop: that is set_aside. " +
			"replaces: a line in the topic this one makes false (moved city, new job).",
		params: []string{"topic:string", "line:string", "replaces?:string"},
		run:    remember,
	})
	register(verb{
		id:     "forget",
		desc:   "Take back a line you remembered, when it is wrong or they ask you to. line is the line as it was saved.",
		params: []string{"topic:string", "line:string"},
		run:    forget,
	})
	register(verb{
		id:     "recall",
		desc:   "Search what you remember and what was said before. Returns the best matching lines with where they came from.",
		params: []string{"query:string"},
		run:    recall,
	})
	register(verb{
		id: "rewrite_notes",
		desc: "Replace your notes on yourself (about=self) or on the person (about=you) with text, " +
			"under 1500 bytes. They are in front of you on every turn.",
		params: []string{"about:string", "text:string"},
		run:    rewriteNotes,
	})
	register(diaryDecision{verb{
		id: diaryName,
		desc: "Write the entry in your diary for the day you are looking back on: day as 2026-10-11, " +
			"text in the first person, under 600 characters. Ends the turn.",
		params: []string{"day:string", "text:string"},
		ends:   true,
		run:    writeDiary,
	}})
}

// diaryDecision is offered on a reflection turn only: a day's entry is
// written once, looking back, not in the middle of the day it is about.
type diaryDecision struct{ verb }

func (diaryDecision) offeredTo(g *Ghost) bool {
	t := g.current()
	return t != nil && t.kind() == reflectKind
}

// writeDiary writes the entry for the day the reflection turn is about,
// whatever day the call names: text under diaryEntryLimit characters,
// through the same write gate and label as the notes. A day that already
// has an entry, the ghost's or one the person wrote, keeps it.
func writeDiary(_ context.Context, g *Ghost, t *turn, args json.RawMessage) (string, error) {
	var a struct {
		Day  string `json:"day"`
		Text string `json:"text"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if _, err := memoryOf(g); err != nil || g.mind.memDir == "" {
		return "", refuse("you have no diary to write in")
	}
	day := ""
	if t != nil {
		day = envelopeField(t.input, "day")
	}
	if _, err := time.ParseInLocation(dayForm, day, time.Local); err != nil {
		return "", refuse("the diary is written when you look back on a day, not now")
	}
	text := strings.TrimSpace(labelLine.ReplaceAllString(a.Text, ""))
	if text == "" {
		return "", fmt.Errorf("write what?")
	}
	if n := utf8.RuneCountInString(text); n > diaryEntryLimit {
		return "", fmt.Errorf("the entry is %d characters; keep it under %d", n, diaryEntryLimit)
	}
	if err := g.mayRemember(t, text); err != nil {
		return "", err
	}
	switch err := writeDiaryEntry(g.mind.memDir, day, g.labelled(text, t)); {
	case errors.Is(err, errHasEntry):
		return "", refuse("%s %s already has an entry in your diary; it stays as it is", weekday(day), day)
	case err != nil:
		return "", err
	}
	said := fmt.Sprintf("Written in your diary for %s %s.", weekday(day), day)
	if asked := strings.TrimSpace(a.Day); asked != "" && asked != day {
		said += fmt.Sprintf(" (Under %s, the day you are looking back on, not %s.)", day, asked)
	}
	return said, nil
}

// envelopeField is one field's value in an envelope, unquoted, or "".
func envelopeField(input, key string) string {
	for _, f := range strings.Fields(input) {
		if v, ok := strings.CutPrefix(f, key+"="); ok {
			if u, err := strconv.Unquote(v); err == nil {
				return u
			}
			return v
		}
	}
	return ""
}

// recallHits is how many lines recall returns from each source.
const recallHits = 5

// memoryOf is the ghost's memory, or a refusal when it has none.
func memoryOf(g *Ghost) (memory.Store, error) {
	if g.mind == nil || g.mind.mem == nil {
		return nil, refuse("you have no memory to keep this in")
	}
	return g.mind.mem, nil
}

// provenance is the source a line written now is labelled with: a file or
// the web when the turn has read one, since the words may be theirs; the
// person on a turn they started; the ghost itself otherwise.
func provenance(t *turn) string {
	if t == nil {
		return "self"
	}
	switch tainted := t.taintedBy(); {
	case tainted&taintFile != 0:
		return "file"
	case tainted&taintWeb != 0:
		return "web"
	case t.person:
		return "person"
	default:
		return "self"
	}
}

// theirs is the refusal to take back a line the person stated on a turn
// that is not theirs alone.
// The result wraps it as "That was not allowed: …." so it has no stop.
const theirs = "That line is theirs; only they can take it back, on a turn of their own"

// mayRetire is the source rule, for forget and for remember's replaces: a
// line the person stated may be taken back only on a turn the person
// started with nothing read (provenance "person"), or on the ghost's
// reflection on a finished day (reflect.go), which weighs the whole day
// the person spoke in, in either mode and whatever the day read. Any other
// event turn, or one that read a page or a file, could be somebody else's
// words talking the ghost out of what the person told it. Any other line,
// any turn.
func mayRetire(t *turn, f memory.Fact) error {
	if f.Prov.Source == "person" && provenance(t) != "person" && !reflecting(t) {
		return refuse(theirs)
	}
	return nil
}

// matching is the facts in a topic that read as line, the way the store
// matches it.
func matching(m memory.Store, topic, line string) (all, hit []memory.Fact, err error) {
	all, err = m.Facts(topic)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range all {
		if memory.SameLine(f, line) {
			hit = append(hit, f)
		}
	}
	return all, hit, nil
}

// reflecting is whether t is the ghost's reflection on a finished day.
func reflecting(t *turn) bool { return t != nil && t.kind() == reflectKind }

// othersCap is how many of a topic's other lines a remember result lists.
const othersCap = 12

// listLines quotes up to othersCap lines, the ones that are not except.
func listLines(facts []memory.Fact, except string) string {
	var q []string
	more := 0
	for _, f := range facts {
		if strings.EqualFold(f.Line, except) {
			continue
		}
		if len(q) == othersCap {
			more++
			continue
		}
		q = append(q, "'"+f.Line+"'")
	}
	s := strings.Join(q, ", ")
	if more > 0 {
		s += fmt.Sprintf(" and %d more", more)
	}
	return s
}

// mayRemember is the gate every write passes (remember, rewrite_notes,
// skill_write), by the trust table (trust.go): on cloud, a write whose words
// repeat what the turn read is refused, naming what it repeats; on local
// it is kept, and the label says where it came from.
func (g *Ghost) mayRemember(t *turn, text string) error {
	if g.trust().needs(classWrite)&needEcho == 0 {
		return nil
	}
	e, ok := t.echoes(text)
	if !ok {
		return nil
	}
	from, _ := UntrustedRead(e.tool)
	quoted := make([]string, 0, len(e.shared))
	for i, s := range e.shared {
		if i == 3 {
			break
		}
		quoted = append(quoted, fmt.Sprintf("%q", clip(s, 80)))
	}
	g.log.Info(hlog.Memory, "refused a write that repeats %s (%s): %s", from, e.tool, strings.Join(quoted, ", "))
	return refuse("that repeats what you read from %s this turn (%s: %s); keep what the person told you, in your own words",
		from, e.tool, strings.Join(quoted, ", "))
}

func remember(_ context.Context, g *Ghost, t *turn, args json.RawMessage) (string, error) {
	var a struct {
		Topic    string `json:"topic"`
		Line     string `json:"line"`
		Replaces string `json:"replaces"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	m, err := memoryOf(g)
	if err != nil {
		return "", err
	}
	line := strings.Join(strings.Fields(a.Line), " ")
	if line == "" {
		return "", fmt.Errorf("remember what?")
	}
	replaces := strings.Join(strings.Fields(a.Replaces), " ")
	if strings.EqualFold(replaces, line) {
		replaces = "" // the line replacing itself is the line kept
	}
	src := provenance(t)
	if err := g.mayRemember(t, line); err != nil {
		return "", err
	}
	// The topic as it stood, for what replaces names and for the lines
	// listed back. A topic name the store refuses is Remember's to say.
	before, old, _ := matching(m, a.Topic, replaces)
	if replaces == "" {
		old = nil
	}
	for _, f := range old {
		if mayRetire(t, f) != nil {
			return "", refuse("%s. Remember the new line without replaces to keep both", theirs)
		}
	}
	var id int64
	if t != nil {
		id = t.id
	}
	p := memory.Provenance{Source: src, Turn: id, At: time.Now(), Reflection: reflecting(t)}
	if err := m.Remember(a.Topic, line, p); err != nil {
		return "", err
	}
	topic, _ := memory.NormalizeTopic(a.Topic)
	switch {
	case len(old) > 0:
		if err := m.Retire(topic, replaces, p); err != nil {
			return "", err
		}
		return fmt.Sprintf("Kept. Retired: %s.", strings.TrimRight(old[0].Line, ".")), nil
	case replaces != "":
		others := listLines(before, line)
		if others == "" {
			others = "nothing else"
		}
		return fmt.Sprintf("Kept. No line '%s' to retire; the topic has: %s", replaces, others), nil
	}
	said := fmt.Sprintf("Remembered under %s, as from %s.", topic, src)
	// The topic's other lines, so a line this one makes false is in front
	// of the model while it can still retire it.
	if others := listLines(before, line); others != "" {
		said += " The topic also has: " + others + ". If one is now false, remember again with replaces naming it."
	}
	return said, nil
}

func forget(_ context.Context, g *Ghost, t *turn, args json.RawMessage) (string, error) {
	var a struct {
		Topic string `json:"topic"`
		Line  string `json:"line"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	m, err := memoryOf(g)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Line) == "" {
		return "", fmt.Errorf("forget which line? (a whole topic is the person's to remove)")
	}
	if _, hit, err := matching(m, a.Topic, a.Line); err == nil {
		for _, f := range hit {
			if err := mayRetire(t, f); err != nil {
				return "", err
			}
		}
	}
	if err := m.Forget(a.Topic, a.Line); err != nil {
		return "", err
	}
	return "Forgotten.", nil
}

func recall(ctx context.Context, g *Ghost, t *turn, args json.RawMessage) (string, error) {
	var a struct {
		Query string `json:"query"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", fmt.Errorf("recall what?")
	}
	var b strings.Builder
	if m, err := memoryOf(g); err == nil {
		hits, err := m.Recall(ctx, a.Query, recallHits)
		if err != nil {
			return "", err
		}
		if len(hits) > 0 {
			b.WriteString("From what you remember:\n")
			for _, h := range hits {
				fmt.Fprintf(&b, "- [%s] %s (from %s, %s)\n", h.Topic, h.Line, h.Prov.Source, h.Prov.At.Format("2 Jan 2006"))
			}
		}
	}
	if g.mind != nil {
		hits, err := g.mind.tr.search(ctx, a.Query, recallHits+1)
		if err != nil {
			return "", err
		}
		n := 0
		for _, h := range hits {
			// This turn's own words find themselves.
			if (t != nil && h.Turn == t.id) || n == recallHits {
				continue
			}
			if n == 0 {
				b.WriteString("From what was said before:\n")
			}
			n++
			who := "you said"
			switch h.Kind {
			case rowInput:
				who = "they said"
			case rowSummary:
				who = "in summary"
			case rowDiary:
				who = "in your diary"
			}
			fmt.Fprintf(&b, "- %s, %s: %s\n", h.At.Format("2 Jan 2006"), who, clip(h.Text, 300))
		}
	}
	if b.Len() == 0 {
		return "Nothing comes to mind.", nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func rewriteNotes(_ context.Context, g *Ghost, t *turn, args json.RawMessage) (string, error) {
	var a struct {
		About string `json:"about"`
		Text  string `json:"text"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.About != "self" && a.About != "you" {
		return "", fmt.Errorf(`about is "self" or "you"`)
	}
	m, err := memoryOf(g)
	if err != nil {
		return "", err
	}
	if err := g.mayRemember(t, a.Text); err != nil {
		return "", err
	}
	// The notes are kept whole, so their label is one line at the end:
	// where the words written this time came from.
	if err := m.SetCore(a.About, g.labelled(a.Text, t)); err != nil {
		return "", err
	}
	return fmt.Sprintf("Your notes are rewritten, as from %s.", provenance(t)), nil
}

// MemoryWrite reports whether a decision saved something the ghost
// keeps (a line, its notes, a diary entry, a skill of its own), and the
// words it saved;
// a removal saves none. For `haunt
// history`'s echo check, which compares what was saved with what the
// ghost read.
func MemoryWrite(name string, args json.RawMessage) (saved string, ok bool) {
	var a struct {
		Line string `json:"line"`
		Text string `json:"text"`
	}
	switch name {
	case "remember":
		json.Unmarshal(args, &a)
		return strings.TrimSpace(a.Line), true
	case "rewrite_notes", diaryName:
		json.Unmarshal(args, &a)
		return strings.TrimSpace(a.Text), true
	case "skill_write":
		var w struct {
			Body string `json:"body"`
		}
		json.Unmarshal(args, &w)
		return strings.TrimSpace(w.Body), true
	case "forget":
		return "", true
	}
	return "", false
}
