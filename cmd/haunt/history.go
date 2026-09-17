package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/hermes"
	"github.com/aaronjauregui/haunt/internal/store"
)

// cmdHistory prints a ghost's recent conversation. The balloon is
// transient — a long reply pages away, and looking up a moment too late
// means it is gone — so the words have to be readable somewhere that keeps
// them. A ghost's conversation is its hermes session; this is the window
// onto it.
func cmdHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	state := fs.String("state", "", "override state directory")
	n := fs.Int("n", 10, "how many turns to show, most recent last")
	why := fs.Bool("why", false, "show what went into each reply: what woke the ghost, what was on screen, what it did")
	fs.Parse(args)

	stateDir, err := resolveStateDir(*state, fs.Args())
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}

	s, err := store.Open(filepath.Join(stateDir, "state.db"))
	if err != nil {
		return err
	}
	defer s.Close()

	// The conversation is kept by hermes, and the store holds only which
	// session it is. No session means the ghost has never spoken (or is
	// scripted, and keeps no conversation).
	var turns []hermes.Message
	if session, onHermes, err := s.Get(ghost.HermesSessionStoreKey); err != nil {
		return err
	} else if onHermes {
		name := filepath.Base(stateDir)
		if len(fs.Args()) == 1 {
			name = fs.Args()[0]
		}
		if turns, err = hermesHistory(name, session, *n); err != nil {
			return fmt.Errorf("history: %w", err)
		}
	}
	if len(turns) == 0 {
		fmt.Println("(nothing has been said yet)")
		return nil
	}
	if *n > 0 && len(turns) > *n {
		turns = turns[len(turns)-*n:]
	}
	// The reasons, if they were asked for. Loaded once and matched to the
	// turns below rather than looked up per line: they are a short ring of
	// their own and may not cover the whole window.
	var traces []ghost.Why
	if *why {
		if raw, ok, err := s.Get(ghost.WhyStoreKey); err != nil {
			return err
		} else if ok {
			if traces, err = ghost.DecodeWhy(raw); err != nil {
				return fmt.Errorf("the reply trace is corrupt: %w", err)
			}
		}
	}

	for _, t := range turns {
		who := "you"
		switch {
		case t.Role == "assistant":
			who = "ghost"
		case hermes.IsEnvelope(t.Text):
			// The desktop, not the person: what woke the ghost.
			who = "event"
			t.Text = strings.TrimSpace(strings.TrimPrefix(t.Text, hermes.EnvelopePrefix))
		}
		// Indent continuations so a multi-paragraph reply stays legible.
		text := strings.ReplaceAll(strings.TrimSpace(t.Text), "\n", "\n       ")
		if who == "ghost" && hermes.Silent(t.Text) {
			// Hermes keeps the token as the reply; nothing was shown.
			text = "(said nothing)"
		}
		fmt.Printf("%-6s %s\n", who+":", text)
		if !*why || t.Role != "assistant" {
			continue
		}
		w, ok := traceFor(traces, t.Text)
		if !ok {
			fmt.Println("       (no reasons kept for this one — it is older than the trace)")
			continue
		}
		printWhy(w)
	}
	return nil
}

// hermesHistory reads a ghost's conversation from its hermes gateway, which
// has to be running: the session lives in hermes's own database, and haunt
// reads it only through hermes.
func hermesHistory(name, session string, n int) ([]hermes.Message, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	p, err := cfg.Hermes.OpenProfile(name)
	if err != nil {
		return nil, err
	}
	c := &hermes.Client{BaseURL: p.URL(), Key: p.Key, Ghost: name,
		LoadSession: func() string { return session }}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Health(ctx); err != nil {
		return nil, fmt.Errorf("%s's hermes gateway is not running, and the conversation is kept there (%v)", name, err)
	}
	// Twice as many messages as turns: a turn is a line each way.
	return c.Messages(ctx, 2*n)
}

// traceFor finds the reasons behind a reply, and consumes the match so two
// identical replies get their own record rather than sharing the first.
func traceFor(traces []ghost.Why, reply string) (ghost.Why, bool) {
	for i, w := range traces {
		if w.Reply == reply {
			// Blanked rather than removed: the slice is walked in order and
			// removing would shift everything after it.
			traces[i].Reply = "\x00consumed"
			return w, true
		}
	}
	return ghost.Why{}, false
}

// printWhy renders one reply's reasons under the line it explains. What
// hermes recalled and was prompted with is in hermes's session, not here.
func printWhy(w ghost.Why) {
	const indent = "       "
	switch {
	case w.Suppressed:
		fmt.Println(indent + "· woke, and chose not to speak")
	case w.SelfPrompt:
		fmt.Println(indent + "· spoke first — nobody asked")
	}
	if !w.Window.Empty() {
		what := w.Window.App
		if w.Window.Title != "" {
			what += " — " + w.Window.Title
		}
		fmt.Println(indent + "· on screen at the time: " + what)
	}
	if len(w.Acted) > 0 {
		fmt.Printf("%s· did something first: %s\n", indent, strings.Join(w.Acted, ", "))
	}
}
