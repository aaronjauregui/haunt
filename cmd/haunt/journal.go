package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/library"
	"github.com/aaronjauregui/haunt/internal/store"
)

// cmdJournal prints a ghost's self-notes: what it chose to remember, plus
// whatever consolidation has since concluded. Half debugging tool, half the
// point — and with -entities, the answer to "what does it think it knows
// about me", which is a thing a person is entitled to read rather than
// infer from how the ghost behaves.
func cmdJournal(args []string) error {
	fs := flag.NewFlagSet("journal", flag.ExitOnError)
	state := fs.String("state", "", "override state directory")
	entities := fs.Bool("entities", false, "print the entity index instead: subjects and what it knows about them")
	fs.Parse(args)

	stateDir, err := resolveStateDir(*state, fs.Args())
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}

	s, err := store.Open(filepath.Join(stateDir, "state.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	if *entities {
		return printEntities(s)
	}
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

// printEntities lists the subjects the ghost has concluded things about,
// most-talked-about first.
func printEntities(s *store.Store) error {
	all, err := s.All()
	if err != nil {
		return err
	}
	var ents []ghost.Entity
	for k, v := range all {
		if !strings.HasPrefix(k, ghost.EntityKeyPrefix) {
			continue
		}
		e, err := ghost.DecodeEntity(v)
		if err != nil || e.Name == "" {
			continue
		}
		ents = append(ents, e)
	}
	if len(ents) == 0 {
		// An empty index is a young ghost, not a failure.
		fmt.Println("(it has not concluded anything about anyone yet)")
		return nil
	}
	sort.SliceStable(ents, func(i, j int) bool { return ents[i].Mentions > ents[j].Mentions })
	for _, e := range ents {
		who := e.Name
		if e.Kind != "" {
			who += " (" + e.Kind + ")"
		}
		fmt.Printf("%s  last mentioned %s\n", who, e.Last.Format("2006-01-02"))
		for _, f := range e.Facts {
			fmt.Printf("    %s\n", f)
		}
	}
	return nil
}

// resolveStateDir finds a ghost's state directory: an explicit override, a
// named ghost, or whichever ghost is current.
func resolveStateDir(override string, args []string) (string, error) {
	if override != "" {
		return override, nil
	}
	if len(args) == 1 {
		return library.GhostDir(args[0])
	}
	name, err := library.Current()
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", fmt.Errorf("pass a ghost name or -state DIR")
	}
	return library.GhostDir(name)
}
