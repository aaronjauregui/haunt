package ghost

import (
	"encoding/json"
	"testing"

	"github.com/aaronjauregui/haunt/internal/wlclient"
)

func placeGhost(t *testing.T) *Ghost {
	t.Helper()
	return &Ghost{
		opts:  Options{Log: func(string) {}},
		store: &memStore{m: map[string]string{}},
	}
}

// TestSavedPlaceIsRestored: a companion that forgets its corner every login
// is furniture that walks off.
func TestSavedPlaceIsRestored(t *testing.T) {
	g := placeGhost(t)
	g.outputName = "DP-2"
	g.savePlace(640, 480)

	got, ok := g.loadPlace()
	if !ok {
		t.Fatal("nothing was saved")
	}
	if got.Output != "DP-2" || got.X != 640 || got.Y != 480 {
		t.Errorf("saved place = %+v", got)
	}

	// It is stored per output, because layer-shell coordinates mean
	// different places on different screens.
	raw, _, _ := storeGet(g.store, placeKey)
	var p place
	if json.Unmarshal([]byte(raw), &p) != nil || p.Output == "" {
		t.Errorf("the stored place does not name its screen: %s", raw)
	}
}

// fakeOutputs is what the compositor is pretending to have.
type placeCase struct {
	name     string
	saved    *place
	outputs  []wlclient.Output
	explicit bool
	wantOut  string
	wantX    int
	wantY    int
}

// TestStartingPlace covers where a ghost opens, which is a question with
// several wrong answers: on top of a window, off the edge of a screen that
// shrank, or on a monitor that is no longer plugged in.
func TestStartingPlace(t *testing.T) {
	left := wlclient.Output{Name: "DP-1", X: 0, W: 1920, H: 1080}
	right := wlclient.Output{Name: "DP-2", X: 1920, W: 1280, H: 720}

	for _, tc := range []placeCase{{
		name:    "first run goes to the corner of the first screen",
		outputs: []wlclient.Output{left, right},
		wantOut: "DP-1", wantX: 1920 - 160 - 48, wantY: 1080 - 160 - 48,
	}, {
		name:    "a remembered place comes back, screen and all",
		saved:   &place{Output: "DP-2", X: 300, Y: 200},
		outputs: []wlclient.Output{left, right},
		wantOut: "DP-2", wantX: 300, wantY: 200,
	}, {
		name:    "a place off a shrunken screen is pulled back on",
		saved:   &place{Output: "DP-2", X: 5000, Y: 4000},
		outputs: []wlclient.Output{left, right},
		wantOut: "DP-2", wantX: 1280 - 32, wantY: 720 - 32,
	}, {
		// Unplugging a monitor should not lose the companion.
		name:    "an unplugged screen falls back to the first",
		saved:   &place{Output: "HDMI-9", X: 300, Y: 200},
		outputs: []wlclient.Output{left},
		wantOut: "DP-1", wantX: 1920 - 160 - 48, wantY: 1080 - 160 - 48,
	}, {
		name:     "an explicit position wins",
		saved:    &place{Output: "DP-2", X: 300, Y: 200},
		outputs:  []wlclient.Output{left, right},
		explicit: true,
		wantOut:  "DP-1", wantX: 42, wantY: 43,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			g := placeGhost(t)
			if tc.saved != nil {
				g.outputName = tc.saved.Output
				g.savePlace(tc.saved.X, tc.saved.Y)
			}
			out, x, y := g.startingPlaceOn(tc.outputs, 42, 43, tc.explicit)
			if out != tc.wantOut || x != tc.wantX || y != tc.wantY {
				t.Errorf("placed at %s (%d,%d), want %s (%d,%d)",
					out, x, y, tc.wantOut, tc.wantX, tc.wantY)
			}
		})
	}
}

// TestNoOutputsYet: a compositor that has not announced a screen must not
// stop the ghost from starting.
func TestNoOutputsYet(t *testing.T) {
	g := placeGhost(t)
	out, x, y := g.startingPlaceOn(nil, 10, 20, false)
	if out != "" || x != 10 || y != 20 {
		t.Errorf("placed at %s (%d,%d) with no screens, want the given position", out, x, y)
	}
}
