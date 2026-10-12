// Package quiet implements do-not-disturb: recurring quiet-hours windows
// plus a manual shush with expiry. The driver consults it before showing
// balloons; ghosts cannot override it.
//
// The [quiet] section is read for it by internal/config, which owns the
// config file; New takes the parsed section. Package quiet deliberately
// does no file reading of its own beyond the shush marker, which is
// runtime state rather than configuration.
package quiet

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aaronjauregui/haunt/internal/control"
)

// Config is the [quiet] portion of the user config.
type Config struct {
	// Hours are "HH:MM-HH:MM" windows, overnight allowed ("22:00-08:00").
	Hours []string `toml:"hours"`
}

type window struct{ start, end int } // minutes since midnight

// Checker answers "is it quiet right now?".
type Checker struct {
	windows []window
	// shushFile holds a manual-mute expiry (RFC3339) when present.
	shushFile string
	now       func() time.Time

	// mu guards awayUntil, which is the one thing here that changes while
	// the ghost runs: everything else is settled from config at startup and
	// read from a great many goroutines.
	mu sync.Mutex
	// awayUntil is how long the house is believed to be empty. Set by a
	// presence poll, if the ghost has one (see notePresence in
	// internal/ghost/poll.go), and never by anything else — the driver
	// knows no vendor's word for "away" and holds only a deadline.
	//
	// A deadline rather than a flag, so that quiet decays back to talking
	// on its own. A machine that stops polling is the ordinary case — a
	// laptop asleep, a server gone — and a ghost left mute for good on the
	// strength of one reading is the failure people uninstall over.
	awayUntil time.Time
}

// Away says nobody is home until t, and reports whether that is news. It is
// advice from a presence poll, not a command from a brain: a ghost cannot
// silence itself, because being able to would make quiet hours negotiable.
func (c *Checker) Away(t time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	was := c.awayUntil.After(c.now())
	if t.After(c.awayUntil) {
		c.awayUntil = t
	}
	return !was
}

// Home says somebody is there after all, and reports whether that is news.
// Immediate, unlike going away, which expires: the reading that says the
// house is occupied is the one it is safe to believe at once.
func (c *Checker) Home() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	was := c.awayUntil.After(c.now())
	c.awayUntil = time.Time{}
	return was
}

// NobodyHome reports whether a presence poll says the house is empty
// now, whatever the shush and the quiet hours say.
func (c *Checker) NobodyHome() bool { return c.away() }

func (c *Checker) away() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.awayUntil.After(c.now())
}

// ShushPath is the manual-mute marker file, in the runtime dir so it clears
// on reboot.
func ShushPath() string {
	return filepath.Join(control.SocketDir(), "shush")
}

// New builds a Checker from explicit config.
func New(cfg Config) (*Checker, error) {
	c := &Checker{shushFile: ShushPath(), now: time.Now}
	for _, h := range cfg.Hours {
		w, err := parseWindow(h)
		if err != nil {
			return nil, err
		}
		c.windows = append(c.windows, w)
	}
	return c, nil
}

func parseWindow(s string) (window, error) {
	parts := strings.Split(s, "-")
	if len(parts) != 2 {
		return window{}, fmt.Errorf("quiet window %q must be HH:MM-HH:MM", s)
	}
	start, err := parseHHMM(parts[0])
	if err != nil {
		return window{}, fmt.Errorf("quiet window %q: %w", s, err)
	}
	end, err := parseHHMM(parts[1])
	if err != nil {
		return window{}, fmt.Errorf("quiet window %q: %w", s, err)
	}
	return window{start: start, end: end}, nil
}

func parseHHMM(s string) (int, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("time %q must be HH:MM", s)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("time %q must be HH:MM", s)
	}
	return h*60 + m, nil
}

// Active reports whether output should be suppressed right now, and why.
func (c *Checker) Active() (bool, string) {
	if until, ok := c.shushedUntil(); ok {
		return true, "shushed until " + until.Format("15:04")
	}
	if c.away() {
		return true, "nobody is home"
	}
	now := c.now()
	minutes := now.Hour()*60 + now.Minute()
	for _, w := range c.windows {
		if w.start <= w.end {
			if minutes >= w.start && minutes < w.end {
				return true, "quiet hours"
			}
		} else { // overnight window
			if minutes >= w.start || minutes < w.end {
				return true, "quiet hours"
			}
		}
	}
	return false, ""
}

func (c *Checker) shushedUntil() (time.Time, bool) {
	data, err := os.ReadFile(c.shushFile)
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil || c.now().After(t) {
		return time.Time{}, false
	}
	return t, true
}

// Shush mutes all ghosts for d.
func Shush(d time.Duration) (time.Time, error) {
	until := time.Now().Add(d)
	if err := os.MkdirAll(filepath.Dir(ShushPath()), 0o700); err != nil {
		return time.Time{}, err
	}
	return until, os.WriteFile(ShushPath(), []byte(until.Format(time.RFC3339)+"\n"), 0o644)
}

// Unshush clears a manual mute.
func Unshush() error {
	err := os.Remove(ShushPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
