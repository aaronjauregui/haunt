// Command haunt-poc renders a sprite as a draggable layer-shell overlay.
// It is the phase-1 proof of concept, normally run against a headless
// compositor (see internal/testenv); pass an explicit WAYLAND_DISPLAY to
// target a real session.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/aaronjauregui/haunt/internal/sprite"
	"github.com/aaronjauregui/haunt/internal/wlclient"
)

func main() {
	spritePath := flag.String("sprite", "", "PNG sprite to display (default: built-in placeholder ghost)")
	x := flag.Int("x", 100, "initial x position")
	y := flag.Int("y", 100, "initial y position")
	size := flag.Int("size", 200, "built-in sprite size (ignored with -sprite)")
	flag.Parse()

	if err := run(*spritePath, int32(*x), int32(*y), *size); err != nil {
		log.Fatal(err)
	}
}

func run(spritePath string, x, y int32, size int) error {
	var sp *sprite.Sprite
	if spritePath != "" {
		var err error
		if sp, err = sprite.Load(spritePath); err != nil {
			return err
		}
	} else {
		sp = sprite.Builtin(size)
	}

	app, err := wlclient.Connect()
	if err != nil {
		return err
	}
	defer app.Close()
	// Machine-readable event lines for tests and debugging.
	app.EventLog = func(line string) { fmt.Println("EVT", line) }

	surf, err := app.CreateSprite(sp, x, y)
	if err != nil {
		return err
	}
	defer surf.Destroy()

	if err := app.Roundtrip(); err != nil {
		return err
	}
	// Tests wait for this line before taking screenshots.
	fmt.Println("READY")

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	errc := make(chan error, 1)
	go func() {
		// The deferred app.Close races this loop during shutdown; the
		// library can panic on the closed connection, which is benign here.
		defer func() {
			if r := recover(); r != nil {
				errc <- fmt.Errorf("dispatch panic: %v", r)
			}
		}()
		for {
			if err := app.Dispatch(); err != nil {
				errc <- err
				return
			}
		}
	}()

	select {
	case <-sigc:
		return nil
	case err := <-errc:
		return fmt.Errorf("event loop: %w", err)
	}
}
