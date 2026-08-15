// Command haunt is the ghost driver CLI.
//
//	haunt run <ghost-dir>   run a ghost (requires a layer-shell compositor)
//	haunt new <name>        scaffold a new ghost project
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/aaronjauregui/haunt/internal/ghost"
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

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  haunt run [-x N] [-y N] [-record FILE] [-state DIR] [ghost-dir|name]
  haunt dev [-x N] [-y N] [-state DIR] <ghost-dir>
  haunt say [-ghost NAME] <text>
  haunt new <name>
  haunt keygen
  haunt pack [-o FILE] <ghost-dir>
  haunt install [-yes] <file.haunt>
  haunt ls | rm <name> | switch <name>
  haunt shush [duration] | unshush
  haunt journal [name | -state DIR]
  haunt history [-n N] [name | -state DIR]`)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	x := fs.Int("x", 100, "initial sprite x position")
	y := fs.Int("y", 100, "initial sprite y position")
	record := fs.String("record", "", "record the event session to FILE (JSONL)")
	state := fs.String("state", "", "override state directory")
	idleMS := fs.Uint("idle-ms", 0, "idle threshold in milliseconds (default 2 minutes)")
	fs.Parse(args)
	if fs.NArg() > 1 {
		return fmt.Errorf("run: at most one ghost directory or name expected")
	}

	opts := ghost.Options{
		X: *x, Y: *y,
		RecordTo:      *record,
		StateDir:      *state,
		IdleTimeoutMS: uint32(*idleMS),
		Log:           func(s string) { fmt.Fprintln(os.Stderr, s) },
		// Machine-readable readiness marker for scripts and tests.
		OnReady: func() { fmt.Println("READY") },
	}

	// The target is a directory (dev mode: declared capabilities granted),
	// or an installed ghost name / the current ghost (grants apply).
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
		grants, err := library.LoadGrants(name)
		if err != nil {
			return err
		}
		opts.Granted = grants.Granted
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return ghost.Run(ctx, dir, opts)
}
