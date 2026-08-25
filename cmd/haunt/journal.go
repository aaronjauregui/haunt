package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	intentions := fs.Bool("intentions", false, "print what it is still meaning to do")
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
	if *intentions {
		return printIntentions(s)
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
	name, err := soleResident("pass a ghost name or -state DIR")
	if err != nil {
		return "", err
	}
	return library.GhostDir(name)
}

// printIntentions lists what the ghost has taken on and not discharged.
//
// It belongs beside the journal rather than in a command of its own: the
// journal is what the ghost has concluded, the entity index is who it
// thinks people are, and this is what it is still meaning to do. All three
// are the same question — what is my ghost carrying — asked about different
// tenses, and a person checking one will want the others nearby.
func printIntentions(s *store.Store) error {
	raw, ok, err := s.Get(ghost.IntentionsStoreKey)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("(it is not meaning to do anything)")
		return nil
	}
	list, err := ghost.DecodeIntentions(raw)
	if err != nil {
		return fmt.Errorf("the intentions are corrupt: %w", err)
	}
	if len(list) == 0 {
		fmt.Println("(it is not meaning to do anything)")
		return nil
	}
	now := time.Now()
	for _, it := range list {
		mark := "  "
		note := ""
		switch {
		case it.Fired:
			// Worth flagging: its moment came and the ghost has not managed
			// to hand it back — quiet hours, or a fullscreen game, or
			// nobody was there. It is still owed.
			mark = "! "
			note = "  (its moment has passed and it has not said so yet)"
		case it.Aside:
			note = "  (set aside — still on the books, no longer watched)"
		case it.Goal():
			note = "  (no particular moment)"
		default:
			note = "  (due " + it.Due.Format("2006-01-02 15:04") + ")"
		}
		fmt.Printf("%s%s%s\n", mark, it.Text, note)
		fmt.Printf("    taken on %s\n", humanAge(it.Made, now))
		printGoalEvidence(it, now)
	}
	return nil
}

// printGoalEvidence prints everything the ghost is keeping about a goal.
//
// Not a debugging aid. The consent story for goals is that the ghost
// watches what you named, from when you named it, and nothing else — and a
// promise nobody can check is a promise. So this is the whole of it,
// verbatim: the words it is looking for, and the times it saw them. There
// is no fourth thing, and if there ever is, it belongs here too.
func printGoalEvidence(it ghost.Intention, now time.Time) {
	if !it.Goal() || it.Aside {
		return
	}
	if len(it.Watch) == 0 {
		fmt.Println("    watching for: nothing — it could not tell what to look for")
		return
	}
	fmt.Printf("    watching for: %s\n", strings.Join(it.Watch, ", "))
	switch last := it.LastSeen(); {
	case last.IsZero():
		fmt.Println("    seen: never, since it was taken on")
	default:
		fmt.Printf("    seen: %d times, last %s\n", len(it.Seen), humanAge(last, now))
	}
}

// humanAge says how long ago something was taken on, which is the thing a
// person reading this list actually wants to know.
func humanAge(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d/time.Hour))
	default:
		return t.Format("2006-01-02")
	}
}
