package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/hlog"
)

// cmdDev runs a ghost and restarts it whenever its files change: edit the
// brain, save, and the ghost reloads in place with its state intact.
func cmdDev(args []string) error {
	fs := flag.NewFlagSet("dev", flag.ExitOnError)
	x := fs.Int("x", 100, "sprite x position (default: where it was left)")
	y := fs.Int("y", 100, "sprite y position (default: where it was left)")
	state := fs.String("state", "", "override state directory")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("dev: exactly one ghost directory expected")
	}
	dir := fs.Arg(0)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	for {
		snap, err := snapshotGhostFiles(dir)
		if err != nil {
			return err
		}

		runCtx, cancelRun := context.WithCancel(ctx)
		runDone := make(chan error, 1)
		go func() {
			runDone <- ghost.Run(runCtx, dir, ghost.Options{
				X: *x, Y: *y,
				Placed:   isSet(fs, "x") || isSet(fs, "y"),
				StateDir: *state,
				Logger:   hlog.Open(os.Stderr),
				OnReady:  func() { fmt.Println("READY") },
			})
		}()

		// Watch for changes or exit.
		changed := false
		var runErr error
	watch:
		for {
			select {
			case runErr = <-runDone:
				break watch
			case <-time.After(500 * time.Millisecond):
				cur, err := snapshotGhostFiles(dir)
				if err != nil {
					continue // transient: file mid-write
				}
				if !sameSnapshot(snap, cur) {
					changed = true
					fmt.Fprintln(os.Stderr, "haunt dev: change detected, reloading")
					cancelRun()
					runErr = <-runDone
					break watch
				}
			}
		}
		cancelRun()

		if ctx.Err() != nil {
			return nil // interrupted by the user
		}
		if !changed {
			// The ghost died on its own (e.g. brain crash): report and wait
			// for the next change instead of hot-looping.
			if runErr != nil {
				fmt.Fprintf(os.Stderr, "haunt dev: ghost exited: %v; waiting for changes\n", runErr)
			}
			if err := waitForChange(ctx, dir, snap); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "haunt dev: change detected, restarting")
		}
	}
}

func waitForChange(ctx context.Context, dir string, snap map[string]time.Time) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
			cur, err := snapshotGhostFiles(dir)
			if err != nil {
				continue
			}
			if !sameSnapshot(snap, cur) {
				return nil
			}
		}
	}
}

// snapshotGhostFiles records the mtime of every regular file in the ghost
// directory (excluding dotfiles and the brain's Go module internals, which
// churn during builds without changing the shipped artifacts).
func snapshotGhostFiles(dir string) (map[string]time.Time, error) {
	snap := map[string]time.Time{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name != "." && (name[0] == '.' || name == "brain") {
				return filepath.SkipDir
			}
			return nil
		}
		if name[0] == '.' {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		snap[path] = info.ModTime()
		return nil
	})
	return snap, err
}

func sameSnapshot(a, b map[string]time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || !bv.Equal(v) {
			return false
		}
	}
	return true
}
