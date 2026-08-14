package main

import (
	"fmt"
	"time"

	"github.com/aaronjauregui/haunt/internal/quiet"
)

// cmdShush mutes all ghosts for a duration (default 1h).
func cmdShush(args []string) error {
	d := time.Hour
	if len(args) > 1 {
		return fmt.Errorf("shush: at most one duration expected")
	}
	if len(args) == 1 {
		var err error
		if d, err = time.ParseDuration(args[0]); err != nil || d <= 0 {
			return fmt.Errorf("shush: bad duration %q (try 30m, 2h)", args[0])
		}
	}
	until, err := quiet.Shush(d)
	if err != nil {
		return err
	}
	fmt.Printf("ghosts shushed until %s\n", until.Format("15:04"))
	return nil
}

// cmdUnshush clears a manual mute.
func cmdUnshush(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("unshush takes no arguments")
	}
	if err := quiet.Unshush(); err != nil {
		return err
	}
	fmt.Println("ghosts unshushed")
	return nil
}
