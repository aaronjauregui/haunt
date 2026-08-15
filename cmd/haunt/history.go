package main

import (
	"flag"
	"fmt"
	"path/filepath"
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
	for _, t := range turns {
		who := "you"
		if t.Role == "assistant" {
			who = "ghost"
		}
		// Indent continuations so a multi-paragraph reply stays legible.
		text := strings.ReplaceAll(strings.TrimSpace(t.Text), "\n", "\n       ")
		fmt.Printf("%-6s %s\n", who+":", text)
	}
	return nil
}
