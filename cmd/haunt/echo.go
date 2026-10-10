package main

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/hermes"
	"github.com/aaronjauregui/haunt/internal/store"
)

// The echo check: a memory write that repeats what a page or a file said.
//
// Hermes memory is not haunt's to gate (docs/dev/decisions.md), so nothing
// stops the model saving a planted line as the person's. What haunt can do
// is show it: `haunt history` and `haunt journal -echoes` flag a write
// whose words are a read's words, and the person decides. It flags and
// never gates, and it sees only wording: a write that says the same thing
// in other words passes, and so does anything hermes saves outside the
// session (its background review, a provider that records every turn).
//
// Wording is compared as runs of words. Both texts are lowercased and cut
// into words (letters and digits; everything else separates), and every
// echoWords consecutive words is one run. A write echoes a read when they
// share at least echoShared runs that the person did not say themselves.
const (
	// echoWords is the length of a run. Two words match by accident all
	// the time ("in the", "to cats"); a model saving a fact keeps the
	// fact's own three-word phrases ("name is Bartholomew", "allergic to
	// cats") even when it rewrites the sentence around them, which four
	// words would already miss.
	echoWords = 3
	// echoShared is how many runs a write must share with a read. One is
	// what a page about the same thing shares by chance ("kyoto in
	// april"); two distinct ones is a fact carried over. A terse write
	// that keeps only one phrase of a planted line is missed.
	echoShared = 2
	// echoTurns is how far back a read counts: in the write's own turn or
	// the echoTurns before it. A planted line is saved in the turn that
	// read it, or a turn or two later when the conversation comes back to
	// it; further back, a phrase shared with an old page is more likely
	// the person returning to a subject than an echo. Event turns count,
	// so on a busy desktop the window is short in minutes.
	echoTurns = 3
)

// echo is one flagged write in a historyTurn: where its call is, and what
// it repeats.
type echo struct {
	reply, call int // the write is replies[reply].Looked[call]
	traceEcho
}

// markEchoes flags every memory write in turns that echoes a read of the
// web or a file in its window. The calls must be whole (Client.Transcript):
// what history shows a person is cut to a line, and a planted line can sit
// anywhere in a page. A read before the first turn is not seen, so the
// check is only as good as the window read back.
func markEchoes(turns []historyTurn) {
	for i := range turns {
		t := &turns[i]
		for ri, m := range t.replies {
			for ci, c := range m.Looked {
				saved, ok := hermes.MemoryWrite(c.Tool, c.Args)
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
		if t.input != nil && !hermes.IsEnvelope(t.input.Text) {
			for _, r := range wordRuns(t.input.Text) {
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
				shared, count := sharedRuns(saved, c.Result, own)
				if count >= echoShared && count > n {
					n = count
					best = traceEcho{Read: c.Tool, ReadArgs: c.Args, TurnsAgo: at - j, Shared: shared}
				}
			}
		}
	}
	return best, n > 0
}

// words is text lowercased and cut into words.
func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// wordRuns is every echoWords consecutive words of text, in order.
func wordRuns(text string) []string {
	w := words(text)
	var out []string
	for i := 0; i+echoWords <= len(w); i++ {
		out = append(out, strings.Join(w[i:i+echoWords], " "))
	}
	return out
}

// sharedRuns compares what was saved with what was read. count is how
// many distinct runs of the saved words the read has and own does not;
// shared is those runs joined where they overlap, as the saved words have
// them ("name is bartholomew", not its three-word pieces), for a person
// to read.
func sharedRuns(saved, read string, own map[string]bool) (shared []string, count int) {
	have := map[string]bool{}
	for _, r := range wordRuns(read) {
		have[r] = true
	}
	w := words(saved)
	covered := make([]bool, len(w))
	seen := map[string]bool{}
	for i := 0; i+echoWords <= len(w); i++ {
		r := strings.Join(w[i:i+echoWords], " ")
		if !have[r] || own[r] {
			continue
		}
		for k := i; k < i+echoWords; k++ {
			covered[k] = true
		}
		if !seen[r] {
			seen[r] = true
			count++
		}
	}
	for i := 0; i < len(w); {
		if !covered[i] {
			i++
			continue
		}
		j := i
		for j < len(w) && covered[j] {
			j++
		}
		shared = append(shared, strings.Join(w[i:j], " "))
		i = j
	}
	return shared, count
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
// conversation that repeats a page or a file. The conversation is hermes's,
// so like `haunt history` it needs the gateway running. It reads the whole
// session, since a write is only checked against the reads read back with
// it.
func printEchoes(w io.Writer, s *store.Store, name string) error {
	session, onHermes, err := s.Get(ghost.HermesSessionStoreKey)
	if err != nil {
		return err
	}
	if !onHermes {
		fmt.Fprintln(w, "(it has no hermes conversation, so nothing it saved came from one)")
		return nil
	}
	msgs, err := hermesHistory(name, session, 0, true)
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	writeEchoes(w, msgs)
	return nil
}

// writeEchoes lists the flagged writes in msgs, oldest first: when, what
// was saved, and the read it repeats.
func writeEchoes(w io.Writer, msgs []hermes.Message) {
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
