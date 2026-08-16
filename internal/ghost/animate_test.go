package ghost

import (
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/bus"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/shell"
)

// animatedGhost is a ghost with the built-in shell and its clips, driven
// without a compositor.
func animatedGhost(t *testing.T, reducedMotion bool) *Ghost {
	t.Helper()
	g := &Ghost{
		opts:          Options{},
		log:           hlog.Discard(),
		manifest:      &Manifest{Ghost: GhostSection{Name: "test"}},
		shell:         shell.Builtin(64),
		bus:           bus.New(64),
		done:          make(chan struct{}),
		reducedMotion: reducedMotion,
	}
	g.curiosity = newCuriosity(0, &memStore{m: map[string]string{}}, g.log)
	g.startAnimation()
	t.Cleanup(func() {
		g.stopAnimation()
		close(g.done)
	})
	return g
}

// mouthNow reports the variant the mouth layer is currently showing.
func mouthNow(g *Ghost) string {
	g.shellMu.Lock()
	defer g.shellMu.Unlock()
	return g.shell.State().Selection()["mouth"]
}

// TestTalkingMovesTheMouth: a `talk` clip runs for as long as words are
// arriving, and hands the mouth back to the pose when they stop.
func TestTalkingMovesTheMouth(t *testing.T) {
	g := animatedGhost(t, false)
	g.setExpression("happy") // pose mouth: smile
	if got := mouthNow(g); got != "smile" {
		t.Fatalf("pose mouth = %q, want smile", got)
	}

	g.startTalking()
	if got := mouthNow(g); got != "open" {
		t.Errorf("mouth while talking = %q, want it open", got)
	}

	// It keeps going: the clip loops rather than running once.
	time.Sleep(500 * time.Millisecond)
	g.anim.mu.Lock()
	_, running := g.anim.active["talk"]
	g.anim.mu.Unlock()
	if !running {
		t.Error("the talk clip stopped while the ghost was still speaking")
	}

	g.stopTalking()
	if got := mouthNow(g); got != "smile" {
		t.Errorf("mouth after talking = %q, want the pose's smile back", got)
	}
}

// TestTalkForEndsOnItsOwn: dialogue that does not stream still stops
// moving the mouth, after roughly as long as the line would take to say.
func TestTalkForEndsOnItsOwn(t *testing.T) {
	g := animatedGhost(t, false)
	g.talkFor("hi") // short line: clamped to the minimum
	if got := mouthNow(g); got != "open" {
		t.Fatalf("mouth = %q, want it open at the start of a line", got)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		g.anim.mu.Lock()
		_, running := g.anim.active["talk"]
		g.anim.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the mouth never stopped moving")
}

// TestReducedMotionSilencesTheMouth: it is decoration on top of the words,
// and the words are there without it.
func TestReducedMotionSilencesTheMouth(t *testing.T) {
	g := animatedGhost(t, true)
	g.startTalking()
	g.anim.mu.Lock()
	_, running := g.anim.active["talk"]
	g.anim.mu.Unlock()
	if running {
		t.Error("reduced motion still moved the mouth")
	}
}

// TestPokeClipRestoresTheBody: a manual clip runs to its end and leaves
// nothing behind.
func TestPokeClipRestoresTheBody(t *testing.T) {
	g := animatedGhost(t, false)
	g.playClip("poke")

	// The squash is at the clip's middle keyframe, not its first — the
	// first is the resting pose it moves away from.
	squashed := false
	for i := 0; i < 40 && !squashed; i++ {
		g.shellMu.Lock()
		scale := g.shell.State().Moves()["body"].Scale
		g.shellMu.Unlock()
		squashed = scale > 0 && scale < 1
		time.Sleep(10 * time.Millisecond)
	}
	if !squashed {
		t.Error("the body never squashed during a poke")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		g.anim.mu.Lock()
		_, running := g.anim.active["poke"]
		g.anim.mu.Unlock()
		if !running {
			g.shellMu.Lock()
			back := g.shell.State().Moves()["body"].Scale
			g.shellMu.Unlock()
			if back != 1 {
				t.Errorf("body scale after the poke = %v, want 1", back)
			}
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Error("the poke never finished")
}

// TestUnknownClipIsIgnored: brains may ask for flourishes a simpler shell
// cannot perform.
func TestUnknownClipIsIgnored(t *testing.T) {
	g := animatedGhost(t, false)
	g.playClip("backflip") // must not panic
	g.anim.mu.Lock()
	defer g.anim.mu.Unlock()
	if _, ok := g.anim.active["backflip"]; ok {
		t.Error("an undeclared clip is running")
	}
}
