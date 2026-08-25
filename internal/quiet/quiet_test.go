package quiet

import (
	"strings"
	"testing"
	"time"
)

func at(h, m int) time.Time {
	return time.Date(2026, 8, 14, h, m, 0, 0, time.Local)
}

func checker(t *testing.T, hours ...string) *Checker {
	t.Helper()
	t.Setenv("HAUNT_RUNTIME_DIR", t.TempDir())
	c, err := New(Config{Hours: hours})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDaytimeWindow(t *testing.T) {
	c := checker(t, "13:00-14:00")
	cases := []struct {
		h, m int
		want bool
	}{
		{12, 59, false}, {13, 0, true}, {13, 30, true}, {14, 0, false},
	}
	for _, tc := range cases {
		c.now = func() time.Time { return at(tc.h, tc.m) }
		if got, _ := c.Active(); got != tc.want {
			t.Errorf("at %02d:%02d active=%v, want %v", tc.h, tc.m, got, tc.want)
		}
	}
}

func TestOvernightWindow(t *testing.T) {
	c := checker(t, "22:00-08:00")
	cases := []struct {
		h, m int
		want bool
	}{
		{21, 59, false}, {22, 0, true}, {2, 0, true}, {7, 59, true}, {8, 0, false}, {12, 0, false},
	}
	for _, tc := range cases {
		c.now = func() time.Time { return at(tc.h, tc.m) }
		if got, _ := c.Active(); got != tc.want {
			t.Errorf("at %02d:%02d active=%v, want %v", tc.h, tc.m, got, tc.want)
		}
	}
}

func TestShushAndUnshush(t *testing.T) {
	c := checker(t)
	if got, _ := c.Active(); got {
		t.Fatal("quiet with no windows and no shush")
	}
	if _, err := Shush(time.Hour); err != nil {
		t.Fatal(err)
	}
	got, why := c.Active()
	if !got || why == "" {
		t.Fatalf("shush not active: %v %q", got, why)
	}
	if err := Unshush(); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Active(); got {
		t.Fatal("still quiet after unshush")
	}
}

func TestExpiredShushIgnored(t *testing.T) {
	c := checker(t)
	if _, err := Shush(-time.Minute); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Active(); got {
		t.Fatal("expired shush still active")
	}
}

func TestBadWindowRejected(t *testing.T) {
	for _, bad := range []string{"22:00", "25:00-08:00", "aa:bb-cc:dd", "22:00-08:99"} {
		if _, err := New(Config{Hours: []string{bad}}); err == nil {
			t.Errorf("window %q accepted", bad)
		}
	}
}

// Presence: a house that knows you left is a better answer to "should the
// ghost be talking" than a clock is.
func TestAwayIsQuiet(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	c := checker(t)
	c.now = func() time.Time { return now }

	if muted, _ := c.Active(); muted {
		t.Fatal("quiet at noon with no windows and nobody away")
	}
	if !c.Away(now.Add(30 * time.Minute)) {
		t.Error("the first reading that nobody is home was not news")
	}
	muted, why := c.Active()
	if !muted || !strings.Contains(why, "home") {
		t.Errorf("Active() = %v, %q; want quiet, and a reason saying why", muted, why)
	}

	// It wears off on its own. A machine that stops polling — asleep, off
	// the network, the server gone — must not leave a ghost mute for good
	// on the strength of one half-hour-old reading.
	now = now.Add(31 * time.Minute)
	if muted, why := c.Active(); muted {
		t.Errorf("still quiet after the reading expired (%s)", why)
	}

	// And coming home is believed at once, unlike leaving.
	c.Away(now.Add(30 * time.Minute))
	if !c.Home() {
		t.Error("coming home while away was not news")
	}
	if muted, why := c.Active(); muted {
		t.Errorf("still quiet after coming home (%s)", why)
	}
}
