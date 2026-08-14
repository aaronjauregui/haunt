// Package quiet implements do-not-disturb: recurring quiet-hours windows
// from the user config plus a manual shush with expiry. The driver consults
// it before showing balloons; ghosts cannot override it.
package quiet

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

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
}

// ConfigPath returns the user config file location.
func ConfigPath() (string, error) {
	if dir := os.Getenv("HAUNT_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "config.toml"), nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "haunt", "config.toml"), nil
}

// ShushPath is the manual-mute marker file, in the runtime dir so it clears
// on reboot.
func ShushPath() string {
	return filepath.Join(control.SocketDir(), "shush")
}

// Load builds a Checker from the user config (missing file = no windows).
func Load() (*Checker, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	var root struct {
		Quiet Config `toml:"quiet"`
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := toml.Unmarshal(data, &root); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	return New(root.Quiet)
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
