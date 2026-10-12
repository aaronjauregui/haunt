package ghost

import (
	"encoding/json"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/wlclient"
)

// Where the ghost sits. A companion that forgets its place every login is
// furniture that walks off, so the driver remembers the screen and the spot
// on it, and puts the ghost back.
//
// Position is stored per output name, because layer-shell coordinates are
// relative to the screen the surface is on: the same numbers mean different
// places on different monitors, and "bottom right of my second screen" has
// to survive the second screen being unplugged and replugged.

const placeKey = internalKeyPrefix + "place"

// balloonGap is the space between the ghost and the panel it speaks in.
const balloonGap = 12

// Which side a ghost speaks on is a placement question too, and the same
// one: a position is only good if what the ghost says from it can be read.

// place is the remembered position.
type place struct {
	// Output is the connector the ghost was on ("DP-1"). Empty means the
	// compositor chose and we never learned which.
	Output string `json:"output"`
	// X, Y are output-relative.
	X int `json:"x"`
	Y int `json:"y"`
}

// loadPlace reads the remembered position.
func (g *Ghost) loadPlace() (place, bool) {
	raw, ok, err := g.store.Get(placeKey)
	if err != nil || !ok {
		return place{}, false
	}
	var p place
	if json.Unmarshal([]byte(raw), &p) != nil {
		return place{}, false
	}
	return p, true
}

// savePlace records where the ghost is now. Called after a drag settles:
// dragging is how a person says where they want it.
func (g *Ghost) savePlace(x, y int) {
	p := place{Output: g.outputName, X: x, Y: y}
	b, err := json.Marshal(p)
	if err != nil {
		return
	}
	if err := g.store.Set(placeKey, string(b)); err != nil {
		g.log.Warn(hlog.Place, "saving position: %v", err)
	}
}

// startingPlace decides where a ghost opens: an explicit -x/-y wins, then
// the remembered spot if its screen is still there, then a default corner
// of the first screen. w and h are the sprite's size, which the corner
// needs so that the whole ghost fits in it.
func (g *Ghost) startingPlace(app *wlclient.App, optX, optY int, explicit bool, w, h int) (output string, x, y int) {
	return g.startingPlaceOn(app.Outputs(), optX, optY, explicit, w, h)
}

// startingPlaceOn is startingPlace against a known set of screens.
func (g *Ghost) startingPlaceOn(outs []wlclient.Output, optX, optY int, explicit bool, w, h int) (output string, x, y int) {
	if explicit {
		// An explicit position still needs a screen to be relative to.
		if len(outs) > 0 {
			return outs[0].Name, optX, optY
		}
		return "", optX, optY
	}

	if p, ok := g.loadPlace(); ok {
		if p.Output == "" {
			return "", p.X, p.Y
		}
		for _, o := range outs {
			if o.Name == p.Output {
				x, y := clampTo(o, p.X, p.Y)
				return p.Output, x, y
			}
		}
		// The screen it was on is gone. Keep the ghost rather than the
		// coordinates: an unplugged monitor should not lose the companion.
		g.log.Info(hlog.Place, "the screen %q is not connected; placing on the first one", p.Output)
	}

	if len(outs) == 0 {
		return "", optX, optY
	}
	o := outs[0]
	x, y = defaultSpot(o, w, h)
	return o.Name, x, y
}

// defaultSpot is the lower-right of a screen, inset a little: out of the
// way of most windows, and where a desktop mascot conventionally sits. w
// and h are the sprite's size, so that a large ghost's first appearance is
// on the screen rather than hanging off its edge; one larger than the
// screen is pinned to the top-left corner, which shows as much as fits.
func defaultSpot(o wlclient.Output, w, h int) (x, y int) {
	const inset = 48
	x = int(o.W) - w - inset
	y = int(o.H) - h - inset
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y
}

// clampTo keeps a remembered position on a screen that may have changed
// resolution since — a ghost parked off the edge is a ghost you cannot find.
func clampTo(o wlclient.Output, x, y int) (int, int) {
	const margin = 32
	if maxX := int(o.W) - margin; x > maxX {
		x = maxX
	}
	if maxY := int(o.H) - margin; y > maxY {
		y = maxY
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y
}

// balloonPos computes the top-left corner for a panel lw by lh: beside
// the sprite on whichever side has room (balloonSide), and level with it
// on whichever edge keeps it on the screen (balloonTop). up is for the
// conversation panel, which stands on the ghost's feet and grows upward.
func (g *Ghost) balloonPos(lw, lh int32, up bool) (int32, int32) {
	x, y, w, h := g.surface.Geometry()
	sw, sh, known := g.outputSize()
	return balloonSide(x, w, lw, sw, known), balloonTop(y, h, lh, sh, known, up)
}

// balloonSide picks which side of the sprite a panel of width lw sits on.
// To the right by default — reading order, and the ghost faces its own
// words — but flipped to the left when there is no room on the right. That
// is the ordinary case rather than an exotic one: a ghost's default spot is
// the bottom right of the screen, so the side it would rather speak on is
// exactly the side with nothing left of it, and a lone ghost in its own
// default corner used to talk off the edge of the display.
//
// It stays on the right when neither side fits, because a panel running off
// the right edge is at least anchored to the ghost that said it, while one
// pushed to a negative x is somewhere else entirely.
func balloonSide(x, w, lw, edge int32, known bool) int32 {
	right := x + w + balloonGap
	if known && right+lw > edge {
		if left := x - balloonGap - lw; left >= 0 {
			return left
		}
	}
	return right
}

// balloonTop picks the top of a panel lh tall beside a sprite whose top is
// at y and which is h tall, on a screen floor high. It is balloonSide's
// rule turned on its side.
//
// A line, a question or a status line hangs from the ghost's top edge, so
// it starts where a reader looks first; when that would run past the
// bottom of the screen it stands on the ghost's bottom edge instead. A
// ghost's default spot is the bottom right, so this is the tall reply in
// the ordinary corner, not a corner case.
//
// The conversation panel (up) always stands on the ghost's bottom edge.
// It grows as lines arrive and as the box takes rows, and growing upward
// is what keeps the line being typed in one place: the transcript stacks
// above it, away from the bottom of the screen, rather than pushing the
// box down and off it.
//
// Either way the top never goes above the screen's top: a panel taller
// than the room above the ghost's feet starts at y = 0 and overlaps
// whatever is below, which beats losing its first lines.
func balloonTop(y, h, lh, floor int32, known, up bool) int32 {
	top := y
	if up || (known && y+lh > floor) {
		top = y + h - lh
	}
	return max(top, 0)
}

// outputSize is the size of the screen this ghost is on. Not the first
// screen: on a two-monitor desk those are different questions, and the one
// that matters is where the balloon is about to be drawn.
func (g *Ghost) outputSize() (w, h int32, known bool) {
	if g.app == nil {
		return 0, 0, false
	}
	if g.outputName != "" {
		if o, ok := g.app.OutputNamed(g.outputName); ok {
			return o.W, o.H, true
		}
	}
	return g.app.OutputSize()
}
