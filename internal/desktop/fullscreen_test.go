package desktop

import (
	"github.com/aaronjauregui/haunt/internal/hlog"

	"encoding/json"
	"testing"
)

// TestSwayTreeReading: sway reports fullscreen_mode 1 on every workspace,
// the scratchpad included, because a workspace does fill its output. Only
// containers hold windows — an earlier version asked the whole tree and
// concluded that an empty desktop was permanently fullscreen, which would
// have silenced the ghost forever.
func TestSwayTreeReading(t *testing.T) {
	for _, tc := range []struct {
		name string
		tree string
		want bool
	}{{
		name: "empty desktop, workspaces claim fullscreen_mode",
		tree: `{"type":"root","nodes":[
			{"type":"output","fullscreen_mode":0,"nodes":[
				{"type":"workspace","focused":true,"fullscreen_mode":1,"nodes":[]}]}]}`,
		want: false,
	}, {
		name: "the scratchpad is not a fullscreen window either",
		tree: `{"type":"root","nodes":[
			{"type":"workspace","name":"__i3_scratch","fullscreen_mode":1,"nodes":[]}]}`,
		want: false,
	}, {
		name: "a focused fullscreen window",
		tree: `{"type":"root","nodes":[
			{"type":"workspace","fullscreen_mode":1,"nodes":[
				{"type":"con","focused":true,"fullscreen_mode":1}]}]}`,
		want: true,
	}, {
		name: "a fullscreen window on another monitor is not an interruption",
		tree: `{"type":"root","nodes":[
			{"type":"workspace","fullscreen_mode":1,"nodes":[
				{"type":"con","focused":false,"fullscreen_mode":1}]},
			{"type":"workspace","fullscreen_mode":1,"nodes":[
				{"type":"con","focused":true,"fullscreen_mode":0}]}]}`,
		want: false,
	}, {
		name: "floating windows count",
		tree: `{"type":"root","nodes":[
			{"type":"workspace","fullscreen_mode":1,"floating_nodes":[
				{"type":"floating_con","focused":true,"fullscreen_mode":1}]}]}`,
		want: true,
	}, {
		name: "an ordinary window is not fullscreen",
		tree: `{"type":"root","nodes":[
			{"type":"workspace","fullscreen_mode":1,"nodes":[
				{"type":"con","focused":true,"fullscreen_mode":0}]}]}`,
		want: false,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			var n swayNode
			if err := json.Unmarshal([]byte(tc.tree), &n); err != nil {
				t.Fatal(err)
			}
			if got := n.fullscreenFocused(); got != tc.want {
				t.Errorf("fullscreenFocused = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNoCompositorIsQuietlyFine: on an unrecognised compositor the answer
// is "not fullscreen". A ghost that stayed silent because it could not ask
// would be worse than one that occasionally speaks over something.
func TestNoCompositorIsQuietlyFine(t *testing.T) {
	t.Setenv("SWAYSOCK", "")
	t.Setenv("HYPRLAND_INSTANCE_SIGNATURE", "")
	rec, logged := hlog.Recorder()
	w := New(rec)
	if w.Mechanism() != "none" {
		t.Errorf("mechanism = %q", w.Mechanism())
	}
	if w.Fullscreen() {
		t.Error("reported fullscreen with no way to know")
	}
	if len(logged()) == 0 {
		t.Error("said nothing about having no way to tell")
	}
}

// TestBrokenSocketDoesNotSilenceTheGhost: a socket that has gone away
// answers false, and complains once rather than every two seconds.
func TestBrokenSocketDoesNotSilenceTheGhost(t *testing.T) {
	t.Setenv("SWAYSOCK", "/nonexistent/sway.sock")
	rec, logged := hlog.Recorder()
	w := New(rec)
	for i := 0; i < 5; i++ {
		if w.Fullscreen() {
			t.Fatal("a dead socket reported fullscreen")
		}
		w.lastAt = w.lastAt.Add(-pollInterval) // expire the cache
	}
	if got := logged(); len(got) != 1 {
		t.Errorf("logged %d times, want exactly one complaint: %v", len(got), got)
	}
}
