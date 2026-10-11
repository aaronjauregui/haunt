package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/library"
	"github.com/aaronjauregui/haunt/internal/skill"
	"github.com/aaronjauregui/haunt/internal/store"
	"github.com/aaronjauregui/haunt/skills"
)

// cmdJournal prints a ghost's journal: the action log, a line for each thing
// it did. What it remembers is its memory files (`haunt memory`). -echoes
// lists what it saved there in the words of a page or a file it read, from
// its transcript. -skills lists the routines it has, and which of them it
// wrote itself. -style is the look its pictures have, and whether the
// ghost was asked for one of its own. -mood is the line it set about how
// it is feeling.
func cmdJournal(args []string) error {
	fs := flag.NewFlagSet("journal", flag.ExitOnError)
	state := fs.String("state", "", "override state directory")
	intentions := fs.Bool("intentions", false, "print what it is still meaning to do")
	echoes := fs.Bool("echoes", false, "list what it saved to memory in the words of a page or a file it read")
	listSkills := fs.Bool("skills", false, "list its skills: the ones it learned, the package's and haunt's")
	style := fs.Bool("style", false, "the style it draws in, and where that came from")
	reset := fs.Bool("reset", false, "with -style: go back to the style its package ships")
	mood := fs.Bool("mood", false, "the mood it set, and when")
	fs.Parse(args)

	stateDir, err := resolveStateDir(*state, fs.Args())
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}

	if *listSkills {
		name := filepath.Base(stateDir)
		if len(fs.Args()) == 1 {
			name = fs.Args()[0]
		}
		pkg, _ := library.PkgDir(name)
		return printSkills(os.Stdout, stateDir, pkg)
	}
	if *echoes {
		return printEchoes(os.Stdout, stateDir)
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
	if *mood {
		return printMood(os.Stdout, s, time.Now())
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

// printMood prints the ghost's mood and when it set it. A mood from an
// earlier day is printed too, marked as cleared: the ghost no longer has
// it in mind, but it is still the last one it set.
func printMood(w io.Writer, s *store.Store, now time.Time) error {
	raw, ok, err := s.Get(ghost.MoodStoreKey)
	if err != nil {
		return err
	}
	m := ghost.Mood{}
	if ok {
		if m, err = ghost.DecodeMood(raw); err != nil {
			return fmt.Errorf("the mood is corrupt: %w", err)
		}
	}
	if m.Text == "" {
		fmt.Fprintln(w, "(no mood set)")
		return nil
	}
	fmt.Fprintln(w, m.Text)
	if m.Today(now) {
		fmt.Fprintf(w, "  set %s\n", m.At.Format("2006-01-02 15:04"))
	} else {
		fmt.Fprintf(w, "  set %s, an earlier day; it has cleared\n", m.At.Format("2006-01-02 15:04"))
	}
	return nil
}

// printSkills lists a ghost's skills, grouped by where they came from: the
// ones it wrote itself first, since nobody reviewed them, then its
// package's, then haunt's. Reading the files is enough; the ghost need not
// be running.
func printSkills(w io.Writer, stateDir, pkgDir string) error {
	decisions := ghost.DecisionNames()
	groups := []struct {
		title   string
		fsys    fs.FS
		learned bool
	}{
		{"Learned (it wrote these itself)", os.DirFS(filepath.Join(stateDir, "skills")), true},
		{"From its package", nil, false},
		{"From haunt", skills.Haunt, false},
	}
	if pkgDir != "" {
		groups[1].fsys = os.DirFS(filepath.Join(pkgDir, "skills"))
	}
	for i, g := range groups {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, g.title+":")
		n := 0
		var entries []fs.DirEntry
		if g.fsys != nil {
			var err error
			entries, err = fs.ReadDir(g.fsys, ".")
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("journal: %w", err)
			}
		}
		for _, e := range entries {
			if !e.IsDir() || e.Name() == skill.ArchiveDir {
				continue
			}
			raw, err := fs.ReadFile(g.fsys, e.Name()+"/SKILL.md")
			if err != nil {
				continue
			}
			f, body, err := skill.Parse(raw)
			if err != nil {
				fmt.Fprintf(w, "  %s: ! cannot be read: %v\n", e.Name(), err)
				continue
			}
			n++
			fmt.Fprintf(w, "  %s", f.Name)
			if f.Description != "" {
				fmt.Fprintf(w, ": %s", f.Description)
			}
			fmt.Fprintln(w)
			if g.learned {
				var unknown []string
				for _, tool := range append(f.RequiresTools(), skill.ToolRefs(body)...) {
					if !slices.Contains(decisions, tool) && !slices.Contains(unknown, tool) && !strings.Contains(tool, "__") {
						unknown = append(unknown, tool)
					}
				}
				if len(unknown) > 0 {
					// A skill written for an agent with a shell is dead
					// weight here.
					fmt.Fprintf(w, "    ! names tools a ghost does not have: %s\n", strings.Join(unknown, ", "))
				}
			}
		}
		if n == 0 {
			fmt.Fprintln(w, "  (none)")
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
