package main

import (
	"flag"
	"fmt"
	"path/filepath"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/library"
	"github.com/aaronjauregui/haunt/internal/store"
)

// cmdJournal prints a ghost's self-notes: what it chose to remember.
// Half debugging tool, half the point.
func cmdJournal(args []string) error {
	fs := flag.NewFlagSet("journal", flag.ExitOnError)
	state := fs.String("state", "", "override state directory")
	fs.Parse(args)

	var stateDir string
	switch {
	case *state != "":
		stateDir = *state
	case fs.NArg() == 1:
		var err error
		if stateDir, err = library.GhostDir(fs.Arg(0)); err != nil {
			return err
		}
	default:
		name, err := library.Current()
		if err != nil {
			return err
		}
		if name == "" {
			return fmt.Errorf("journal: pass a ghost name or -state DIR")
		}
		if stateDir, err = library.GhostDir(name); err != nil {
			return err
		}
	}

	s, err := store.Open(filepath.Join(stateDir, "state.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	raw, ok, err := s.Get(ghost.NotesStoreKey)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("(the journal is empty)")
		return nil
	}
	notes, err := ghost.DecodeNotes(raw)
	if err != nil {
		return fmt.Errorf("journal is corrupt: %w", err)
	}
	for _, n := range notes {
		fmt.Printf("%s  %s\n", n.Time.Format("2006-01-02 15:04"), n.Text)
	}
	return nil
}
