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
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
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
	Files     FilesConfig
	// Hermes is where the ghosts' brain lives (docs/dev/hermes.md).
	Hermes     hermes.Config
	Appearance AppearanceConfig
}

// root is the on-disk shape. Separate from File so that File can carry
// derived fields without them looking like things a user may set.
type root struct {
	Quiet      quiet.Config     `toml:"quiet"`
	Backend    backend.Config   `toml:"backend"`
	Animation  AnimationConfig  `toml:"animation"`
	Awareness  AwarenessConfig  `toml:"awareness"`
	Curiosity  CuriosityConfig  `toml:"curiosity"`
	Files      FilesConfig      `toml:"files"`
	Hermes     hermes.Config    `toml:"hermes"`
	Appearance AppearanceConfig `toml:"appearance"`
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
		Path:       path,
		Quiet:      r.Quiet,
		Backend:    backend.WithDefaults(r.Backend),
		Animation:  r.Animation,
		Awareness:  r.Awareness,
		Curiosity:  r.Curiosity,
		Files:      r.Files,
		Hermes:     r.Hermes,
		Appearance: r.Appearance,
	}
	if err := f.Awareness.derive(path); err != nil {
		return nil, err
	}
	if err := f.Files.derive(path); err != nil {
		return nil, err
	}
	if err := f.Appearance.validate(path); err != nil {
		return nil, err
	}
	// What [backend] points at is checked here rather than on first use,
	// so that a file it cannot read is named while the person is still
	// looking at the config that names it.
	if err := f.Backend.Validate(); err != nil {
		return nil, fmt.Errorf("in %s: %w", path, err)
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

// FilesConfig is the [files] portion of the user config: the folders a
// ghost granted the files capability may read. The config is the person's
// consent to the folders; the capability decides which ghosts use them.
// It is read at startup and not editable at runtime.
type FilesConfig struct {
	// Roots names each shared folder: `notes = "~/notes"` lets a ghost read
	// notes/2026/todo.md. The model only ever sees the name, so neither the
	// host path nor the username reaches it. After load the paths are
	// absolute and clean, with a leading ~ expanded. Empty shares nothing,
	// the same as an empty [backend.search].
	Roots map[string]string `toml:"roots"`
	// Unprompted says when a ghost may read files on a turn the person did
	// not start: "never" (default), "skills" or "any". The default is
	// closed, unlike search, because files are more personal than the web.
	Unprompted string `toml:"unprompted"`
}

// Unprompted modes for [files], the same three as [backend.search].
const (
	FilesNever  = "never"
	FilesSkills = "skills"
	FilesAny    = "any"
)

// rootName is what a root may be called. It is the first segment of every
// path the model writes, so it is kept to something a small model spells
// right and that can never be mistaken for a path of its own.
var rootName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// UnpromptedMode is Unprompted with its default: never. Load refuses
// anything else, so the default only covers a FilesConfig built by hand.
func (c FilesConfig) UnpromptedMode() string {
	switch c.Unprompted {
	case FilesSkills, FilesAny:
		return c.Unprompted
	default:
		return FilesNever
	}
}

// RootNames is the shared folders' names, sorted, so that listing them
// reads the same every time.
func (c FilesConfig) RootNames() []string {
	names := make([]string, 0, len(c.Roots))
	for name := range c.Roots {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// derive checks the roots and expands ~ in their paths. A mistake here is
// a startup error naming the file, because a folder the person meant to
// share and did not is worth hearing about at once. A root whose directory
// is missing is not a mistake: an unmounted drive comes back, so that is
// left to whoever opens the roots to report. path is only for error
// messages.
func (c *FilesConfig) derive(path string) error {
	switch c.Unprompted {
	case "", FilesNever, FilesSkills, FilesAny:
	default:
		return fmt.Errorf("parsing %s: [files] unprompted %q: use %q, %q or %q",
			path, c.Unprompted, FilesNever, FilesSkills, FilesAny)
	}
	// Two roots cannot share a name: TOML refuses a repeated key, so that
	// arrives here as a parse error that already names the file.
	var home string
	for _, name := range c.RootNames() {
		if !rootName.MatchString(name) {
			return fmt.Errorf("parsing %s: [files.roots] %q: a name is 1-32 lowercase letters, "+
				"digits, - or _, starting with a letter or digit", path, name)
		}
		dir := c.Roots[name]
		if dir == "~" || strings.HasPrefix(dir, "~/") {
			if home == "" {
				h, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("parsing %s: [files.roots] %s = %q: %w", path, name, dir, err)
				}
				home = h
			}
			dir = filepath.Join(home, dir[1:])
		}
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("parsing %s: [files.roots] %s = %q: the path must be absolute or start with ~/",
				path, name, c.Roots[name])
		}
		c.Roots[name] = filepath.Clean(dir)
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

// AppearanceConfig is the [appearance] portion of the user config: how
// large the person wants their ghosts drawn.
type AppearanceConfig struct {
	// Scale multiplies every ghost's own scale. Absent means 1.
	Scale *float64 `toml:"scale"`
	// Ghost holds per-ghost settings, keyed by [ghost] name; a ghost's
	// entry replaces the global one.
	Ghost map[string]GhostAppearance `toml:"ghost"`
}

// GhostAppearance is one ghost's [appearance.ghost.<name>] table.
type GhostAppearance struct {
	Scale *float64 `toml:"scale"`
}

// validate refuses a scale of zero or less. Omitting a scale is how to ask
// for the default, so an explicit zero is more likely a slip than a wish,
// and a negative size means nothing at all. path is only for error
// messages.
func (c *AppearanceConfig) validate(path string) error {
	if c.Scale != nil && *c.Scale <= 0 {
		return fmt.Errorf("parsing %s: [appearance] scale %v: use a number above 0, or leave it out for 1",
			path, *c.Scale)
	}
	for _, name := range slices.Sorted(maps.Keys(c.Ghost)) {
		if s := c.Ghost[name].Scale; s != nil && *s <= 0 {
			return fmt.Errorf("parsing %s: [appearance.ghost.%s] scale %v: use a number above 0, or leave it out for 1",
				path, tomlKey(name), *s)
		}
	}
	return nil
}

// tomlKey writes a table key as the person would have typed it: bare when
// TOML allows, quoted when the name has spaces or anything else in it.
func tomlKey(name string) string {
	if name == "" {
		return `""`
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return strconv.Quote(name)
		}
	}
	return name
}

// ShellScale is the factor the person wants the ghost drawn at, relative to
// its shell's own scale: its own entry, else the global one, else 1. The
// ghost's entry replaces the global factor rather than multiplying it, so
// that "wisp at 0.8" means the same whatever the global setting is.
func (f *File) ShellScale(ghost string) float64 {
	if g, ok := f.Appearance.Ghost[ghost]; ok && g.Scale != nil {
		return *g.Scale
	}
	if f.Appearance.Scale != nil {
		return *f.Appearance.Scale
	}
	return 1
}
