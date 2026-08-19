// Command haunt is the ghost driver CLI.
//
//	haunt run <ghost-dir>   run a ghost (requires a layer-shell compositor)
//	haunt new <name>        scaffold a new ghost project (art + persona)
//	haunt shell init <ora>  build a shell from a layered drawing
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/library"
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
	case "shush":
		err = cmdShush(os.Args[2:])
	case "unshush":
		err = cmdUnshush(os.Args[2:])
	case "journal":
		err = cmdJournal(os.Args[2:])
	case "history":
		err = cmdHistory(os.Args[2:])
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
  haunt run [-x N] [-y N] [-output NAME] [-record FILE] [-state DIR] [-idle-ms N] [ghost-dir|name]
  haunt dev [-x N] [-y N] [-state DIR] <ghost-dir>
  haunt say [-ghost NAME] <text>
  haunt new [-brain] <name>
  haunt shell init [-C dir] [-write] <art.ora>
  haunt keygen
  haunt pack [-o FILE] <ghost-dir>
  haunt install [-yes] <file.haunt>
  haunt ls | rm <name> | switch <name>
  haunt perms [name] | perms grant|revoke <capability> [name]
  haunt shush [duration] | unshush
  haunt journal [-entities] [name | -state DIR]
  haunt history [-n N] [name | -state DIR]`)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	x := fs.Int("x", 100, "sprite x position (default: where it was left)")
	y := fs.Int("y", 100, "sprite y position (default: where it was left)")
	output := fs.String("output", "", "screen to place the ghost on (default: where it was left)")
	record := fs.String("record", "", "record the event session to FILE (JSONL)")
	state := fs.String("state", "", "override state directory")
	idleMS := fs.Uint("idle-ms", 0, "idle threshold in milliseconds (default 2 minutes)")
	// Consolidation is scheduled for a real working day: five minutes after
	// you walk away, over memories at least two days old. Both are right and
	// both make it unwatchable, so they are overridable for the same reason
	// -idle-ms is — you cannot debug a behaviour you have to wait two days
	// to see. Neither belongs in the config file: this is a demo and
	// debugging affordance, not a preference.
	sleepAfter := fs.Duration("consolidate-delay", 0,
		"wait after the user goes idle before consolidating memories (default 5m)")
	sleepAge := fs.Duration("consolidate-after", 0,
		"how old a memory must be to be consolidated (default 48h)")
	fs.Parse(args)
	if fs.NArg() > 1 {
		return fmt.Errorf("run: at most one ghost directory or name expected")
	}

	// One logger for the session: level and topics from the user's config
	// and the environment (see internal/hlog).
	logger := hlog.Open(os.Stderr)
	opts := ghost.Options{
		X: *x, Y: *y,
		Placed:           isSet(fs, "x") || isSet(fs, "y"),
		Output:           *output,
		RecordTo:         *record,
		StateDir:         *state,
		IdleTimeoutMS:    uint32(*idleMS),
		ConsolidateDelay: *sleepAfter,
		ConsolidateAfter: *sleepAge,
		Logger:           logger,
		// Machine-readable readiness marker for scripts and tests.
		OnReady: func() { fmt.Println("READY") },
	}

	// The target is a directory (dev mode: declared capabilities granted),
	// or an installed ghost name / the current ghost (grants apply).
	var watch *library.GrantWatch
	target := fs.Arg(0)
	dir := target
	if info, err := os.Stat(target); target == "" || err != nil || !info.IsDir() {
		name := target
		if name == "" {
			if name, err = library.Current(); err != nil || name == "" {
				return fmt.Errorf("no ghost selected; haunt install one or pass a directory")
			}
		}
		if dir, err = library.PkgDir(name); err != nil {
			return err
		}
		// Watched rather than loaded once: `haunt perms revoke` must take
		// effect on the ghost that is already running, and the capability
		// check is per-intent already.
		watch, err = library.Watch(name)
		if err != nil {
			return err
		}
		watch.OnChange = func(granted, revoked []string) {
			if len(granted) > 0 {
				logger.Info(hlog.Session, "granted: %s", strings.Join(granted, ", "))
			}
			if len(revoked) > 0 {
				logger.Info(hlog.Session, "revoked: %s — refused from now on", strings.Join(revoked, ", "))
			}
		}
		opts.Granted = watch.Granted
		opts.Refused = watch.Refused
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
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if watch != nil {
		// Followed in the background so a change is noticed while the ghost
		// is idle, not at whatever moment it next checks something.
		go watch.Follow(ctx.Done())
	}
	return ghost.Run(ctx, dir, opts)
}
