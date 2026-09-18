// Package config is the user's config file: where it lives, and the whole
// of what is in it, read once.
//
// It exists because five subsystems used to each open ~/.config/haunt/
// config.toml for their own section — the same read, the same parse, the
// same os.IsNotExist dance, five times per boot. Beyond the waste, a syntax
// error produced a different message depending on which loader reached it
// first, and there was nowhere to look to find out what the file may
// contain. Load reads and parses once; every section is a field on File.
//
// The one section deliberately not here is [log]. hlog reads it with its
// own copy of the same few lines, because a logging package that depends on
// half the driver cannot be used by the other half — see internal/hlog/
// config.go, which says so at the copy.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/aaronjauregui/haunt/internal/backend"
	"github.com/aaronjauregui/haunt/internal/hermes"
	"github.com/aaronjauregui/haunt/internal/quiet"
	"github.com/aaronjauregui/haunt/internal/session"
)

// File is the parsed config, with defaults filled in and cross-field
// derivations (durations, the session model's thresholds) already done. A
// missing file is not an error: the zero value plus defaults is the
// shipping configuration.
type File struct {
	// Path is where this was read from, kept for error messages that have
	// to name the file a person should go and edit.
	Path string

	Quiet     quiet.Config
	Backend   *backend.Config
	Animation AnimationConfig
	Awareness AwarenessConfig
	Curiosity CuriosityConfig
	// Hermes is where the ghosts' brain lives (docs/dev/hermes.md).
	Hermes hermes.Config
}

// root is the on-disk shape. Separate from File so that File can carry
// derived fields without them looking like things a user may set.
type root struct {
	Quiet     quiet.Config    `toml:"quiet"`
	Backend   backend.Config  `toml:"backend"`
	Animation AnimationConfig `toml:"animation"`
	Awareness AwarenessConfig `toml:"awareness"`
	Curiosity CuriosityConfig `toml:"curiosity"`
	Hermes    hermes.Config   `toml:"hermes"`
}

// Path returns the user config file location. HAUNT_CONFIG_DIR overrides
// it, which is how tests and custom setups point at a different file.
func Path() (string, error) {
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

// Load reads and parses the config file once. A missing file yields
// defaults; a malformed one is an error naming the file, because a config
// nobody can parse is a config nobody meant.
func Load() (*File, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	var r root
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := toml.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	f := &File{
		Path:      path,
		Quiet:     r.Quiet,
		Backend:   backend.WithDefaults(r.Backend),
		Animation: r.Animation,
		Awareness: r.Awareness,
		Curiosity: r.Curiosity,
		Hermes:    r.Hermes,
	}
	if err := f.Awareness.derive(path); err != nil {
		return nil, err
	}
	return f, nil
}

// AnimationConfig is the [animation] portion of the user config.
type AnimationConfig struct {
	// ReducedMotion stops idle animation — blinking, breathing, anything on
	// an automatic trigger. Clips a brain asks for still play: those are the
	// ghost saying something, and a companion that ignores its own brain is
	// broken rather than considerate.
	ReducedMotion bool `toml:"reduced_motion"`
	// ExpressionInference chooses how scripted dialogue is mapped to shell
	// poses: "lexicon" (default) uses the driver's free word table, and
	// "off" leaves the face alone entirely. A ghost on hermes wears a face
	// by calling its face tool; the lexicon is the fallback when it did not.
	ExpressionInference string `toml:"expression_inference"`
}

// Expression modes.
const (
	ExpressionOff     = "off"
	ExpressionLexicon = "lexicon"
)

// ExpressionMode is ExpressionInference with its default: the word table.
// Anything unknown reads as the default.
func (c AnimationConfig) ExpressionMode() string {
	switch c.ExpressionInference {
	case ExpressionOff, ExpressionLexicon:
		return c.ExpressionInference
	default:
		return ExpressionLexicon
	}
}

// AwarenessConfig is the [awareness] portion of the user config: what the
// ghost is allowed to notice about your screen.
type AwarenessConfig struct {
	// Ignore lists applications never to report, matched case-insensitively
	// by substring. Password managers and banking belong here, and the
	// defaults cover the obvious ones so the careless case is not the
	// dangerous one.
	Ignore []string `toml:"ignore"`
	// Dwell is how long in one window before the ghost may remark on it
	// ("25m" by default). One of three thresholds people actually want to
	// tune: this is the difference between a companion that notices you are
	// stuck and one that interrupts you while you are working. The session
	// model's remaining thresholds are fixed.
	Dwell string `toml:"dwell"`
	// Away is how long a gap counts as having been away from the machine,
	// so coming back is worth remarking on ("90m" by default).
	Away string `toml:"away"`
	// LateHour is the hour after which still working is itself worth
	// noticing, in local time (23 by default), running until the small
	// hours of the next morning. Negative switches the shape off, which is
	// the setting for people whose day starts at eleven at night.
	LateHour *int `toml:"late_hour"`

	// Session is the parsed form of the above, filled in at load.
	Session session.Config `toml:"-"`
}

// DefaultIgnoredApps are never watched unless the user replaces the list.
var DefaultIgnoredApps = []string{
	"1password", "bitwarden", "keepass", "gnome-keyring", "seahorse",
	"polkit", "pinentry", "gnome-disks", "banking",
}

// derive fills in the defaults and turns the user's strings into the
// session model's durations. path is only for error messages.
func (c *AwarenessConfig) derive(path string) error {
	if c.Ignore == nil {
		c.Ignore = DefaultIgnoredApps
	}
	if c.Dwell != "" {
		d, err := time.ParseDuration(c.Dwell)
		if err != nil {
			return fmt.Errorf("parsing %s: [awareness] dwell %q: %w", path, c.Dwell, err)
		}
		c.Session.Dwell = d
	}
	if c.Away != "" {
		d, err := time.ParseDuration(c.Away)
		if err != nil {
			return fmt.Errorf("parsing %s: [awareness] away %q: %w", path, c.Away, err)
		}
		c.Session.AwayLong = d
	}
	if c.LateHour != nil {
		h := *c.LateHour
		if h > 23 || (h >= 0 && h < session.EarliestLateHour) {
			return fmt.Errorf(
				"parsing %s: [awareness] late_hour %d: use %d-23, or a negative number to switch it off — "+
					"late runs until the small hours, so anything earlier would make the whole day late",
				path, h, session.EarliestLateHour)
		}
		c.Session.LateHour = h
	}
	return nil
}

// CuriosityConfig is the [curiosity] portion of the user config. It is the
// user's dial, not the ghost's: a ghost declares no allowance and cannot
// raise one.
type CuriosityConfig struct {
	// ActionsPerDay caps unprompted actions in any rolling 24 hours. Zero
	// disables initiative entirely; absent means DefaultActionsPerDay.
	ActionsPerDay *int `toml:"actions_per_day"`
	// ActionsPerDayTotal caps unprompted actions across every resident
	// ghost together. Absent means the ceiling is derived from the number
	// of ghosts in the room (see internal/troupe); it is here for the user
	// who wants to say "however many of you there are, twenty a day".
	ActionsPerDayTotal *int `toml:"actions_per_day_total"`
	// ObligationsPerDay caps how many things the ghost may raise that the
	// *person* put on its books — a reminder they asked for, an occasion
	// the manifest declares. Kept apart from ActionsPerDay because the two
	// ration different things: that one rations the ghost's whims, and a
	// reminder you asked for is not a whim (see markObliged in
	// internal/ghost/curiosity.go).
	//
	// This is a backstop rather than a dial. It is not meant to be reached
	// in ordinary use — nobody sets two dozen reminders a day — and it is
	// here so that a brain looping on `intend` cannot spend the user's
	// reminders on its own chatter. Turning it down to shape behaviour is
	// turning off things you asked for; ActionsPerDay is the dial for
	// "talk to me less".
	ObligationsPerDay *int `toml:"obligations_per_day"`
}

const (
	// DefaultActionsPerDay is roughly one unprompted remark every couple
	// of waking hours: present, not pestering.
	DefaultActionsPerDay = 12
	// DefaultObligationsPerDay is the backstop on things the person put on
	// the ghost's books. Generous, because being refused one is being
	// refused something you asked for, and the number is only there to
	// bound a brain that has gone wrong.
	DefaultObligationsPerDay = 24
)

// Allowances reports the daily rations: the per-ghost allowance for
// unprompted action, and the explicit ceiling for the room if the user set
// one (-1 when they did not). The supervisor needs the second and the ghost
// needs the first, and neither should be parsing the file a second time.
func (f *File) Allowances() (perGhost, room int) {
	perGhost = DefaultActionsPerDay
	if f.Curiosity.ActionsPerDay != nil {
		perGhost = *f.Curiosity.ActionsPerDay
	}
	room = -1
	if f.Curiosity.ActionsPerDayTotal != nil {
		room = *f.Curiosity.ActionsPerDayTotal
	}
	return perGhost, room
}

// ObligationsPerDay is the setting with its default applied.
func (f *File) ObligationsPerDay() int {
	if f.Curiosity.ObligationsPerDay == nil {
		return DefaultObligationsPerDay
	}
	return *f.Curiosity.ObligationsPerDay
}
