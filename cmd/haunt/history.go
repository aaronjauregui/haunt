package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/hermes"
	"github.com/aaronjauregui/haunt/internal/store"
)

// cmdHistory prints a ghost's recent conversation. The balloon is
// transient — a long reply pages away, and looking up a moment too late
// means it is gone — so the words have to be readable somewhere that keeps
// them. A ghost's conversation is its hermes session; this is the window
// onto it.
func cmdHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	state := fs.String("state", "", "override state directory")
	n := fs.Int("n", 10, "how many turns to show, most recent last")
	why := fs.Bool("why", false, "show what went into each reply: what woke the ghost, what was on screen, what it did")
	asJSON := fs.Bool("json", false, "print one JSON object per turn, with every tool call's arguments and result, for a program to read")
	fs.Parse(args)

	stateDir, err := resolveStateDir(*state, fs.Args())
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}

	s, err := store.Open(filepath.Join(stateDir, "state.db"))
	if err != nil {
		return err
	}
	defer s.Close()

	// The conversation is kept by hermes, and the store holds only which
	// session it is. No session means the ghost has never spoken (or is
	// scripted, and keeps no conversation).
	var msgs []hermes.Message
	if session, onHermes, err := s.Get(ghost.HermesSessionStoreKey); err != nil {
		return err
	} else if onHermes {
		name := filepath.Base(stateDir)
		if len(fs.Args()) == 1 {
			name = fs.Args()[0]
		}
		if msgs, err = hermesHistory(name, session, *n, *asJSON); err != nil {
			return fmt.Errorf("history: %w", err)
		}
	}
	if len(msgs) == 0 {
		if !*asJSON {
			fmt.Println("(nothing has been said yet)")
		}
		return nil
	}
	if !*asJSON && *n > 0 && len(msgs) > *n {
		msgs = msgs[len(msgs)-*n:]
	}
	// The reasons, if they were asked for. Loaded once and matched to the
	// turns rather than looked up per line: they are a short ring of their
	// own and may not cover the whole window.
	var traces []ghost.Why
	if *why || *asJSON {
		if raw, ok, err := s.Get(ghost.WhyStoreKey); err != nil {
			return err
		} else if ok {
			if traces, err = ghost.DecodeWhy(raw); err != nil {
				return fmt.Errorf("the reply trace is corrupt: %w", err)
			}
		}
	}
	turns := mergeTurns(msgs, traces)

	if *asJSON {
		// The window was read generously, so its first turn may be the tail
		// of one that began before it; that one is left out rather than
		// shown without what provoked it.
		if *n > 0 && len(turns) > 0 && turns[0].input == nil {
			turns = turns[1:]
		}
		if *n > 0 && len(turns) > *n {
			turns = turns[len(turns)-*n:]
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		for _, t := range turns {
			if err := enc.Encode(t.record()); err != nil {
				return err
			}
		}
		return nil
	}

	for _, t := range turns {
		if t.input != nil {
			printLine(*t.input)
		}
		for _, r := range t.replies {
			printLine(r)
		}
		if !*why || t.final() == nil {
			// An interim line is part of the turn whose last reply
			// carries the reasons.
			continue
		}
		if t.whyKept {
			printWhy(t.why)
		} else {
			fmt.Println("       (no reasons kept for this one — it is older than the trace)")
		}
		printLooked(t.final().Looked)
	}
	return nil
}

// historyTurn is one exchange of the conversation: what provoked it, the
// ghost's lines in answer, and the driver's reasons for the last of them.
// Both `-why` and `-json` are drawn from it, so the two never disagree
// about which reasons and which calls belong to which reply.
type historyTurn struct {
	// input is the person's words or the event envelope; nil when the
	// window opens partway through a turn.
	input *hermes.Message
	// replies is the ghost's lines, oldest first. Every one but the last
	// is interim; the last is interim too when the turn ended on a tool
	// call, and then the turn has no final reply.
	replies []hermes.Message
	// why is the driver's record of the final reply, when the ring still
	// holds it (whyKept).
	why     ghost.Why
	whyKept bool
}

// final is the turn's last word, or nil when it has none.
func (t *historyTurn) final() *hermes.Message {
	if len(t.replies) == 0 || t.replies[len(t.replies)-1].Interim {
		return nil
	}
	return &t.replies[len(t.replies)-1]
}

// mergeTurns groups the session's messages into turns and matches each
// final reply to its reasons. A turn ends at a reply that is not interim,
// or where the next input begins.
func mergeTurns(msgs []hermes.Message, traces []ghost.Why) []historyTurn {
	var turns []historyTurn
	open := false // whether the last turn can still take replies
	for _, m := range msgs {
		if m.Role != "assistant" {
			turns = append(turns, historyTurn{input: &m})
			open = true
			continue
		}
		if !open {
			turns = append(turns, historyTurn{})
			open = true
		}
		t := &turns[len(turns)-1]
		t.replies = append(t.replies, m)
		if !m.Interim {
			t.why, t.whyKept = traceFor(traces, m.Text)
			open = false
		}
	}
	return turns
}

// traceRecord is one turn of `haunt history -json`, for a program reading
// the conversation back (the eval harness's graders): what provoked the
// turn, what the ghost said, and every tool call with its arguments and
// result.
//
// The calls and the words come from the hermes session, which keeps every
// turn. The driver's reasons (ghost.Why) keep only the last dozen, and a
// long session outlives them; rather than fail or go quiet past the ring,
// an older turn is recorded from the session alone. What is lost there is
// only what the session never had: Window is left out, and SelfPrompt is
// read from the input being an event envelope, which is what the driver
// decides it by for every turn it starts from one.
type traceRecord struct {
	Time       time.Time    `json:"time"`
	ProvokedBy string       `json:"provoked_by"` // "person" or "event"
	Input      string       `json:"input"`       // an event without its envelope prefix
	Reply      string       `json:"reply"`       // "" when nothing was said
	Silent     bool         `json:"silent"`
	Failed     bool         `json:"failed"` // the turn failed; hermes, not the ghost, closed it
	SelfPrompt bool         `json:"self_prompt"`
	Acted      []traceCall  `json:"acted"`  // the ghost's own tools, mcp__haunt__ taken off
	Looked     []traceCall  `json:"looked"` // hermes's own: memory, session_search, skill_view, …
	Window     *traceWindow `json:"window,omitempty"`
}

// traceCall is one tool call in a traceRecord.
type traceCall struct {
	Tool   string `json:"tool"`
	Args   string `json:"args"`
	Result string `json:"result"`
}

// traceWindow is what was on screen when the ghost answered.
type traceWindow struct {
	App   string `json:"app"`
	Title string `json:"title"`
}

// record is the turn as `-json` prints it.
func (t historyTurn) record() traceRecord {
	raw := ""
	if t.input != nil {
		raw = t.input.Text
	} else if t.whyKept {
		raw = t.why.User
	}
	event := hermes.IsEnvelope(raw)
	r := traceRecord{
		ProvokedBy: "person",
		Input:      strings.TrimSpace(raw),
		SelfPrompt: event,
		Acted:      []traceCall{},
		Looked:     []traceCall{},
	}
	if event {
		r.ProvokedBy = "event"
		r.Input = strings.TrimSpace(strings.TrimPrefix(raw, hermes.EnvelopePrefix))
	}
	// Every line the ghost wrote that was not silence reached the balloon,
	// interim ones included; the calls ride on whichever carries them.
	var said []string
	for _, m := range t.replies {
		if m.Failed {
			r.Failed = true
		} else if !hermes.Silent(m.Text) {
			said = append(said, strings.TrimSpace(m.Text))
		}
		for _, c := range m.Acted {
			r.Acted = append(r.Acted, traceCall(c))
		}
		for _, c := range m.Looked {
			r.Looked = append(r.Looked, traceCall(c))
		}
	}
	r.Reply = strings.Join(said, "\n")
	r.Silent = r.Reply == "" && !r.Failed
	// The time of the last thing known about the turn: hermes does not
	// always stamp a reply, and an unstamped one reads as the epoch.
	for _, at := range []time.Time{t.lastReplyTime(), t.why.Time, t.inputTime()} {
		if !at.IsZero() && at.Unix() != 0 {
			r.Time = at
			break
		}
	}
	if t.whyKept {
		r.SelfPrompt = t.why.SelfPrompt
		if !t.why.Window.Empty() {
			r.Window = &traceWindow{App: t.why.Window.App, Title: t.why.Window.Title}
		}
	}
	return r
}

func (t historyTurn) lastReplyTime() time.Time {
	if len(t.replies) == 0 {
		return time.Time{}
	}
	return t.replies[len(t.replies)-1].Time
}

func (t historyTurn) inputTime() time.Time {
	if t.input == nil {
		return time.Time{}
	}
	return t.input.Time
}

// printLine prints one line of the conversation under who said it.
func printLine(m hermes.Message) {
	who := "you"
	switch {
	case m.Role == "assistant":
		who = "ghost"
	case hermes.IsEnvelope(m.Text):
		// The desktop, not the person: what woke the ghost.
		who = "event"
		m.Text = strings.TrimSpace(strings.TrimPrefix(m.Text, hermes.EnvelopePrefix))
	}
	// Indent continuations so a multi-paragraph reply stays legible.
	text := strings.ReplaceAll(strings.TrimSpace(m.Text), "\n", "\n       ")
	if m.Failed {
		text = "(the turn failed)"
	} else if who == "ghost" && hermes.Silent(m.Text) {
		// Hermes keeps the token as the reply; nothing was shown.
		text = "(said nothing)"
	}
	fmt.Printf("%-6s %s\n", who+":", text)
}

// hermesHistory reads a ghost's conversation from its hermes gateway, which
// has to be running: the session lives in hermes's own database, and haunt
// reads it only through hermes. whole asks for every call's arguments and
// result uncut, and for a wider window, since the caller counts turns
// rather than lines.
func hermesHistory(name, session string, n int, whole bool) ([]hermes.Message, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	p, err := cfg.Hermes.OpenProfile(name)
	if err != nil {
		return nil, err
	}
	c := &hermes.Client{BaseURL: p.URL(), Key: p.Key, Ghost: name,
		LoadSession: func() string { return session }}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Health(ctx); err != nil {
		return nil, fmt.Errorf("%s's hermes gateway is not running, and the conversation is kept there (%v)", name, err)
	}
	if whole {
		// A turn is an input, a reply, and two entries for every call; a
		// busy turn makes several calls.
		return c.Transcript(ctx, 20*n)
	}
	// A turn is a line each way, and a tool call and its result are two
	// more; the caller keeps the last n.
	return c.Messages(ctx, 6*n)
}

// printLooked lists what hermes consulted or changed on its own before a
// reply: its memory, earlier sessions, its skills.
func printLooked(looked []hermes.Lookup) {
	for _, l := range looked {
		line := "       · hermes used " + l.Tool
		if l.Args != "" && l.Args != "{}" {
			line += " " + l.Args
		}
		if l.Result != "" {
			line += "\n           → " + l.Result
		}
		fmt.Println(line)
	}
}

// traceFor finds the reasons behind a reply, and consumes the match so two
// identical replies get their own record rather than sharing the first.
func traceFor(traces []ghost.Why, reply string) (ghost.Why, bool) {
	for i, w := range traces {
		if w.Reply == reply {
			// Blanked rather than removed: the slice is walked in order and
			// removing would shift everything after it.
			traces[i].Reply = "\x00consumed"
			return w, true
		}
	}
	return ghost.Why{}, false
}

// printWhy renders one reply's reasons under the line it explains, from
// the driver's trace. What hermes looked up is in its session
// (printLooked); what it was prompted with is not given out by its API.
func printWhy(w ghost.Why) {
	const indent = "       "
	switch {
	case w.Suppressed:
		fmt.Println(indent + "· woke, and chose not to speak")
	case w.SelfPrompt:
		fmt.Println(indent + "· spoke first — nobody asked")
	}
	if !w.Window.Empty() {
		what := w.Window.App
		if w.Window.Title != "" {
			what += " — " + w.Window.Title
		}
		fmt.Println(indent + "· on screen at the time: " + what)
	}
	if len(w.Acted) > 0 {
		fmt.Printf("%s· did something first: %s\n", indent, strings.Join(w.Acted, ", "))
	}
}
