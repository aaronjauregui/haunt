package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/aaronjauregui/haunt/internal/ghost"
)

// The echo check: a memory write that repeats what a page or a file said.
//
// Every line the ghost remembers is labelled with where it came from, but
// a model that has read a page can still save the page's claim as the
// person's. `haunt history` and `haunt journal -echoes` flag a write whose
// words are a read's words, and the person decides. It sees only wording:
// a write that says the same thing in other words passes.
//
// Wording is compared as runs of words (ghost.WordRuns, ghost.SharedRuns),
// the same check that refuses such a write on a cloud model as it is made.
// A write echoes a read when they share at least ghost.EchoShared runs
// that the person did not say themselves.

// echoTurns is how far back a read counts: in the write's own turn or the
// echoTurns before it. A planted line is saved in the turn that read it,
// or a turn or two later when the conversation comes back to it; further
// back, a phrase shared with an old page is more likely the person
// returning to a subject than an echo. Event turns count, so on a busy
// desktop the window is short in minutes.
const echoTurns = 3

// echo is one flagged write in a historyTurn: where its call is, and what
// it repeats.
type echo struct {
	reply, call int // the write is replies[reply].Looked[call]
	traceEcho
}

// markEchoes flags every memory write in turns that echoes a read of the
// web or a file in its window. The calls must be whole, as the transcript
// keeps them: what history shows a person is cut to a line, and a planted
// line can sit anywhere in a page. A read before the first turn is not seen, so the
// check is only as good as the window read back.
func markEchoes(turns []historyTurn) {
	for i := range turns {
		t := &turns[i]
		for ri, m := range t.replies {
			for ci, c := range m.Looked {
				saved, ok := ghost.MemoryWrite(c.Tool, json.RawMessage(c.Args))
				if !ok || saved == "" {
					continue
				}
				if e, ok := echoOf(turns, i, saved); ok {
					e.Tool, e.Saved = c.Tool, saved
					t.echoes = append(t.echoes, echo{reply: ri, call: ci, traceEcho: e})
				}
			}
		}
	}
}

// echoOf finds the read that the words saved in turns[at] repeat most,
// the latest on a tie.
func echoOf(turns []historyTurn, at int, saved string) (traceEcho, bool) {
	first := max(0, at-echoTurns)
	// What the person said in the window is theirs to have saved, however
	// many pages said it too.
	own := map[string]bool{}
	for _, t := range turns[first : at+1] {
		if t.input != nil && t.input.Role == "user" && !ghost.IsEnvelope(t.input.Text) {
			for _, r := range ghost.WordRuns(t.input.Text) {
				own[r] = true
			}
		}
	}
	var best traceEcho
	n := 0
	for j := at; j >= first; j-- {
		for _, m := range turns[j].replies {
			for _, c := range m.Acted {
				if _, ok := ghost.UntrustedRead(c.Tool); !ok {
					continue
				}
				shared, count := ghost.SharedRuns(saved, c.Result, own)
				if count >= ghost.EchoShared && count > n {
					n = count
					best = traceEcho{Read: c.Tool, ReadArgs: c.Args, TurnsAgo: at - j, Shared: shared}
				}
			}
		}
	}
	return best, n > 0
}

// echoLine is a flagged write as `history -why` and `journal -echoes` say
// it: what was read, when, and the words it shares.
func echoLine(e traceEcho) string {
	from, _ := ghost.UntrustedRead(e.Read)
	when := "this turn"
	switch {
	case e.TurnsAgo == 1:
		when = "a turn earlier"
	case e.TurnsAgo > 1:
		when = fmt.Sprintf("%d turns earlier", e.TurnsAgo)
	}
	quoted := make([]string, 0, len(e.Shared))
	for i, s := range e.Shared {
		if i == 3 {
			quoted = append(quoted, "…")
			break
		}
		quoted = append(quoted, fmt.Sprintf("%q", echoClip(s, 80)))
	}
	return fmt.Sprintf("! repeats what it read from %s (%s %s, %s): %s",
		from, e.Read, echoClip(e.ReadArgs, 120), when, strings.Join(quoted, ", "))
}

// printEchoes is `haunt journal -echoes`: every memory write in the ghost's
// transcript that repeats a page or a file. It reads the whole transcript,
// since a write is only checked against the reads read back with it.
func printEchoes(w io.Writer, stateDir string) error {
	msgs, err := readTranscript(stateDir)
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	writeEchoes(w, msgs)
	return nil
}

// writeEchoes lists the flagged writes in msgs, oldest first: when, what
// was saved, and the read it repeats.
func writeEchoes(w io.Writer, msgs []message) {
	turns := mergeTurns(msgs, nil)
	markEchoes(turns)
	n := 0
	for _, t := range turns {
		for _, e := range t.echoes {
			n++
			fmt.Fprintf(w, "%s  saved: %s\n", t.record().Time.Format("2006-01-02 15:04"), echoClip(e.Saved, 160))
			fmt.Fprintf(w, "  %s\n", echoLine(e.traceEcho))
		}
	}
	if n == 0 {
		fmt.Fprintln(w, "(nothing it saved repeats a page or a file it read)")
	}
}

// echoClip is s on one line, at most n runes.
func echoClip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
