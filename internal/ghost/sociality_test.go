package ghost

import (
	"sync"
	"testing"

	"github.com/aaronjauregui/haunt/internal/bus"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/shell"
)

// fakeStage records what a ghost announced, standing in for the other
// ghosts in the room.
type fakeStage struct {
	mu      sync.Mutex
	acts    []string
	deliver func(from, action, detail string)
	left    bool
}

func (f *fakeStage) Announce(action, detail string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acts = append(f.acts, action+":"+detail)
}

func (f *fakeStage) Observe(deliver func(from, action, detail string)) { f.deliver = deliver }

func (f *fakeStage) Leave() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.left = true
}

func (f *fakeStage) announced() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.acts...)
}

func stagedGhost(t *testing.T) (*Ghost, *fakeStage) {
	t.Helper()
	st := &fakeStage{}
	g := &Ghost{
		opts:     Options{Stage: st},
		log:      hlog.Discard(),
		manifest: &Manifest{Ghost: GhostSection{Name: "wisp"}},
		shell:    shell.Builtin(64),
		bus:      bus.New(64),
	}
	return g, st
}

// A face that really changed is a thing the room can see. One that did not
// — a pose this shell has never heard of — is not, and announcing it would
// have the neighbours reacting to something that never happened.
func TestOnlyRealChangesAreAnnounced(t *testing.T) {
	g, st := stagedGhost(t)
	g.setExpression("happy")
	g.setExpression("happy") // already wearing it
	g.setExpression("ecstatic")
	if got := st.announced(); len(got) != 1 || got[0] != proto.ActedExpression+":happy" {
		t.Errorf("announced %v, want the one change that happened", got)
	}
}

// A blink is not news. Every ghost on screen is breathing every few
// seconds, and announcing that would bury what somebody actually did under
// the noise of everybody being alive — so only a clip a brain asked for
// crosses the stage, and only if the shell really has it.
func TestOnlyDeliberateFlourishesAreAnnounced(t *testing.T) {
	g, st := stagedGhost(t)
	g.startAnimation() // the built-in shell's idle clips, on their own timers
	g.playClip("breathe")
	if got := st.announced(); len(got) != 0 {
		t.Errorf("announced idle animation: %v", got)
	}
	g.execIntent(proto.Intent{Type: proto.IntentPlayClip, Name: "breathe"})
	g.execIntent(proto.Intent{Type: proto.IntentPlayClip, Name: "cartwheel"})
	if got := st.announced(); len(got) != 1 || got[0] != proto.ActedClip+":breathe" {
		t.Errorf("announced %v, want the one clip a brain asked for", got)
	}
}

// A ghost with nobody else in the room announces into no stage at all.
// This is the common case — one ghost — and it must not need a stage to
// exist.
func TestAloneIsQuiet(t *testing.T) {
	g := &Ghost{opts: Options{}, log: hlog.Discard(), shell: shell.Builtin(64), bus: bus.New(64),
		manifest: &Manifest{Ghost: GhostSection{Name: "alone"}}}
	g.setExpression("happy") // must not panic
	g.announce(proto.ActedSpoke, "hello")
}

// What the stage delivers becomes an event on this ghost's bus, carrying
// who did it and what was visible — and nothing else.
func TestObservationsReachTheBus(t *testing.T) {
	g, st := stagedGhost(t)
	st.deliver = g.observed
	st.deliver("whisper", proto.ActedSpoke, "the kettle has boiled")

	select {
	case e := <-g.bus.Events():
		if e.Type != proto.EventObserved || e.From != "whisper" ||
			e.Name != proto.ActedSpoke || e.Text != "the kettle has boiled" {
			t.Errorf("event = %+v, want whisper speaking", e)
		}
		if e.KV != nil || e.Session != nil {
			t.Errorf("an observation carried private state: %+v", e)
		}
	default:
		t.Fatal("nothing reached the bus")
	}
}
