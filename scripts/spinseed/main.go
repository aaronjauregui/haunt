// Command spinseed prepares a ghost's state for scripts/hermes-spin.sh: it
// writes a goal taken on eight days ago and never seen since, so the
// driver's goal check finds it gone quiet within the spin instead of a
// week later (goalMinAge and driftAfter in internal/ghost/goal.go).
// Dev-only; it writes the store directly, so the ghost must not be running.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/store"
)

func main() {
	name := flag.String("ghost", "wisp", "the ghost, whose state is <root>/<ghost>")
	text := flag.String("goal", "finish the thesis", "the goal's text")
	watch := flag.String("watch", "thesis", "comma-separated words to look for on screen")
	age := flag.Duration("age", 8*24*time.Hour, "how long ago the goal was taken on")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spinseed [flags] <state root>")
		os.Exit(2)
	}
	if err := seed(filepath.Join(flag.Arg(0), *name), *text, strings.Split(*watch, ","), *age); err != nil {
		fmt.Fprintln(os.Stderr, "spinseed:", err)
		os.Exit(1)
	}
}

func seed(state, text string, watch []string, age time.Duration) error {
	if err := os.MkdirAll(state, 0o700); err != nil {
		return err
	}
	s, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	list := []ghost.Intention{{
		Name:  "hermes/spin-goal",
		Text:  text,
		Made:  time.Now().Add(-age),
		Watch: watch,
		Asked: true,
	}}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.Set(ghost.IntentionsStoreKey, string(b))
}
