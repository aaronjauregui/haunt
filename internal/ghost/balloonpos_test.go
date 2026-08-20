package ghost

import "testing"

// Where a ghost speaks from. The bug this guards was visible with one ghost
// and obvious with two: the conventional spot is the bottom right of the
// screen and the balloon always went to the right of the sprite, so the
// default position put every word off the edge of the display.
func TestBalloonSide(t *testing.T) {
	const screen, sprite, panel = 1280, 160, 320
	for _, tc := range []struct {
		name string
		x    int32
		want int32
	}{
		// Room to the right: the panel goes where it reads best.
		{"middle of the screen", 400, 400 + sprite + balloonGap},
		{"hard against the left edge", 0, sprite + balloonGap},
		// The default corner: no room on the right, so it flips.
		{"the default bottom-right spot", 1072, 1072 - balloonGap - panel},
		// Just past the point where it stops fitting.
		{"one pixel too far right", screen - sprite - panel - balloonGap + 1,
			screen - sprite - panel - balloonGap + 1 - balloonGap - panel},
	} {
		got := balloonSide(tc.x, sprite, panel, screen, true)
		if got != tc.want {
			t.Errorf("%s: balloon at x=%d, want %d", tc.name, got, tc.want)
		}
		if got < 0 {
			t.Errorf("%s: balloon placed off the left edge at %d", tc.name, got)
		}
	}
}

// A screen too narrow for the ghost and a panel on either side: staying on
// the right keeps the balloon attached to whoever said it, which is the
// less confusing of two bad answers.
func TestBalloonSideWithNoRoomEitherWay(t *testing.T) {
	const sprite, panel = 160, 320
	x := int32(100)
	if got := balloonSide(x, sprite, panel, 400, true); got != x+sprite+balloonGap {
		t.Errorf("balloon at %d, want it left beside the ghost at %d", got, x+sprite+balloonGap)
	}
}

// Without a known screen width there is nothing to flip away from.
func TestBalloonSideWithoutAScreen(t *testing.T) {
	const sprite, panel = 160, 320
	if got := balloonSide(1072, sprite, panel, 0, false); got != 1072+sprite+balloonGap {
		t.Errorf("balloon at %d, want the default right-hand side", got)
	}
}
