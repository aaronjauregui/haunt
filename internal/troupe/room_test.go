package troupe

import "testing"

// Two ghosts computing the same default corner is the ordinary case, not
// the unlucky one, so the second must land somewhere else.
func TestSecondGhostStepsAside(t *testing.T) {
	r := NewRoom()
	x1, y1 := r.For("wisp").Claim("DP-1", 1000, 500, 160, 160)
	if x1 != 1000 || y1 != 500 {
		t.Fatalf("the first ghost should get the spot it asked for, got (%d,%d)", x1, y1)
	}
	x2, y2 := r.For("moth").Claim("DP-1", 1000, 500, 160, 160)
	if y2 != 500 {
		t.Errorf("stepping aside should stay on the bottom edge, got y=%d", y2)
	}
	if x2 >= 1000 {
		t.Errorf("the second ghost should have moved left of the first, got x=%d", x2)
	}
	if x2+160 > 1000 {
		t.Errorf("(%d,%d) still overlaps the first ghost", x2, y2)
	}
}

// The same coordinates on two screens are two different places.
func TestDifferentScreensDoNotCollide(t *testing.T) {
	r := NewRoom()
	r.For("wisp").Claim("DP-1", 1000, 500, 160, 160)
	x, y := r.For("moth").Claim("HDMI-A-1", 1000, 500, 160, 160)
	if x != 1000 || y != 500 {
		t.Errorf("another screen is not crowded; got (%d,%d)", x, y)
	}
}

// A ghost that stops gives its spot back, so the next one to open may have
// the good corner.
func TestReleaseFreesTheSpot(t *testing.T) {
	r := NewRoom()
	v := r.For("wisp")
	v.Claim("DP-1", 1000, 500, 160, 160)
	v.Release()
	x, y := r.For("moth").Claim("DP-1", 1000, 500, 160, 160)
	if x != 1000 || y != 500 {
		t.Errorf("the spot should be free again; got (%d,%d)", x, y)
	}
}

// Re-claiming as the same ghost is not colliding with yourself.
func TestAGhostDoesNotBlockItself(t *testing.T) {
	r := NewRoom()
	v := r.For("wisp")
	v.Claim("DP-1", 1000, 500, 160, 160)
	x, y := v.Claim("DP-1", 1000, 500, 160, 160)
	if x != 1000 || y != 500 {
		t.Errorf("a ghost should not be pushed off its own spot; got (%d,%d)", x, y)
	}
}

// Against the left edge there is nowhere to step but right.
func TestAtTheLeftEdgeItStepsRight(t *testing.T) {
	r := NewRoom()
	r.For("wisp").Claim("DP-1", 0, 500, 160, 160)
	x, _ := r.For("moth").Claim("DP-1", 0, 500, 160, 160)
	if x < 160 {
		t.Errorf("with no room to the left it should step right; got x=%d", x)
	}
}

// Dragging a ghost onto a spot makes that spot taken for anyone opening
// later.
func TestMovedIsRespected(t *testing.T) {
	r := NewRoom()
	v := r.For("wisp")
	v.Claim("DP-1", 1000, 500, 160, 160)
	v.Moved("DP-1", 300, 200, 160, 160)
	if x, y := r.For("moth").Claim("DP-1", 1000, 500, 160, 160); x != 1000 || y != 500 {
		t.Errorf("the vacated spot should be free; got (%d,%d)", x, y)
	}
	if x, _ := r.For("shade").Claim("DP-1", 300, 200, 160, 160); x == 300 {
		t.Error("the dragged-to spot should be taken")
	}
}

// Enough ghosts and the room runs out: opening stacked beats not opening.
func TestOutOfRoomItOpensAnyway(t *testing.T) {
	r := NewRoom()
	// A wide sprite on a narrow imaginary screen: after `shuffles` steps
	// in each direction there is nowhere left that is free.
	for i := 0; i < shuffles*2+4; i++ {
		r.For(string(rune('a'+i))).Claim("DP-1", 400, 0, 400, 160)
	}
	if got := len(r.taken); got != shuffles*2+4 {
		t.Errorf("every ghost should have a spot recorded, got %d", got)
	}
}
