package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/testenv"
	"github.com/aaronjauregui/haunt/internal/troupe"
)

// The supervisor decides *when* to bring a crashed ghost back, and that
// half is tested where the decision lives (internal/troupe). This is the
// other half, and the one that cannot be tested with a fake: that a second
// life is possible at all.
//
// A ghost holds a Wayland connection, a SQLite file, a control socket
// named after it and, if it declares any, tool server processes. Every one
// of those is released on the way out by a defer in ghost.Run, and every
// one of them has to be reacquirable — a restart that failed on "address
// already in use" would be a supervisor that turns one crash into a ghost
// that is gone for good and noisier about it.
func TestAGhostCanBeRunTwiceInOneProcess(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	t.Setenv("WAYLAND_DISPLAY", c.Display)
	t.Setenv("SWAYSOCK", c.Sock)
	t.Setenv("HAUNT_RUNTIME_DIR", filepath.Join(dir, "run"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))

	pkg := filepath.Join(dir, "pkg")
	writeGhost(t, pkg, `[ghost]
name = "revenant"
version = "0.1.0"
capabilities = ["message", "llm"]
`)

	// The same state directory both times, because a ghost that came back
	// having forgotten everything would be a new ghost wearing the name.
	state := filepath.Join(dir, "state")
	for life := 1; life <= 2; life++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		ready := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- troupe.Run(ctx, []troupe.Member{{
				Name: "revenant",
				Dir:  pkg,
				Opts: ghost.Options{
					StateDir: state,
					Logger:   hlog.Discard(),
					OnReady:  func() { close(ready) },
				},
			}}, troupe.Options{Logger: hlog.Discard()})
		}()

		select {
		case <-ready:
		case err := <-done:
			cancel()
			t.Fatalf("life %d never came up: %v", life, err)
		case <-time.After(30 * time.Second):
			cancel()
			t.Fatalf("life %d never came up", life)
		}

		cancel()
		if err := <-done; err != nil {
			t.Fatalf("life %d ended badly: %v", life, err)
		}
	}
}
