package ghost

import (
	"sync"
	"testing"
	"time"
)

// The whole point of counting the goroutines go_ starts: teardown has to
// let them finish before the store they write into closes. Untracked, a
// Ctrl-C in the second after a reply closed the store underneath the
// goroutine writing the memory of that reply.
func TestBackgroundWorkFinishesBeforeShutdown(t *testing.T) {
	var b background
	var mu sync.Mutex
	finished := false

	if !b.add() {
		t.Fatal("add refused before anything closed the door")
	}
	go func() {
		defer b.done()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		finished = true
		mu.Unlock()
	}()

	if done, left := b.wait(time.Second); !done || left != 0 {
		t.Fatalf("wait = (%v, %d), want it to have waited for the work", done, left)
	}
	mu.Lock()
	defer mu.Unlock()
	if !finished {
		t.Error("teardown carried on while the work was still running")
	}
}

// And the other half: a job offered after teardown has begun is refused
// rather than started, because there is nowhere left for its result to go.
func TestBackgroundWorkIsRefusedAfterShutdown(t *testing.T) {
	var b background
	if done, _ := b.wait(time.Second); !done {
		t.Fatal("waiting on nothing did not finish")
	}
	if b.add() {
		t.Error("a job started after the store began closing")
	}
}

// A wedged backend must not hold the terminal of somebody who just pressed
// Ctrl-C: whatever has not finished by the grace period is abandoned, and
// the count of what was abandoned is reported.
func TestBackgroundWorkIsAbandonedWhenItHangs(t *testing.T) {
	var b background
	release := make(chan struct{})
	for i := 0; i < 2; i++ {
		if !b.add() {
			t.Fatal("add refused")
		}
		go func() {
			defer b.done()
			<-release
		}()
	}
	start := time.Now()
	done, left := b.wait(50 * time.Millisecond)
	if done {
		t.Error("wait claimed the hung work had finished")
	}
	if left != 2 {
		t.Errorf("left = %d, want the 2 still running", left)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waited %v; the grace period did not bound it", elapsed)
	}
	close(release)
}
