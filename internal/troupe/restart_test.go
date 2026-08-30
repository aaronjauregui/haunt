package troupe

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/hlog"
)

// lives records what the supervisor asked of a fake ghost: one entry per
// time it was started, and what that life was told to do.
type lives struct {
	mu      sync.Mutex
	started []time.Time
	// script is consulted by life number, counting from 1. A life with no
	// entry left runs until the context ends.
	script []func(ready func()) error
	woken  chan struct{}
}

func newLives(script ...func(ready func()) error) *lives {
	return &lives{script: script, woken: make(chan struct{}, 64)}
}

// install replaces the supervisor's view of ghost.Run for one test.
func (l *lives) install(t *testing.T) {
	t.Helper()
	prev := runGhost
	runGhost = func(ctx context.Context, dir string, o ghost.Options) error {
		l.mu.Lock()
		n := len(l.started)
		l.started = append(l.started, time.Now())
		var act func(ready func()) error
		if n < len(l.script) {
			act = l.script[n]
		}
		l.mu.Unlock()
		select {
		case l.woken <- struct{}{}:
		default:
		}
		if act != nil {
			return act(o.OnReady)
		}
		if o.OnReady != nil {
			o.OnReady()
		}
		<-ctx.Done()
		return nil
	}
	t.Cleanup(func() { runGhost = prev })
}

func (l *lives) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.started)
}

// wait blocks until the ghost has been started n times.
func (l *lives) wait(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for l.count() < n {
		select {
		case <-l.woken:
		case <-deadline:
			t.Fatalf("ghost started %d times, wanted %d", l.count(), n)
		}
	}
}

// quick is a policy with the waits taken out, so a test that watches a
// ghost give up finishes in milliseconds rather than in a minute.
func quick(tries int) Restart {
	return Restart{
		Delay:    time.Millisecond,
		MaxDelay: 2 * time.Millisecond,
		Tries:    tries,
		Steady:   time.Hour,
	}
}

func member(name string) Member {
	return Member{Name: name, Dir: "/nonexistent", Opts: ghost.Options{}}
}

// The point of the whole thing: a ghost that was on screen and then died
// comes back. Before this, the supervisor contained the failure and left
// the desktop one companion short until the session restarted — which,
// since a ghost can carry an errand until four o'clock, is a meeting
// missed rather than a decoration absent.
func TestACrashedGhostComesBack(t *testing.T) {
	up := make(chan struct{}, 4)
	l := newLives(
		func(ready func()) error {
			ready()
			up <- struct{}{}
			return errors.New("the compositor went away")
		},
	)
	l.install(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []Member{member("wisp")}, Options{
			Logger: hlog.Discard(), Restart: quick(5),
		})
	}()

	<-up         // it lived
	l.wait(t, 2) // it died, and was started again
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("a ghost that recovered should not fail the session: %v", err)
	}
}

// A panic is a death like any other, and is restartable for the same
// reason: it is exactly the kind of failure that does not reproduce.
func TestAPanicIsRestarted(t *testing.T) {
	l := newLives(
		func(ready func()) error {
			ready()
			panic("slice bounds out of range")
		},
	)
	l.install(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []Member{member("wisp")}, Options{
			Logger: hlog.Discard(), Restart: quick(5),
		})
	}()
	l.wait(t, 2)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("a ghost that recovered from a panic should not fail the session: %v", err)
	}
}

// A ghost that never got as far as the screen is not restarted at all. A
// failure on the way up is a configuration failure — an unreadable
// manifest, a brain that is not there — and it reproduces itself
// identically however many times you try it.
func TestAGhostThatNeverLivedIsNotRestarted(t *testing.T) {
	l := newLives(
		func(ready func()) error { return errors.New("manifest: no such file") },
	)
	l.install(t)

	err := Run(context.Background(), []Member{member("wisp")}, Options{
		Logger: hlog.Discard(), Restart: quick(5),
	})
	if err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("failure %v, want the manifest error reported", err)
	}
	if n := l.count(); n != 1 {
		t.Errorf("started %d times, want exactly 1: a bad manifest is not worth retrying", n)
	}
}

// A ghost that comes back and dies again, over and over, is eventually
// left stopped and said so. A companion is not worth a boot loop.
func TestItGivesUp(t *testing.T) {
	always := func(ready func()) error {
		ready()
		return errors.New("dead again")
	}
	script := make([]func(func()) error, 20)
	for i := range script {
		script[i] = always
	}
	l := newLives(script...)
	l.install(t)

	err := Run(context.Background(), []Member{member("wisp")}, Options{
		Logger: hlog.Discard(), Restart: quick(3),
	})
	if err == nil || !strings.Contains(err.Error(), "would not stay up") {
		t.Fatalf("failure %v, want the session to report a ghost left stopped", err)
	}
	// The first life, plus the three restarts it was allowed.
	if n := l.count(); n != 4 {
		t.Errorf("started %d times, want 4: one life and %d restarts", n, 3)
	}
}

// A ghost that comes back and stays up has its failure count cleared, so
// one crash a week never adds up to being given up on. The measure of
// recovery is that it lived, not that it started.
func TestStayingUpClearsTheCount(t *testing.T) {
	die := func(ready func()) error {
		ready()
		return errors.New("dead")
	}
	// Two deaths, a life that lasts, then two more deaths. With three
	// tries and no clearing, the fifth start would never happen.
	l := newLives(die, die, func(ready func()) error {
		ready()
		time.Sleep(30 * time.Millisecond)
		return errors.New("dead after a while")
	}, die, die)
	l.install(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []Member{member("wisp")}, Options{
			Logger:  hlog.Discard(),
			Restart: Restart{Delay: time.Millisecond, MaxDelay: time.Millisecond, Tries: 3, Steady: 20 * time.Millisecond},
		})
	}()
	l.wait(t, 6)
	cancel()
	<-done
}

// Off is the supervisor as it behaved before any of this: the failure is
// contained, the others keep running, and the dead one stays dead.
func TestRestartCanBeTurnedOff(t *testing.T) {
	l := newLives(func(ready func()) error {
		ready()
		return errors.New("dead")
	})
	l.install(t)

	err := Run(context.Background(), []Member{member("wisp")}, Options{
		Logger: hlog.Discard(), Restart: Restart{Off: true},
	})
	if err == nil {
		t.Fatal("a dead ghost should still fail the session")
	}
	if n := l.count(); n != 1 {
		t.Errorf("started %d times, want 1", n)
	}
}

// One ghost dying does not disturb the others: the healthy one is started
// once and stays up, and the brittle one exhausts its restarts on its own.
func TestTheOthersAreUndisturbed(t *testing.T) {
	var mu sync.Mutex
	starts := map[string]int{}
	gaveUp := make(chan struct{})
	var once sync.Once
	prev := runGhost
	runGhost = func(ctx context.Context, dir string, o ghost.Options) error {
		mu.Lock()
		starts[dir]++
		n := starts[dir]
		mu.Unlock()
		if o.OnReady != nil {
			o.OnReady()
		}
		if dir == "brittle" {
			// One life and the two restarts it is allowed.
			if n == 3 {
				once.Do(func() { close(gaveUp) })
			}
			return errors.New("dead")
		}
		<-ctx.Done()
		return nil
	}
	t.Cleanup(func() { runGhost = prev })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []Member{
			{Name: "wisp", Dir: "steady"},
			{Name: "mayura", Dir: "brittle"},
		}, Options{
			Logger:    hlog.Discard(),
			StateRoot: t.TempDir(),
			Restart:   quick(2),
			OnReady:   func() { close(ready) },
		})
	}()

	// READY is one line for the session, and a ghost that has stopped
	// coming up counts as settled — so it is said even though one of the
	// two is on its way to being given up on.
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the session never reported itself ready")
	}
	select {
	case <-gaveUp:
	case <-time.After(5 * time.Second):
		t.Fatal("the brittle ghost never exhausted its restarts")
	}

	// The healthy ghost is still running: only cancelling ends the session.
	cancel()
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "mayura") {
		t.Fatalf("failure %v, want the lost ghost named", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if starts["steady"] != 1 {
		t.Errorf("the healthy ghost was started %d times, want 1", starts["steady"])
	}
	if starts["brittle"] != 3 {
		t.Errorf("the brittle ghost was started %d times, want 3", starts["brittle"])
	}
}

// Each ghost gets a seat of its own for each life. A returning ghost that
// reused the one it left with would be announcing into a queue nobody
// drains, and would see nothing its neighbours did ever again.
func TestARestartedGhostGetsAFreshSeat(t *testing.T) {
	stage := NewStage(hlog.Discard())
	first := stage.For("wisp")
	first.Leave()

	second := stage.For("wisp")
	got := newSeen()
	second.Observe(got.deliver)
	stage.For("mayura").Announce("spoke", "welcome back")
	got.wait(t, 1)
}

// The backoff doubles and then stops doubling, and no number of failures
// wraps it back round to no wait at all.
func TestBackoffDoublesUpToTheCap(t *testing.T) {
	r := Restart{Delay: time.Second, MaxDelay: 8 * time.Second}.settled()
	for n, want := range map[int]time.Duration{
		1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second,
		4: 8 * time.Second, 5: 8 * time.Second, 400: 8 * time.Second,
	} {
		if got := r.wait(n); got != want {
			t.Errorf("restart %d waits %s, want %s", n, got, want)
		}
	}
}

// The zero value is the policy, not an absence of one.
func TestZeroPolicyIsTheDefault(t *testing.T) {
	r := Restart{}.settled()
	if r.Delay != defaultDelay || r.MaxDelay != defaultMaxDelay ||
		r.Tries != defaultTries || r.Steady != defaultSteady {
		t.Errorf("zero policy settled to %+v, want the documented defaults", r)
	}
	// A cap below the first wait is not a cap.
	if got := (Restart{Delay: 10 * time.Second, MaxDelay: time.Second}).settled(); got.MaxDelay != got.Delay {
		t.Errorf("cap %s under a first wait of %s", got.MaxDelay, got.Delay)
	}
}
