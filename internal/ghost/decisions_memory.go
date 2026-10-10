package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/memory"
)

// Memory: the ghost's own, in files the person can read and edit
// (internal/memory). Every line it keeps says where it came from, and the
// driver writes that, not the model: the source is what the turn had
// read when the line was written (provenance). Recall searches the topics
// and the conversation (transcript.go), so what was said a month ago is
// as reachable as what was saved.

func init() {
	register(verb{
		id: "remember",
		desc: "Keep one fact they told you about themselves, in your own words, under a topic (food, work…). " +
			"Not for a goal they drop: that is set_aside.",
		params: []string{"topic:string", "line:string"},
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
	line := strings.TrimSpace(a.Line)
	if line == "" {
		return "", fmt.Errorf("remember what?")
	}
	src := provenance(t)
	if err := g.mayRemember(t, line); err != nil {
		return "", err
	}
	var id int64
	if t != nil {
		id = t.id
	}
	if err := m.Remember(a.Topic, line, memory.Provenance{Source: src, Turn: id, At: time.Now()}); err != nil {
		return "", err
	}
	topic, _ := memory.NormalizeTopic(a.Topic)
	return fmt.Sprintf("Remembered under %s, as from %s.", topic, src), nil
}

func forget(_ context.Context, g *Ghost, _ *turn, args json.RawMessage) (string, error) {
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
// keeps (a line, its notes, a skill of its own), and the words it saved;
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
	case "rewrite_notes":
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
