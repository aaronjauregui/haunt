package ghost

import (
	"encoding/json"
	"time"

	"github.com/aaronjauregui/haunt/internal/ai"
	"github.com/aaronjauregui/haunt/internal/hlog"
)

// "Why did you say that?"
//
// A ghost with recall and a journal says things whose reasons are invisible.
// It brings up a conversation from March and the person has no way to know
// whether that was memory or coincidence — and a companion you cannot
// interrogate is one you end up either over-trusting or quietly distrusting.
// So every reply the person can see keeps a record of what went into it:
// which memories came back and how strongly, what the ghost could see on
// screen at the time, and the prompt that was actually sent.
//
// The tracing already existed for debugging (`[ai] log_prompts`). The
// difference here is that it is *kept*, per exchange, rather than written to
// a log nobody has open at the moment they become curious.
//
// Three deliberate limits:
//
//   - It is conversation content, so it lives in the ghost's own store next
//     to the history and the journal, and never goes near a session
//     recording (internal/bus), which is a debugging artefact people share.
//   - Silent chats are not traced. Those are the brain talking to itself,
//     the person never saw a balloon, and there is no "that" to ask about.
//   - The ring is short. A full system prompt is kilobytes, and the honest
//     scope of the question is "that thing you just said", not an audit
//     log going back to installation.

const (
	whyKey = internalKeyPrefix + "why"
	// whyKept is how many replies keep their reasons. One per assistant
	// turn in the default history window, so anything still visible in
	// `haunt history` can still be asked about.
	whyKept = 12
)

// WhyStoreKey exposes the trace's store key to the CLI.
const WhyStoreKey = whyKey

// Recalled is one memory that came back, and how strongly.
type Recalled struct {
	// Kind is exchange, note or summary — a summary standing for a dozen
	// conversations is a different claim from a single remembered moment.
	Kind string `json:"kind"`
	// When the memory was originally written.
	When time.Time `json:"when"`
	// Sim is the cosine similarity that got it in. Recall selects
	// relative to the index's own baseline rather than against a fixed
	// floor, so this number is only meaningful next to its neighbours —
	// which is exactly how it is shown.
	Sim float64 `json:"sim"`
	// Uses is how many times it had proved relevant before this one.
	Uses int    `json:"uses,omitempty"`
	Text string `json:"text"`
}

// Why is the account of one reply.
type Why struct {
	Time time.Time `json:"time"`
	// User is what provoked it, empty when the ghost spoke first.
	User string `json:"user,omitempty"`
	// SelfPrompt says the ghost started this itself, so the absence of a
	// user line is the fact rather than a gap in the record.
	SelfPrompt bool   `json:"self_prompt,omitempty"`
	Reply      string `json:"reply"`
	// Recalled is what long-term memory contributed, strongest first.
	Recalled []Recalled `json:"recalled,omitempty"`
	// Acted is the tools that ran in the course of producing this reply,
	// in order. Empty for the ordinary exchange where nothing was done.
	// It belongs here for the same reason the recalled memories do: when a
	// ghost has *done* something, "why did you say that" and "what did you
	// do" are the same question, and the answer should not have to be
	// pieced together from the journal.
	Acted []string `json:"acted,omitempty"`
	// Window is what the ghost could see of the screen when it answered,
	// recorded only while the window capability is actually granted. It is
	// the context the ghost had, not necessarily context it used.
	Window WhyWindow `json:"window,omitempty"`
	// System is the system prompt as sent: the persona, the journal block,
	// the entity facts and the recall block, already assembled. This is the
	// part that answers "what did it actually know when it said that".
	System string `json:"system"`
	// HistoryTurns is how many earlier turns rode along with it. The turns
	// themselves are not copied — they are in the history already, and
	// duplicating them per exchange would square the storage.
	HistoryTurns int `json:"history_turns,omitempty"`
	// PromptChars is the size of the largest request this reply took: every
	// message sent, on the last round if tools ran and the array grew.
	//
	// A measurement and not a policy. Nothing acts on it, nothing is
	// trimmed to fit it, and there is no configured limit for it to be
	// compared against. It is here because the assembled prompt has a
	// bound that nobody chose — the journal's twenty notes, recall's five
	// memories, the entity facts and the intentions each have a cap picked
	// for its own reasons, and their sum is whatever it happens to be —
	// and the question of whether that sum ever matters is answerable by
	// looking rather than by arithmetic over worst cases that do not
	// occur. A prompt is also the thing a slow machine spends its time on
	// before the first token arrives, which is the deadline in
	// `FirstTokenTimeout`, so this is the number to look at when a ghost
	// on a modest machine starts timing out rather than answering.
	//
	// Characters, not tokens: the driver has no tokenizer and speaks to
	// backends that do not agree on one. Divide by three or four for a
	// rough count.
	//
	// The breakdown needs no fields of its own. System is stored whole, so
	// its size is len(System) and its blocks are self-labelling ("Your
	// private journal", "Things this reminds you of", and so on); what is
	// left over is the conversation and the utterance.
	PromptChars int `json:"prompt_chars,omitempty"`
}

// promptChars measures a request the way it goes out: the content of every
// message, plus the arguments of any tool call, which are sent too.
func promptChars(msgs []ai.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)
		for _, c := range m.ToolCalls {
			n += len(c.Function.Name) + len(c.Function.Arguments)
		}
	}
	return n
}

// WhyWindow is the focused window at the moment of a reply.
type WhyWindow struct {
	App   string `json:"app,omitempty"`
	Title string `json:"title,omitempty"`
}

// Empty reports whether nothing about the screen was known.
func (w WhyWindow) Empty() bool { return w.App == "" && w.Title == "" }

// noteWhy records the reasons behind a reply, oldest entries falling off
// the end.
func (g *Ghost) noteWhy(w Why) {
	raw, _, err := storeGet(g.store, whyKey)
	if err != nil {
		g.log.Warn(hlog.Store, "reading the reply trace: %v", err)
		return
	}
	var kept []Why
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &kept); err != nil {
			// A corrupt trace is not worth failing a conversation over;
			// start it again rather than lose the reply that caused it.
			g.log.Warn(hlog.Store, "the reply trace was unreadable; starting a new one")
			kept = nil
		}
	}
	kept = append(kept, w)
	if len(kept) > whyKept {
		kept = kept[len(kept)-whyKept:]
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return
	}
	if err := g.store.Set(whyKey, string(b)); err != nil {
		g.log.Warn(hlog.Store, "saving the reply trace: %v", err)
	}
}

// DecodeWhy parses stored traces, oldest first.
func DecodeWhy(raw string) ([]Why, error) {
	var out []Why
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// currentWindow is what the watch loop last saw, or the zero value when
// nothing is being watched.
func (g *Ghost) currentWindow() WhyWindow {
	g.sessMu.Lock()
	defer g.sessMu.Unlock()
	return g.seenWindow
}

// noteWindow records what the watch loop is looking at, so a reply can say
// what the ghost could see when it gave it.
func (g *Ghost) noteWindow(app, title string) {
	g.sessMu.Lock()
	g.seenWindow = WhyWindow{App: app, Title: title}
	g.sessMu.Unlock()
}

// recalledTrace turns recall's own scored selection into the record shown
// to a person. Truncated per entry: the point is to recognise which memory
// it was, not to reprint the archive.
func recalledTrace(picked []scored) []Recalled {
	if len(picked) == 0 {
		return nil
	}
	out := make([]Recalled, 0, len(picked))
	for _, p := range picked {
		out = append(out, Recalled{
			Kind: p.m.Kind,
			When: p.m.Time,
			Sim:  p.sim,
			Uses: p.m.Uses,
			Text: p.m.Text,
		})
	}
	return out
}
