package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/aaronjauregui/haunt/internal/control"
	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/store"
)

// cmdCorrect tells a running ghost that something it believes is wrong.
//
// This is the verb `haunt journal -entities` was missing. That view was
// built on the principle that a person is entitled to read what their ghost
// thinks about them rather than infer it from behaviour — and until now it
// was a window onto a belief that would be kept anyway, which is worse than
// no window at all.
//
// It goes through the control socket rather than opening the database,
// because it has to: while a ghost is running its memory and entity index
// are resident and written through, so a second process writing an entity
// key would have that write silently overwritten by the next flush. The
// constraint and the right design point the same way, which is usually a
// sign the constraint was real.
//
//	haunt correct sam 3 "left Acme in March"   retire fact 3, record this
//	haunt correct sam "is back at Acme"        record this, retire nothing
//	haunt correct sam 3                        retire fact 3, say nothing
//
// The second form is not a lesser version of the first. A fact that has
// gone out of date is not a fact that was wrong, and being made to declare
// one as the other would make the record say something untrue.
func cmdCorrect(args []string) error {
	fs := flag.NewFlagSet("correct", flag.ExitOnError)
	name := fs.String("ghost", "", "target ghost name (default: the one you last interacted with)")
	fs.Parse(args)

	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("correct: a subject expected (haunt journal -entities lists them)")
	}
	subject := rest[0]
	rest = rest[1:]

	// A leading number is the fact being retired. Only leading: a
	// replacement that happens to start with a digit is still a
	// replacement, so "correct sam 3 2024 was the year" retires 3.
	factID := 0
	if len(rest) > 0 {
		if n, err := strconv.Atoi(rest[0]); err == nil && n > 0 {
			factID, rest = n, rest[1:]
		}
	}
	truth := strings.TrimSpace(strings.Join(rest, " "))
	if factID == 0 && truth == "" {
		return fmt.Errorf("correct: say what is true, or give the number of the belief to retire")
	}

	target := *name
	if target == "" {
		var err error
		if target, err = sayTarget(); err != nil {
			return err
		}
	}
	if err := control.Send(target, proto.Event{
		Type: proto.EventCorrect,
		Name: subject,
		Fact: factID,
		Text: truth,
	}); err != nil {
		return err
	}

	// And then show what it thinks now, because the flow this belongs to is
	// see what it believes, say that is wrong, watch it stop believing it —
	// and the third step is the one that makes the second worth doing. Read
	// through the store, which the ghost has already written through.
	return showSubject(target, subject)
}

// showSubject prints what a ghost now believes about one subject. Failures
// here are reported and not returned: the correction has already landed,
// and telling somebody their correction failed because the display did
// would be a lie.
func showSubject(target, subject string) error {
	dir, err := resolveStateDir("", []string{target})
	if err != nil {
		fmt.Printf("(corrected; could not open the store to show it: %v)\n", err)
		return nil
	}
	s, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		fmt.Printf("(corrected; could not open the store to show it: %v)\n", err)
		return nil
	}
	defer s.Close()

	all, err := s.All()
	if err != nil {
		fmt.Printf("(corrected; could not read the store to show it: %v)\n", err)
		return nil
	}
	want := strings.ToLower(strings.TrimSpace(subject))
	for k, v := range all {
		if !strings.HasPrefix(k, ghost.EntityKeyPrefix) {
			continue
		}
		e, err := ghost.DecodeEntity(v)
		if err != nil || e.Name == "" || strings.ToLower(e.Name) != want {
			continue
		}
		printEntity(e, false)
		return nil
	}
	fmt.Printf("(corrected; %s is not in the index)\n", subject)
	return nil
}

// printEntity prints one subject the way both `haunt journal -entities` and
// a correction's confirmation want it: the standing beliefs numbered, so
// there is something to point `haunt correct` at, and what has been retired
// underneath when asked for.
//
// Numbers are the fact's own id rather than its position in the list. A
// position would be a moving target — the caps trim the list from the front
// — so the number somebody read a moment ago would eventually name a
// different belief, which is a bad property for the argument to a command
// that retires things.
func printEntity(e ghost.Entity, history bool) {
	who := e.Name
	if e.Kind != "" {
		who += " (" + e.Kind + ")"
	}
	fmt.Printf("%s  last mentioned %s\n", who, e.Last.Format("2006-01-02"))
	for _, f := range e.Facts {
		if !f.Standing() {
			continue
		}
		told := ""
		if f.From == ghost.FactPerson {
			told = "  (you told it)"
		}
		fmt.Printf("  %3d  %s%s\n", f.ID, f.Text, told)
	}
	if !history {
		return
	}
	for _, f := range e.Facts {
		if f.Standing() {
			continue
		}
		fmt.Printf("       - no longer: %s\n", f.Text)
	}
}

// sortEntities orders subjects for display, most-talked-about first.
func sortEntities(ents []ghost.Entity) {
	sort.SliceStable(ents, func(i, j int) bool { return ents[i].Mentions > ents[j].Mentions })
}
