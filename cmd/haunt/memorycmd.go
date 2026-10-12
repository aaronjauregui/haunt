package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/aaronjauregui/haunt/internal/control"
	"github.com/aaronjauregui/haunt/internal/library"
	"github.com/aaronjauregui/haunt/internal/memory"
	"github.com/aaronjauregui/haunt/internal/store"
)

const memoryUsage = `usage:
  haunt memory ls [-ghost NAME | -state DIR]
  haunt memory show [-ghost NAME | -state DIR] <topic>
  haunt memory rm [-ghost NAME | -state DIR] <topic> [line-number]
  haunt memory reindex [-ghost NAME | -state DIR]`

// cmdMemory reads and prunes a ghost's memory: the topic files under
// memory/ in its state directory. ls, show and rm work on the files
// alone, which the person may edit whether the ghost runs or not; a
// running ghost notices and reindexes. reindex rebuilds the search index
// in state.db, which has one writer: a running ghost is asked to do it
// over its control socket, and the file is opened here only when no
// socket answers.
func cmdMemory(args []string) error {
	return memoryMain(os.Stdout, args)
}

func memoryMain(out io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("memory: a subcommand is expected\n%s", memoryUsage)
	}
	verb := args[0]
	if verb == "-h" || verb == "-help" || verb == "--help" || verb == "help" {
		fmt.Fprintln(out, memoryUsage)
		return nil
	}
	fs := flag.NewFlagSet("memory "+verb, flag.ContinueOnError)
	fs.SetOutput(out)
	name := fs.String("ghost", "", "the ghost (default: the resident one)")
	state := fs.String("state", "", "override state directory")
	fs.Usage = func() { fmt.Fprintln(out, memoryUsage) }
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	ghostName, stateDir, err := memoryTarget(*name, *state)
	if err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	dir := filepath.Join(stateDir, "memory")
	rest := fs.Args()

	switch verb {
	case "ls":
		if len(rest) != 0 {
			return fmt.Errorf("memory ls takes no arguments")
		}
		return memoryLs(out, dir)
	case "show":
		if len(rest) != 1 {
			return fmt.Errorf("memory show: one topic expected")
		}
		return memoryShow(out, dir, rest[0])
	case "rm":
		switch len(rest) {
		case 1:
			if err := memory.RemoveTopic(dir, rest[0]); err != nil {
				return err
			}
			fmt.Fprintf(out, "removed topic %s\n", rest[0])
			return nil
		case 2:
			n, err := strconv.Atoi(rest[1])
			if err != nil || n < 1 {
				return fmt.Errorf("memory rm: line number %q is not a number from 1", rest[1])
			}
			f, err := memory.RemoveLine(dir, rest[0], n)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "removed %s %d: %s\n", rest[0], n, f.Line)
			return nil
		}
		return fmt.Errorf("memory rm: a topic and an optional line number expected")
	case "reindex":
		if len(rest) != 0 {
			return fmt.Errorf("memory reindex takes no arguments")
		}
		return memoryReindex(out, ghostName, stateDir, dir)
	}
	return fmt.Errorf("memory: unknown subcommand %q\n%s", verb, memoryUsage)
}

// memoryTarget resolves the ghost and its state directory the way
// journal and history do: -state names the directory, -ghost the
// installed ghost, and with neither it is the one resident ghost. The
// name is what the control socket is called.
func memoryTarget(name, state string) (ghostName, stateDir string, err error) {
	switch {
	case state != "":
		stateDir = state
		ghostName = name
		if ghostName == "" {
			ghostName = filepath.Base(filepath.Clean(state))
		}
	case name != "":
		ghostName = name
		stateDir, err = library.GhostDir(name)
	default:
		ghostName, err = soleResident("pass -ghost NAME or -state DIR")
		if err == nil {
			stateDir, err = library.GhostDir(ghostName)
		}
	}
	return ghostName, stateDir, err
}

func memoryLs(out io.Writer, dir string) error {
	topics, err := memory.Topics(dir)
	if err != nil {
		return err
	}
	if len(topics) == 0 {
		fmt.Fprintln(out, "(no topics)")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, t := range topics {
		fmt.Fprintf(tw, "%s\t%d\n", t.Name, t.Lines)
	}
	return tw.Flush()
}

func memoryShow(out io.Writer, dir, topic string) error {
	t, err := memory.ReadTopic(dir, topic)
	if err != nil {
		return err
	}
	head := t.Name
	if !t.Updated.IsZero() {
		head += "  (updated " + t.Updated.Local().Format("2006-01-02 15:04") + ")"
	}
	fmt.Fprintln(out, head)
	for i, f := range t.Facts {
		fmt.Fprintf(out, "%3d  %s  (%s)\n", i+1, f.Line, provText(f.Prov))
	}
	// The past is not numbered, so `memory rm <topic> N` counts facts
	// alone, as show numbers them.
	if len(t.Past) > 0 {
		fmt.Fprintln(out, "past:")
	}
	for _, p := range t.Past {
		prov := provText(p.Prov)
		if !p.Retired.IsZero() {
			prov += ", retired " + p.Retired.Format("2006-01-02")
		}
		fmt.Fprintf(out, "     %s  (%s)\n", p.Line, prov)
	}
	return nil
}

// provText is a fact's provenance as show prints it.
func provText(p memory.Provenance) string {
	s := p.Source
	if p.Turn != 0 {
		s += fmt.Sprintf(", turn %d", p.Turn)
	}
	if !p.At.IsZero() {
		s += ", " + p.At.Format("2006-01-02")
	}
	return s
}

// memoryReindex asks the running ghost, or rebuilds the index itself
// when nothing answers on the ghost's socket.
func memoryReindex(out io.Writer, ghostName, stateDir, dir string) error {
	reply, err := control.MemoryReindex(ghostName)
	if err == nil {
		if reply == "ok" || strings.HasPrefix(reply, "ok ") {
			fmt.Fprintf(out, "%s reindexed its memory: %s\n", ghostName, reply)
			return nil
		}
		return fmt.Errorf("memory reindex: %s is running and answered: %s", ghostName, reply)
	}
	if !control.IsNotRunning(err) {
		return fmt.Errorf("memory reindex: asking %s: %w", ghostName, err)
	}
	if _, err := os.Stat(stateDir); err != nil {
		return fmt.Errorf("memory reindex: no state for %s: %w", ghostName, err)
	}
	s, err := store.Open(filepath.Join(stateDir, "state.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	topics, lines, err := memory.Reindex(dir, s.DB())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "reindexed %d topics, %d lines\n", topics, lines)
	return nil
}
