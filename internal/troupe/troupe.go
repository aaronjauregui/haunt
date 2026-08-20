// Package troupe runs the resident ghosts. One ghost is a session; several
// are a household, and the difference is not N copies of the same loop.
//
// What is shared and what is not was the whole design question. Each ghost
// keeps its own Wayland connection, event bus, store, brain process and
// surfaces: the driver already serialises everything behind one lock per
// connection, so sharing one would put every ghost behind the same lock and
// make one crashed ghost the end of all of them. A connection each costs a
// socket and buys isolation.
//
// Five things are genuinely shared, because they are properties of the
// room rather than of a ghost: where everyone is standing (nobody opens on
// top of anybody), the ceiling on unprompted action, the hourly ration of
// backend calls (between them, those two are what stops N ghosts
// multiplying the user's bill by N), the log destination, and the stage —
// what each ghost does in public, fanned out to the others so they can
// react to each other (stage.go). Quiet hours,
// the fullscreen rule and reduced motion were global before this and stay
// global: they are answers about the room, and each ghost reads them for
// itself from the same config.
package troupe

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/aaronjauregui/haunt/internal/ai"
	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/hlog"
)

// Member is one ghost to run: where its package is, and how it is
// configured. The supervisor fills in the shared parts of Opts itself.
type Member struct {
	// Name is the ghost's name, used to tag its log lines and to key its
	// place in the room. It should match the manifest.
	Name string
	// Dir is the package directory.
	Dir string
	// Opts is everything the caller decided: state directory, grants,
	// consent, the debugging overrides.
	Opts ghost.Options
}

// Options configures the supervisor.
type Options struct {
	// Logger is the shared destination. Each ghost gets a logger tagged
	// with its name when there is more than one, so a line can be
	// attributed.
	Logger *hlog.Logger
	// StateRoot is where the shared spend log lives — the library root.
	StateRoot string
	// OnReady is called once, when every ghost that is going to come up
	// has come up. A ghost that fails before it is ready counts as
	// settled: the others should not be held back by it.
	OnReady func()
}

// Run starts every member and blocks until ctx is cancelled or they have
// all stopped. A ghost that fails is reported and the rest keep running —
// that is the point of a connection each. The returned error joins
// whatever went wrong, so a session that lost one of three ghosts is not
// silently a success.
func Run(ctx context.Context, members []Member, opts Options) error {
	if len(members) == 0 {
		return errors.New("no ghosts to run")
	}
	log := opts.Logger
	if log == nil {
		log = hlog.Discard()
	}

	// One ghost is exactly what it was before this package existed: no
	// name on its log lines, nobody to avoid on screen, no ceiling above
	// its own allowance. Everything below is what having company costs.
	solo := len(members) == 1

	var room *Room
	var stage *Stage
	var budget *Budget
	var rations *ai.Budgets
	if !solo {
		room = NewRoom()
		// Ghosts can see each other. Only ghosts this supervisor is
		// running: two `haunt run` processes started separately share a
		// screen but not a stage, which is the same boundary the room
		// draws around positions and the ceiling draws around budgets.
		stage = NewStage(log)
		// The hourly rate limits are the user's money, so they are one
		// ration for the household rather than one each: three ghosts with
		// a private 120 LLM calls an hour is 360 calls an hour, and the
		// curiosity ceiling above does not bound them, since a ghost being
		// *talked to* costs money too.
		aiCfg, err := ai.LoadConfig()
		if err != nil {
			return err
		}
		rations = ai.NewBudgets(aiCfg.Budget)
		perGhost, configured, err := ghost.Curiosity()
		if err != nil {
			return err
		}
		limit := Ceiling(perGhost, configured, len(members))
		budget = NewBudget(opts.StateRoot, limit, log)
		log.Info(hlog.Curiosity, "%d ghosts resident; the room may act unprompted %d times a day",
			len(members), limit)
	}

	// READY is one line for the session, not one per ghost: it means the
	// user's desktop is populated.
	var readyOnce sync.Once
	var readyMu sync.Mutex
	settled := 0
	settle := func() {
		readyMu.Lock()
		settled++
		all := settled == len(members)
		readyMu.Unlock()
		if all && opts.OnReady != nil {
			readyOnce.Do(opts.OnReady)
		}
	}

	var wg sync.WaitGroup
	errs := make([]error, len(members))
	for i, m := range members {
		i, m := i, m
		o := m.Opts
		o.Logger = log
		if !solo {
			o.Logger = log.Named(m.Name)
			o.Spots = room.For(m.Name)
			o.Stage = stage.For(m.Name)
			o.Initiative = func(what string) bool { return budget.Spend(m.Name, what) }
			o.AIBudgets = rations
		}
		// Every ghost reports its own arrival; the caller hears about it
		// once, when the last of them has landed.
		userReady := m.Opts.OnReady
		var once sync.Once
		o.OnReady = func() {
			if userReady != nil {
				userReady()
			}
			once.Do(settle)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			// A ghost that panics costs a ghost. Without this the panic
			// walks up through the supervisor and takes the household with
			// it, which is the failure mode a connection each was chosen to
			// avoid.
			defer func() {
				if r := recover(); r != nil {
					errs[i] = fmt.Errorf("ghost %q panicked: %v\n%s", m.Name, r, debug.Stack())
					log.Error(hlog.Session, "%v", errs[i])
				}
				// Whether it panicked or returned, it is no longer coming
				// up, and the others must not wait on it.
				once.Do(settle)
			}()
			if err := ghost.Run(ctx, m.Dir, o); err != nil {
				errs[i] = fmt.Errorf("ghost %q: %w", m.Name, err)
				if !solo {
					log.Error(hlog.Session, "%v", errs[i])
				}
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
