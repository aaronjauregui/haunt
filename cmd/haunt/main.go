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
  haunt run [-x N] [-y N] [-record FILE] [-state DIR] <ghost-dir>
  haunt dev [-x N] [-y N] [-state DIR] <ghost-dir>
  haunt say [-ghost NAME] <text>
  haunt new <name>`)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	x := fs.Int("x", 100, "initial sprite x position")
	y := fs.Int("y", 100, "initial sprite y position")
	record := fs.String("record", "", "record the event session to FILE (JSONL)")
	state := fs.String("state", "", "override state directory")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("run: exactly one ghost directory expected")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return ghost.Run(ctx, fs.Arg(0), ghost.Options{
		X: *x, Y: *y,
		RecordTo: *record,
		StateDir: *state,
		Log:      func(s string) { fmt.Fprintln(os.Stderr, s) },
		// Machine-readable readiness marker for scripts and tests.
		OnReady: func() { fmt.Println("READY") },
	})
}
