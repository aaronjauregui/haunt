// Command haunt is the ghost driver CLI.
//
// docs/user/cli.md is the reference; usage() below is the short form.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/library"
	"github.com/aaronjauregui/haunt/internal/troupe"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("haunt: ")
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "new":
		err = cmdNew(os.Args[2:])
	case "shell":
		err = cmdShell(os.Args[2:])
	case "perms":
		err = cmdPerms(os.Args[2:])
	case "dev":
		err = cmdDev(os.Args[2:])
	case "say":
		err = cmdSay(os.Args[2:])
	case "keygen":
		err = cmdKeygen(os.Args[2:])
	case "pack":
		err = cmdPack(os.Args[2:])
	case "install":
		err = cmdInstall(os.Args[2:])
	case "ls":
		err = cmdLs(os.Args[2:])
	case "rm":
		err = cmdRm(os.Args[2:])
	case "switch":
		err = cmdSwitch(os.Args[2:])
	case "add":
		err = cmdAdd(os.Args[2:])
	case "drop":
		err = cmdDrop(os.Args[2:])
	case "shush":
		err = cmdShush(os.Args[2:])
	case "unshush":
		err = cmdUnshush(os.Args[2:])
	case "journal":
		err = cmdJournal(os.Args[2:])
	case "history":
		err = cmdHistory(os.Args[2:])
	case "memory":
		err = cmdMemory(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// isSet reports whether a flag was given on the command line, which is how
// an explicit position is told apart from a default one.
func isSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  haunt run [-x N] [-y N] [-output NAME] [-record FILE] [-state DIR] [-idle-ms N] [-goal-check-delay D] [ghost-dir|name ...]
  haunt dev [-x N] [-y N] [-state DIR] <ghost-dir>
  haunt say [-ghost NAME] [-lead WORDS] <text>
  haunt new [-brain] <name>
  haunt shell init [-C dir] [-art subdir] [-scale F] [-write] <art.ora>
  haunt shell check [-C dir] [-factor F] [-render DIR]
  haunt keygen
  haunt pack [-o FILE] <ghost-dir>
  haunt install [-yes] <file.haunt>
  haunt ls | rm <name>
  haunt switch <name> | add <name> | drop <name>
  haunt perms [name] | perms grant|revoke <capability> [name]
  haunt shush [duration] | unshush
  haunt journal [-intentions] [-echoes] [-skills] [-style [-reset]] [name | -state DIR]
  haunt history [-n N] [-why | -json] [name | -state DIR]
  haunt memory ls|show|rm|reindex [-ghost NAME | -state DIR] [topic [line-number]]`)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	x := fs.Int("x", 100, "sprite x position (default: where it was left)")
	y := fs.Int("y", 100, "sprite y position (default: where it was left)")
	output := fs.String("output", "", "screen to place the ghost on (default: where it was left)")
	record := fs.String("record", "", "record the event session to FILE (JSONL)")
	state := fs.String("state", "", "override state directory")
	idleMS := fs.Uint("idle-ms", 0, "idle threshold in milliseconds (default 2 minutes)")
	// The goal check is scheduled for a real working day, five minutes
	// after you walk away. Overridable for the same reason -idle-ms is; it
	// is a demo and debugging affordance, not a preference.
	goalDelay := fs.Duration("goal-check-delay", 0,
		"wait after the user goes idle before checking on goals (default 5m)")
	fs.Parse(args)

	// One logger for the session: level and topics from the user's config
	// and the environment (see internal/hlog).
	logger := hlog.Open(os.Stderr)

	// Who is running: the arguments, or the resident set. An argument may
	// be a directory (dev mode) or an installed name; the two are told
	// apart by looking.
	targets := fs.Args()
	if len(targets) == 0 {
		resident, err := library.Resident()
		if err != nil {
			return err
		}
		if len(resident) == 0 {
			return fmt.Errorf("no ghost is resident; haunt install one, or pass a directory")
		}
		targets = resident
	}

	// The single-ghost flags. Where a ghost opens, which file it records
	// to and which directory holds its state are all answers about one
	// ghost, and there is no sensible way to spread one answer over three.
	if len(targets) > 1 {
		for _, name := range []string{"x", "y", "output", "record", "state"} {
			if isSet(fs, name) {
				return fmt.Errorf("run: -%s takes one ghost; run it on its own, or drop the flag", name)
			}
		}
	}

	base := ghost.Options{
		X: *x, Y: *y,
		Placed:         isSet(fs, "x") || isSet(fs, "y"),
		Output:         *output,
		RecordTo:       *record,
		StateDir:       *state,
		IdleTimeoutMS:  uint32(*idleMS),
		GoalCheckDelay: *goalDelay,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var members []troupe.Member
	seen := map[string]bool{}
	for _, target := range targets {
		// A lone ghost's log lines are untagged, exactly as they were
		// before there could be more than one.
		tag := logger
		if len(targets) > 1 {
			tag = logger.Named(nameOf(target))
		}
		m, err := resolve(ctx, target, base, tag)
		if err != nil {
			return err
		}
		// A name twice over is one ghost: its store, its socket and its
		// brain are all single, and starting it twice would have the
		// second one fail on a socket the first one holds.
		if seen[m.Name] {
			continue
		}
		seen[m.Name] = true
		members = append(members, m)
	}

	root, err := library.Root()
	if err != nil {
		return err
	}
	return troupe.Run(ctx, members, troupe.Options{
		Logger:    logger,
		StateRoot: root,
		// Machine-readable readiness marker for scripts and tests: one
		// line for the session, once everyone is on screen.
		OnReady: func() { fmt.Println("READY") },
	})
}

// resolve turns one `haunt run` argument into a member of the troupe. A
// directory is a dev run with every declared capability granted; anything
// else is an installed ghost, whose grants are watched so `haunt perms`
// takes effect on the running session.
func resolve(ctx context.Context, target string, base ghost.Options, tag *hlog.Logger) (troupe.Member, error) {
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		m, err := ghost.LoadManifest(target)
		if err != nil {
			return troupe.Member{}, err
		}
		return troupe.Member{Name: m.Ghost.Name, Dir: target, Opts: base}, nil
	}

	name := target
	dir, err := library.PkgDir(name)
	if err != nil {
		return troupe.Member{}, err
	}
	opts := base
	if opts.StateDir == "" {
		// Explicitly rather than by ghost.Run's own default, so a library
		// moved with HAUNT_DATA_DIR takes its ghosts' memories with it.
		if opts.StateDir, err = library.GhostDir(name); err != nil {
			return troupe.Member{}, err
		}
	}

	// Watched rather than loaded once: `haunt perms revoke` must take
	// effect on the ghost that is already running, and the capability
	// check is per-intent already.
	watch, err := library.Watch(name)
	if err != nil {
		return troupe.Member{}, err
	}
	watch.OnChange = func(granted, revoked []string) {
		if len(granted) > 0 {
			tag.Info(hlog.Session, "granted: %s", strings.Join(granted, ", "))
		}
		if len(revoked) > 0 {
			tag.Info(hlog.Session, "revoked: %s — refused from now on", strings.Join(revoked, ", "))
		}
	}
	opts.Granted = watch.Granted
	opts.Refused = watch.Refused
	opts.Withdrawn = watch.Withdrawn
	// Consent asked for in the balloon is written where `haunt perms`
	// reads it: one place records what this ghost may do, however the
	// question was put.
	opts.Consent = func(cap string, allowed bool) error {
		grants, err := library.LoadGrants(name)
		if err != nil {
			return err
		}
		if allowed {
			grants.Allow(cap)
		} else {
			grants.Refuse(cap)
		}
		return library.SaveGrants(name, grants)
	}
	// Followed in the background so a change is noticed while the ghost
	// is idle, not at whatever moment it next checks something.
	go watch.Follow(ctx.Done())

	return troupe.Member{Name: name, Dir: dir, Opts: opts}, nil
}

// nameOf is the ghost's name for a `haunt run` argument, without loading
// anything it does not have to: a directory has to be read, a name is one.
func nameOf(target string) string {
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		if m, err := ghost.LoadManifest(target); err == nil {
			return m.Ghost.Name
		}
		return filepath.Base(target)
	}
	return target
}
