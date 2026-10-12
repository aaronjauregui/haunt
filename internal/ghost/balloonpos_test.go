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

// Which edge of the ghost a panel is level with. The bug this guards is
// the conversation: its top was pinned at the ghost's top, so from the
// default bottom-right spot every line that arrived, and every row the box
// took, pushed the line being typed further down and off the screen.
func TestBalloonTop(t *testing.T) {
	const screen, sprite = 720, 160
	for _, tc := range []struct {
		name  string
		y, lh int32
		up    bool
		want  int32
	}{
		// A line that fits below the ghost's top hangs from it.
		{"a short line in the middle", 200, 80, false, 200},
		{"a line that just fits", 400, screen - 400, false, 400},
		// One that would run off the bottom stands on its feet instead.
		{"one pixel too tall", 400, screen - 400 + 1, false, 400 + sprite - (screen - 400 + 1)},
		{"a tall reply in the default corner", 512, 300, false, 512 + sprite - 300},
		// The conversation always stands on its feet, room or not.
		{"a conversation in the middle", 200, 120, true, 200 + sprite - 120},
		{"a conversation in the default corner", 512, 520, true, 512 + sprite - 520},
		// And nothing goes above the top of the screen.
		{"a conversation taller than the room above", 100, 520, true, 0},
		{"a reply taller than the room either way", 20, screen, false, 0},
	} {
		got := balloonTop(tc.y, sprite, tc.lh, screen, true, tc.up)
		if got != tc.want {
			t.Errorf("%s: balloon top at y=%d, want %d", tc.name, got, tc.want)
		}
		if got < 0 {
			t.Errorf("%s: balloon placed above the screen at %d", tc.name, got)
		}
		if bottom := got + tc.lh; bottom > screen && got > 0 {
			t.Errorf("%s: balloon runs to %d, past the bottom at %d, with room above", tc.name, bottom, screen)
		}
	}
}

// Without a known screen height there is nothing to flip away from, but
// the conversation still grows upward: that rule is about the line being
// typed staying put, not about the screen.
func TestBalloonTopWithoutAScreen(t *testing.T) {
	const sprite = 160
	if got := balloonTop(600, sprite, 400, 0, false, false); got != 600 {
		t.Errorf("a line at %d, want it level with the ghost's top at 600", got)
	}
	if got := balloonTop(600, sprite, 400, 0, false, true); got != 600+sprite-400 {
		t.Errorf("a conversation at %d, want it on the ghost's feet at %d", got, 600+sprite-400)
	}
}
