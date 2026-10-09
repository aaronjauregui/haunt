package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/hermes"
	"github.com/aaronjauregui/haunt/internal/library"
	"github.com/aaronjauregui/haunt/internal/store"
)

// cmdJournal prints a ghost's journal: the action log, a line for each thing
// it did. What a ghost on hermes remembers about the person is hermes
// memory, readable in the profile's memories/ (and editable by telling the
// ghost). -skills lists the routines it has on hermes, and which of them it
// wrote itself. -style is the look its pictures have, and whether the ghost
// was asked for one of its own.
func cmdJournal(args []string) error {
	fs := flag.NewFlagSet("journal", flag.ExitOnError)
	state := fs.String("state", "", "override state directory")
	intentions := fs.Bool("intentions", false, "print what it is still meaning to do")
	skills := fs.Bool("skills", false, "list its hermes skills: the ones it learned, the package's and haunt's")
	style := fs.Bool("style", false, "the style it draws in, and where that came from")
	reset := fs.Bool("reset", false, "with -style: go back to the style its package ships")
	fs.Parse(args)

	stateDir, err := resolveStateDir(*state, fs.Args())
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}

	if *skills {
		name := filepath.Base(stateDir)
		if len(fs.Args()) == 1 {
			name = fs.Args()[0]
		}
		return printSkills(name)
	}

	s, err := store.Open(filepath.Join(stateDir, "state.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	if *style {
		name := filepath.Base(stateDir)
		if len(fs.Args()) == 1 {
			name = fs.Args()[0]
		}
		return styleCmd(s, name, *reset)
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

// printSkills lists a ghost's hermes skills, grouped by where they came
// from. The learned ones come first: they are the ones nobody reviewed.
// Reading the profile is enough; the gateway need not be running.
func printSkills(name string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	list, err := cfg.Hermes.Skills(name)
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}
	groups := []struct{ source, title string }{
		{hermes.SkillLearned, "Learned (it wrote these itself)"},
		{"ghost", "From its package"},
		{"haunt", "From haunt"},
		{hermes.SkillHermes, "From hermes"},
	}
	for i, g := range groups {
		if i > 0 {
			fmt.Println()
		}
		fmt.Println(g.title + ":")
		n := 0
		for _, s := range list {
			if s.Source != g.source {
				continue
			}
			n++
			fmt.Printf("  %s", s.Name)
			if s.Description != "" {
				fmt.Printf(": %s", s.Description)
			}
			fmt.Println()
			if s.Source == hermes.SkillLearned {
				fmt.Printf("    %s\n", s.Dir)
				if len(s.Assumes) > 0 {
					// Hermes's review writes skills for an agent with a
					// shell; one that needs one is dead weight here.
					fmt.Printf("    ! names tools a ghost does not have: %s\n", strings.Join(s.Assumes, ", "))
				}
				if len(s.Deps) > 0 {
					// Hermes would install these when the skill is
					// viewed; a ghost's profile has installs off, so
					// a skill that needs them is dead weight here.
					fmt.Printf("    ! needs packages a ghost does not get: %s\n", strings.Join(s.Deps, ", "))
				}
			}
		}
		if n == 0 {
			fmt.Println("  (none)")
		}
	}
	return nil
}

// styleCmd prints the style a ghost draws in, or puts it back to its
// package's.
//
// The ghost can be asked for a standing change to its own look, which is
// the kind of thing a person says once and then lives with for months. So
// it is readable and undoable from the terminal, without asking the ghost:
// both what it is drawing in and what its author meant it to draw in, since
// a package that has been updated since may mean something else by now.
func styleCmd(s *store.Store, name string, reset bool) error {
	seed := ""
	if dir, err := library.PkgDir(name); err == nil {
		if m, err := ghost.LoadManifest(dir); err == nil {
			seed = m.Imagine.Style
		}
	}
	if reset {
		if err := s.Delete(ghost.StyleStoreKey); err != nil {
			return err
		}
		fmt.Println("It draws in its package's style again.")
	}
	raw, ok, err := s.Get(ghost.StyleStoreKey)
	if err != nil {
		return err
	}
	changed, decodeErr := ghost.StyleChange{}, error(nil)
	if ok {
		changed, decodeErr = ghost.DecodeStyle(raw)
		if decodeErr != nil {
			return fmt.Errorf("the style is corrupt: %w", decodeErr)
		}
	}
	switch {
	case changed.Style != "":
		fmt.Printf("Draws in: %s\n", changed.Style)
		fmt.Printf("  asked for on %s\n", changed.At.Format("2006-01-02 15:04"))
		switch {
		case seed == "":
			fmt.Println("  its package asks for no style of its own")
		case changed.Seed != seed:
			fmt.Printf("  its package asked for %q then, and asks for %q now\n", changed.Seed, seed)
		default:
			fmt.Printf("  its package asks for %q\n", seed)
		}
	case seed != "":
		fmt.Printf("Draws in: %s\n", seed)
		fmt.Println("  its package's own style, unchanged")
	default:
		fmt.Println("Draws in: nothing in particular")
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
// journal is what the ghost has done, and this is what it is still meaning
// to do. Both are the same question — what is my ghost carrying — asked
// about different tenses.
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
