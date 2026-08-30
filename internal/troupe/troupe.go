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
	"time"

	"github.com/aaronjauregui/haunt/internal/ai"
	"github.com/aaronjauregui/haunt/internal/config"
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
	// Restart governs bringing a crashed ghost back. The zero value is
	// the policy described in restart.go, which is what a session wants.
	Restart Restart
}

// runGhost is ghost.Run, indirected so that the supervisor's own
// behaviour — when it restarts, how long it waits, when it gives up — can
// be tested without a compositor to crash. Nothing else reassigns it.
var runGhost = ghost.Run

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
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		rations = ai.NewBudgets(cfg.AI.Budget)
		perGhost, configured := cfg.Allowances()
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
			// The room and the ceiling outlive any one life of the ghost:
			// Release and Claim are a pair the driver calls on its own way
			// out and in, so a ghost that comes back reclaims its spot
			// rather than needing a new handle. The stage is the exception,
			// and tend takes a fresh seat for each life — see there.
			o.Spots = room.For(m.Name)
			o.Initiative = func(what string) bool { return budget.Spend(m.Name, what) }
			o.AIBudgets = rations
		}
		// Every ghost reports its own arrival; the caller hears about it
		// once, when the last of them has landed. A ghost that comes back
		// after a crash arrives again — the per-ghost callback fires, and
		// the session-wide one does not, because the desktop was already
		// populated and READY is not a thing to say twice.
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
			// Whether it lived, died or was given up on, it is no longer
			// coming up, and the others must not wait on it.
			defer once.Do(settle)
			if err := tend(ctx, m, o, tending{
				log:    log,
				solo:   solo,
				stage:  stage,
				policy: opts.Restart,
			}); err != nil {
				errs[i] = err
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// tending is the part of the session one ghost's supervision needs.
type tending struct {
	log *hlog.Logger
	// solo suppresses the line for a final failure, which the caller is
	// about to print for itself. Restarts are logged either way: a lone
	// ghost that vanished and came back is a thing the person should be
	// able to find afterwards.
	solo bool
	// stage is the fan-out to take a seat on, or nil when there is nobody
	// to be seen by.
	stage  *Stage
	policy Restart
}

// tend runs one ghost for as long as the session lasts, bringing it back
// when it dies. It returns the failure the ghost was finally left with, or
// nil if it was still running when the session ended. See restart.go for
// which deaths are worth answering and why.
func tend(ctx context.Context, m Member, o ghost.Options, t tending) error {
	policy := t.policy.settled()
	arrived := o.OnReady

	// failures counts consecutive deaths, and is what the backoff and the
	// giving up are both computed from. everUp is the line between a ghost
	// that died and one that never lived.
	failures := 0
	everUp := false

	for {
		// A seat per life. Leave nils the queue for good, so a returning
		// ghost needs a new one; taking it before the ghost boots rather
		// than when it starts listening is what makes a neighbour's
		// greeting wait for it (see Stage.For). ghost.Run leaves the stage
		// on its own way out, but a ghost that fails or panics before it
		// ever gets there would leave a seat the others go on announcing
		// into, so vacating it is this loop's job. Leaving twice is
		// harmless.
		var seat *Seat
		if t.stage != nil {
			seat = t.stage.For(m.Name)
			o.Stage = seat
		}
		up := false
		o.OnReady = func() {
			up, everUp = true, true
			if arrived != nil {
				arrived()
			}
		}

		began := time.Now()
		err := live(ctx, m, o)
		if seat != nil {
			seat.Leave()
		}

		// A ghost that stopped because it was asked to has not died, and
		// the session is over anyway.
		if ctx.Err() != nil {
			return final(m.Name, err, t)
		}
		if err == nil {
			// ghost.Run returns only when the context ends or the session
			// fails, so this is unreachable — and the shape of thing that
			// stops being unreachable one refactor from now, at which
			// point a ghost would silently disappear.
			err = errors.New("stopped without being asked to")
		}

		// A ghost that never got as far as the screen is not brought
		// back. Whatever is wrong with it will be just as wrong in two
		// seconds, and five identical lines in the log is not a diagnosis.
		if !everUp {
			return final(m.Name, err, t)
		}
		if policy.Off {
			return final(m.Name, err, t)
		}

		if up && time.Since(began) >= policy.Steady {
			// It came back and stayed up. The count is about a ghost that
			// cannot hold itself together, and this one could.
			failures = 0
		}
		failures++
		if failures > policy.Tries {
			return final(m.Name, fmt.Errorf("would not stay up; left stopped after %d restarts: %w",
				policy.Tries, err), t)
		}

		wait := policy.wait(failures)
		t.log.Error(hlog.Session, "ghost %q died: %v — bringing it back in %s (restart %d of %d)",
			m.Name, err, wait, failures, policy.Tries)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// final reports the failure a ghost is being left with, logging it on the
// way past so that one ghost lost out of three is visible in the session's
// log and not only in the error the caller eventually joins.
func final(name string, err error, t tending) error {
	if err == nil {
		return nil
	}
	err = fmt.Errorf("ghost %q: %w", name, err)
	if !t.solo {
		t.log.Error(hlog.Session, "%v", err)
	}
	return err
}

// live runs the ghost once, turning a panic into an ordinary error.
//
// A ghost that panics costs a ghost: without this the panic walks up
// through the supervisor and takes the household with it, which is the
// failure mode a connection each was chosen to avoid. Since this became a
// restart loop it does a second thing — a panic arrives back in tend
// looking exactly like any other death, so it is answered like one.
func live(ctx context.Context, m Member, o ghost.Options) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panicked: %v\n%s", r, debug.Stack())
		}
	}()
	return runGhost(ctx, m.Dir, o)
}
