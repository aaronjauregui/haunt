package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/store"
)

// cmdHistory prints a ghost's recent conversation. The balloon is
// transient — a long reply pages away, and looking up a moment too late
// means it is gone — so the words have to be readable somewhere that keeps
// them. They are already stored; this is the window onto them.
func cmdHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	state := fs.String("state", "", "override state directory")
	n := fs.Int("n", 10, "how many turns to show, most recent last")
	why := fs.Bool("why", false, "show what went into each reply: recalled memories, what was on screen, the prompt")
	full := fs.Bool("prompt", false, "with -why, print the whole system prompt as it was sent")
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

	raw, ok, err := s.Get(ghost.HistoryStoreKey)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("(nothing has been said yet)")
		return nil
	}
	turns, err := ghost.DecodeHistory(raw)
	if err != nil {
		return fmt.Errorf("history is corrupt: %w", err)
	}
	if *n > 0 && len(turns) > *n {
		turns = turns[len(turns)-*n:]
	}
	// The reasons, if they were asked for. Loaded once and matched to the
	// turns below rather than looked up per line: they are a short ring of
	// their own and may not cover the whole window.
	var traces []ghost.Why
	if *why {
		if raw, ok, err := s.Get(ghost.WhyStoreKey); err != nil {
			return err
		} else if ok {
			if traces, err = ghost.DecodeWhy(raw); err != nil {
				return fmt.Errorf("the reply trace is corrupt: %w", err)
			}
		}
	}

	for _, t := range turns {
		who := "you"
		if t.Role == "assistant" {
			who = "ghost"
		}
		// Indent continuations so a multi-paragraph reply stays legible.
		text := strings.ReplaceAll(strings.TrimSpace(t.Text), "\n", "\n       ")
		fmt.Printf("%-6s %s\n", who+":", text)
		if !*why || t.Role != "assistant" {
			continue
		}
		w, ok := traceFor(traces, t.Text)
		if !ok {
			fmt.Println("       (no reasons kept for this one — it is older than the trace)")
			continue
		}
		printWhy(w, *full)
	}
	return nil
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

// printWhy renders one reply's reasons under the line it explains.
func printWhy(w ghost.Why, showPrompt bool) {
	const indent = "       "
	if w.SelfPrompt {
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
		// Before the memories, because it is the loudest fact about the
		// reply: the ghost did not only answer, it did something.
		fmt.Printf("%s· did something first: %s\n", indent, strings.Join(w.Acted, ", "))
	}
	if w.HistoryTurns > 0 {
		fmt.Printf("%s· with the last %d turns of this conversation\n", indent, w.HistoryTurns)
	}
	// A measurement, not a verdict: nothing is trimmed to fit and there is
	// no limit to compare it against. It is here because it is the number
	// to look at when a ghost on a modest machine starts timing out rather
	// than answering — the prompt is what a slow backend spends its time
	// on before the first token.
	if w.PromptChars > 0 {
		fmt.Printf("%s· the whole request was %s characters, of which %s were the prompt above\n",
			indent, thousands(w.PromptChars), thousands(len(w.System)))
	}
	if len(w.Recalled) == 0 {
		fmt.Println(indent + "· nothing was recalled from further back")
	} else {
		fmt.Printf("%s· recalled %d thing(s) from further back:\n", indent, len(w.Recalled))
		for _, r := range w.Recalled {
			kind := r.Kind
			if kind == "summary" {
				kind = "summary of many"
			}
			fmt.Printf("%s    [%s, %s, similarity %.2f", indent, kind, r.When.Format("2 Jan 2006"), r.Sim)
			if r.Uses > 0 {
				fmt.Printf(", recalled %d time(s) before", r.Uses)
			}
			fmt.Println("]")
			fmt.Printf("%s    %s\n", indent, oneLine(r.Text, 100))
		}
	}
	if showPrompt {
		fmt.Println(indent + "· the prompt it was given:")
		for _, line := range strings.Split(strings.TrimRight(w.System, "\n"), "\n") {
			fmt.Println(indent + "    " + line)
		}
	}
}

// thousands groups a count so a five-figure prompt is legible at a glance,
// which is the whole reason the number is printed.
func thousands(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// oneLine flattens a memory to something that fits under a history line.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	if len(s) > max {
		// Rune-safe: memories are conversation text and may be any script.
		r := []rune(s)
		if len(r) > max {
			return string(r[:max-1]) + "…"
		}
	}
	return s
}
