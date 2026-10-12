package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/store"
)

// cmdHistory prints a ghost's recent conversation. The balloon is
// transient — a long reply pages away, and looking up a moment too late
// means it is gone — so the words have to be readable somewhere that keeps
// them. A ghost's conversation is its transcript, transcript.db in its
// state directory; this reads it without writing, so it works with the
// ghost running.
func cmdHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	state := fs.String("state", "", "override state directory")
	n := fs.Int("n", 10, "how many turns to show, most recent last")
	why := fs.Bool("why", false, "show what went into each reply: what woke the ghost, what was on screen, what it did")
	asJSON := fs.Bool("json", false, "print one JSON object per turn, with every decision's arguments and result, for a program to read")
	fs.Parse(args)

	stateDir, err := resolveStateDir(*state, fs.Args())
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	msgs, err := readTranscript(stateDir)
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	// The reasons, if they were asked for. Loaded once and matched to the
	// turns rather than looked up per line: they are a short ring of their
	// own and may not cover the whole window.
	var traces []ghost.Why
	if len(msgs) > 0 && (*why || *asJSON) {
		s, err := store.Open(filepath.Join(stateDir, "state.db"))
		if err != nil {
			return err
		}
		defer s.Close()
		if raw, ok, err := s.Get(ghost.WhyStoreKey); err != nil {
			return err
		} else if ok {
			if traces, err = ghost.DecodeWhy(raw); err != nil {
				return fmt.Errorf("the reply trace is corrupt: %w", err)
			}
		}
	}
	return showHistory(os.Stdout, msgs, traces, *n, *why, *asJSON)
}

// showHistory writes the conversation read back from the transcript as
// cmdHistory's flags ask: its last n lines, or with why or asJSON its last n turns, with
// their reasons, their calls, and the memory writes that repeat a page or
// a file (markEchoes).
func showHistory(w io.Writer, msgs []message, traces []ghost.Why, n int, why, asJSON bool) error {
	if len(msgs) == 0 {
		if !asJSON {
			fmt.Fprintln(w, "(nothing has been said yet)")
		}
		return nil
	}
	if !why && !asJSON {
		if n > 0 && len(msgs) > n {
			msgs = msgs[len(msgs)-n:]
		}
		for _, t := range mergeTurns(msgs, nil) {
			if t.input != nil {
				printLine(w, *t.input)
			}
			for _, r := range t.replies {
				printLine(w, r)
			}
		}
		return nil
	}

	turns := mergeTurns(msgs, traces)
	// A summary is shown where it was made, but it is not a turn: it has
	// no input, no reasons and no calls, and -json is turns.
	if asJSON {
		turns = slices.DeleteFunc(turns, func(t historyTurn) bool { return t.summary() })
	}
	// Over everything read back, before it is cut to n: the page a write
	// repeats may be a few turns older than the window shown.
	markEchoes(turns)
	if asJSON && n > 0 && len(turns) > 0 && turns[0].input == nil {
		// The window was read generously, so its first turn may be the
		// tail of one that began before it; that one is left out rather
		// than shown without what provoked it.
		turns = turns[1:]
	}
	if n > 0 && len(turns) > n {
		turns = turns[len(turns)-n:]
	}

	if asJSON {
		enc := json.NewEncoder(w)
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
			printLine(w, *t.input)
		}
		for _, r := range t.replies {
			printLine(w, r)
		}
		if t.summary() || t.final() == nil {
			// An interim line is part of the turn whose last reply
			// carries the reasons.
			continue
		}
		if t.whyKept {
			printWhy(w, t.why)
		} else {
			fmt.Fprintln(w, "       (no reasons kept for this one — it is older than the trace)")
		}
		printLooked(w, t, len(t.replies)-1)
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
	input *message
	// replies is the ghost's lines, oldest first. Every one but the last
	// is interim; the last is interim too when the turn ended on a tool
	// call, and then the turn has no final reply.
	replies []message
	// why is the driver's record of the final reply, when the ring still
	// holds it (whyKept).
	why     ghost.Why
	whyKept bool
	// echoes is the turn's memory writes that repeat a page or a file
	// read in it or shortly before (markEchoes).
	echoes []echo
}

// summary reports whether the "turn" is a summary of older ones.
func (t *historyTurn) summary() bool {
	return t.input != nil && (t.input.Role == roleSummary || t.input.Role == roleDiary)
}

// final is the turn's last word, or nil when it has none.
func (t *historyTurn) final() *message {
	if len(t.replies) == 0 || t.replies[len(t.replies)-1].Interim {
		return nil
	}
	return &t.replies[len(t.replies)-1]
}

// mergeTurns groups the session's messages into turns and matches each
// final reply to its reasons. A turn ends at a reply that is not interim,
// or where the next input begins.
func mergeTurns(msgs []message, traces []ghost.Why) []historyTurn {
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
// The calls and the words come from the transcript, which keeps every
// turn. The driver's reasons (ghost.Why) keep only the last dozen, and a
// long transcript outlives them; rather than fail or go quiet past the ring,
// an older turn is recorded from the transcript alone. What is lost there
// is only what the transcript never had: Window is left out, and SelfPrompt is
// read from the input being an event envelope, which is what the driver
// decides it by for every turn it starts from one.
type traceRecord struct {
	Time       time.Time    `json:"time"`
	ProvokedBy string       `json:"provoked_by"` // "person" or "event"
	Input      string       `json:"input"`       // an event without its envelope prefix
	Reply      string       `json:"reply"`       // "" when nothing was said
	Silent     bool         `json:"silent"`
	Failed     bool         `json:"failed"` // the turn failed: the model never answered
	SelfPrompt bool         `json:"self_prompt"`
	Acted      []traceCall  `json:"acted"`  // the ghost's decisions that act or read: intend, search, face, …
	Looked     []traceCall  `json:"looked"` // its memory and skills: recall, remember, skill_view, …
	Echoes     []traceEcho  `json:"echoes"` // memory writes repeating a page or a file
	Window     *traceWindow `json:"window,omitempty"`
}

// traceCall is one tool call in a traceRecord.
type traceCall struct {
	Tool   string `json:"tool"`
	Args   string `json:"args"`
	Result string `json:"result"`
}

// traceEcho is a memory write in a traceRecord whose words repeat what the
// ghost read from the web or a file (markEchoes).
type traceEcho struct {
	Tool     string   `json:"tool"`      // the write's tool, as in looked
	Saved    string   `json:"saved"`     // the words it saved
	Read     string   `json:"read"`      // the ghost's tool that read them, as in acted
	ReadArgs string   `json:"read_args"` // what it read: a path, a URL, a query
	TurnsAgo int      `json:"turns_ago"` // 0 when the read is in the write's own turn
	Shared   []string `json:"shared"`    // the words both have, as the write has them
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
	event := ghost.IsEnvelope(raw)
	r := traceRecord{
		ProvokedBy: "person",
		Input:      strings.TrimSpace(raw),
		SelfPrompt: event,
		Acted:      []traceCall{},
		Looked:     []traceCall{},
		Echoes:     []traceEcho{},
	}
	for _, e := range t.echoes {
		r.Echoes = append(r.Echoes, e.traceEcho)
	}
	if event {
		r.ProvokedBy = "event"
		r.Input = strings.TrimSpace(strings.TrimPrefix(raw, ghost.EnvelopePrefix))
	}
	// Every line the ghost wrote that was not silence reached the balloon,
	// interim ones included; the calls ride on whichever carries them.
	var said []string
	for _, m := range t.replies {
		if m.Failed {
			r.Failed = true
		} else if !m.Silent && strings.TrimSpace(m.Text) != "" {
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
	// The time of the last thing known about the turn.
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
func printLine(w io.Writer, m message) {
	who := "you"
	switch {
	case m.Role == roleSummary:
		who = "summary"
	case m.Role == roleDiary:
		// A day folded behind its entry, named by the day.
		who = "diary " + m.Time.Local().Format("2006-01-02")
	case m.Role == "assistant":
		who = "ghost"
	case ghost.IsEnvelope(m.Text):
		// The desktop, not the person: what woke the ghost.
		who = "event"
		m.Text = strings.TrimSpace(strings.TrimPrefix(m.Text, ghost.EnvelopePrefix))
	}
	// Indent continuations so a multi-paragraph reply stays legible.
	text := strings.ReplaceAll(strings.TrimSpace(m.Text), "\n", "\n       ")
	if m.Failed {
		text = "(the turn failed)"
	} else if who == "ghost" && m.Silent {
		text = "(said nothing)"
	}
	fmt.Fprintf(w, "%-6s %s\n", who+":", text)
}

// printLooked lists what the ghost consulted or changed in its memory and
// skills before a reply. The calls on t's
// replies[reply] are read whole and shown cut to a line; a memory write
// that repeats a page or a file is marked under it with a `!`.
func printLooked(w io.Writer, t historyTurn, reply int) {
	for i, call := range t.replies[reply].Looked {
		l := call.short()
		line := "       · used " + l.Tool
		if l.Args != "" && l.Args != "{}" {
			line += " " + l.Args
		}
		if l.Result != "" {
			line += "\n           → " + l.Result
		}
		for _, e := range t.echoes {
			if e.reply == reply && e.call == i {
				line += "\n           " + echoLine(e.traceEcho)
			}
		}
		fmt.Fprintln(w, line)
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
// the driver's trace. What the ghost looked up is in the transcript
// (printLooked).
func printWhy(out io.Writer, w ghost.Why) {
	const indent = "       "
	switch {
	case w.Suppressed:
		fmt.Fprintln(out, indent+"· woke, and chose not to speak")
	case w.SelfPrompt:
		fmt.Fprintln(out, indent+"· spoke first — nobody asked")
	}
	if !w.Window.Empty() {
		what := w.Window.App
		if w.Window.Title != "" {
			what += " — " + w.Window.Title
		}
		fmt.Fprintln(out, indent+"· on screen at the time: "+what)
	}
	if len(w.Acted) > 0 {
		fmt.Fprintf(out, "%s· did something first: %s\n", indent, strings.Join(w.Acted, ", "))
	}
}

// roleSummary is a summary's role among the messages: the driver's account
// of turns the model no longer sees whole.
const roleSummary = "summary"

// roleDiary is a day folded behind its diary entry: like a summary, an
// account of turns the model no longer sees, one per day.
const roleDiary = "diary"

// message is one line of the conversation as history shows it: an input,
// one of the ghost's lines, or a summary.
type message struct {
	Role string
	Text string
	Time time.Time
	// Looked is the ghost's memory and skill decisions on the way to a
	// reply; Acted is every other decision, the ones that act or read.
	// speak and stay_silent are how a turn ends, and are neither.
	Looked, Acted []call
	// Interim is words written beside calls, before the turn's last word.
	Interim bool
	// Silent is a turn that ended in silence; Failed one whose model never
	// answered.
	Silent, Failed bool
}

// call is one decision and its result.
type call struct {
	Tool, Args, Result string
}

// Lengths a call is cut to when shown on a line.
const (
	lookupArgs   = 120
	lookupResult = 160
)

// short is the call cut to a line each way, for showing a person.
func (c call) short() call {
	return call{Tool: c.Tool, Args: echoClip(c.Args, lookupArgs), Result: echoClip(c.Result, lookupResult)}
}

// looked are the decisions history files under Looked.
var looked = map[string]bool{
	"recall": true, "remember": true, "forget": true, "rewrite_notes": true, "diary": true,
	"skill_view": true, "skill_write": true,
}

// readTranscript reads a ghost's conversation from its transcript, whole.
func readTranscript(stateDir string) ([]message, error) {
	rows, err := ghost.ReadTranscript(filepath.Join(stateDir, "transcript.db"))
	if err != nil {
		return nil, err
	}
	return messagesOf(rows), nil
}

// messagesOf turns transcript rows into history's lines. Calls ride on the
// line the ghost wrote them beside, or on the turn's last line when it
// wrote none.
func messagesOf(rows []ghost.TranscriptRow) []message {
	// Each call's result, by row and position. A result is matched to the
	// first call of its id in the turn still waiting for one: ids are the
	// provider's, and not every provider makes them unique across a turn.
	type at struct{ row, call int }
	results := map[at]string{}
	var open []at
	for ri, r := range rows {
		switch r.Kind {
		case "input", "event":
			open = nil
		case "calls":
			for ci := range r.Calls {
				open = append(open, at{ri, ci})
			}
		case "result":
			for k, a := range open {
				if rows[a.row].Calls[a.call].ID == r.CallID {
					results[a] = r.Text
					open = append(open[:k], open[k+1:]...)
					break
				}
			}
		}
	}

	var out []message
	var pending []at
	attach := func(m *message) {
		for _, a := range pending {
			c := rows[a.row].Calls[a.call]
			if c.Name == "speak" || c.Name == "stay_silent" {
				continue
			}
			lc := call{Tool: c.Name, Args: string(c.Args), Result: results[a]}
			if looked[c.Name] {
				m.Looked = append(m.Looked, lc)
			} else {
				m.Acted = append(m.Acted, lc)
			}
		}
		pending = nil
	}
	for ri, r := range rows {
		switch r.Kind {
		case "input", "event":
			pending = nil
			out = append(out, message{Role: "user", Text: r.Text, Time: r.At})
		case "summary":
			out = append(out, message{Role: roleSummary, Text: r.Text, Time: r.At})
		case "diary":
			out = append(out, message{Role: roleDiary, Text: r.Text, Time: r.At})
		case "calls":
			for ci := range r.Calls {
				pending = append(pending, at{ri, ci})
			}
			if strings.TrimSpace(r.Text) != "" {
				m := message{Role: "assistant", Text: r.Text, Time: r.At, Interim: true}
				attach(&m)
				out = append(out, m)
			}
		case "words", "silent", "failed":
			m := message{Role: "assistant", Text: r.Text, Time: r.At, Silent: r.Kind == "silent", Failed: r.Kind == "failed"}
			if m.Failed || m.Silent {
				m.Text = ""
			}
			attach(&m)
			out = append(out, m)
		}
	}
	return out
}
