package troupe

import (
	"sync"
)

// Where everyone stands. Positions are remembered per ghost and per screen
// already (internal/ghost/place.go), which is enough for one ghost and not
// enough for several: two ghosts computing the same default corner of the
// same screen both open in it, and one is invisible behind the other.
//
// So opening is arbitrated centrally. Only opening: once a person has
// dragged two ghosts into a heap, the heap is what they asked for.

// gap is the clearance left between two ghosts shuffled apart, so they read
// as two characters standing near each other rather than one wide smear.
const gap = 24

// shuffles is how many times a ghost will step aside before giving up and
// opening on top of somebody. A stacked ghost is a bad outcome; a ghost
// that walks off the screen looking for space is a worse one.
const shuffles = 8

type rect struct {
	output     string
	x, y, w, h int
}

func (r rect) overlaps(o rect) bool {
	if r.output != o.output {
		return false
	}
	return r.x < o.x+o.w && o.x < r.x+r.w &&
		r.y < o.y+o.h && o.y < r.y+r.h
}

// Room tracks which spots are taken, across every resident ghost.
type Room struct {
	mu    sync.Mutex
	taken map[string]rect
}

// NewRoom returns an empty room.
func NewRoom() *Room { return &Room{taken: map[string]rect{}} }

// For returns the room as one ghost sees it: the same shared state, with
// this ghost's name already filled in.
func (r *Room) For(name string) *View { return &View{room: r, name: name} }

// View is one ghost's handle on the room. It satisfies ghost.Spots.
type View struct {
	room *Room
	name string
}

// Claim reserves a rectangle, returning where it actually fits.
func (v *View) Claim(output string, x, y, w, h int) (int, int) {
	r := v.room
	r.mu.Lock()
	defer r.mu.Unlock()

	// Ghosts sit along the bottom edge, so sideways is the direction that
	// keeps them all where a mascot belongs. Leftward first, because the
	// conventional spot is the bottom right and there is more screen to the
	// left of it; then rightward, for the ghost whose remembered place is
	// already at the left edge.
	for _, step := range []int{-(w + gap), w + gap} {
		for i := 0; i <= shuffles; i++ {
			cand := rect{output: output, x: x + i*step, y: y, w: w, h: h}
			if cand.x < 0 {
				break
			}
			if r.freeFor(v.name, cand) {
				r.taken[v.name] = cand
				return cand.x, cand.y
			}
		}
	}
	// Out of room. Take the spot asked for: overlapping is better than
	// refusing to open.
	taken := rect{output: output, x: x, y: y, w: w, h: h}
	r.taken[v.name] = taken
	return x, y
}

// Moved records a new position for a ghost that has already opened.
func (v *View) Moved(output string, x, y, w, h int) {
	v.room.mu.Lock()
	defer v.room.mu.Unlock()
	v.room.taken[v.name] = rect{output: output, x: x, y: y, w: w, h: h}
}

// Release gives the spot back.
func (v *View) Release() {
	v.room.mu.Lock()
	defer v.room.mu.Unlock()
	delete(v.room.taken, v.name)
}

// freeFor reports whether cand clears everyone but the named ghost; callers
// hold the lock.
func (r *Room) freeFor(name string, cand rect) bool {
	for who, have := range r.taken {
		if who == name {
			continue
		}
		if cand.overlaps(have) {
			return false
		}
	}
	return true
}
