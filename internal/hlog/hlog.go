// Package hlog is the driver's log: leveled, and divided by topic.
//
// The problem it solves is that a companion runs for weeks. Everything
// interesting the driver knows — which surface was created, why a balloon
// was suppressed, what the compositor said about scale, how initiative was
// accounted for — is exactly what you want while debugging and exactly what
// nobody wants scrolling past while they use their desktop. Printing all of
// it makes the log unreadable and the useful lines invisible; printing none
// of it means the answer to "why did it do that" is a rebuild.
//
// So a line has a level and a topic. Levels answer "does this matter" —
// errors and warnings always show, info is the short account of a session's
// life, debug is the running commentary. Topics answer "matter to whom" —
// debug is enabled per subsystem (`HAUNT_DEBUG=backend,wayland`), because a
// person chasing a rendering bug does not want intentions narrating
// themselves.
//
// Default is info with no debug topics: a quiet log of a handful of lines
// per session, which is what shipping software should print.
package hlog

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
)

// Level orders how much a line matters.
type Level int

const (
	// LevelError is for what broke: the session, the brain, an operation
	// the user asked for that cannot be retried.
	LevelError Level = iota
	// LevelWarn is for degraded but continuing — a memory write that
	// failed, a balloon that could not be drawn. The ghost carries on.
	LevelWarn
	// LevelInfo is the account of a session: where it opened, which brain
	// it runs, what the user granted it. The default.
	LevelInfo
	// LevelDebug is the running commentary, off unless its topic is asked
	// for by name.
	LevelDebug
)

// ParseLevel reads a level name, reporting whether it was one.
func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "error":
		return LevelError, true
	case "warn", "warning":
		return LevelWarn, true
	case "info":
		return LevelInfo, true
	case "debug":
		return LevelDebug, true
	}
	return LevelInfo, false
}

func (l Level) String() string {
	switch l {
	case LevelError:
		return "error"
	case LevelWarn:
		return "warn"
	case LevelDebug:
		return "debug"
	}
	return "info"
}

// Topic names the subsystem a line comes from. Debug output is selected by
// these, so they are deliberately few and coarse: a topic nobody would
// think to type is a topic nobody will ever enable.
type Topic string

const (
	// Session is the ghost's life: starting, placing, stopping.
	Session Topic = "session"
	// Brain covers the brain host and the intents it emits.
	Brain Topic = "brain"
	// Backend is requests to models and what came back — but not their content.
	Backend Topic = "backend"
	// Prompt is the content: system prompts, conversation, replies. Its
	// own topic because it is the ghost's private context and the user's
	// words, and because it is enormous.
	Prompt Topic = "prompt"
	// Balloon covers dialogue on screen, including why it was withheld.
	Balloon Topic = "balloon"
	// Anim is poses, clips and repaints.
	Anim Topic = "anim"
	// Input is clicks, drags and the typed line.
	Input Topic = "input"
	// Screen is window awareness and looking.
	Screen Topic = "screen"
	// Memory is intentions, goals and the journal.
	Memory Topic = "memory"
	// Store is persistence.
	Store Topic = "store"
	// Wayland is the compositor: surfaces, outputs, scale, protocol events.
	Wayland Topic = "wayland"
	// Place is where the ghost lives on which screen.
	Place Topic = "place"
	// Curiosity is initiative accounting: what was spent and what was
	// refused.
	Curiosity Topic = "curiosity"
	// Tool is the tool servers a ghost acts through: what was started,
	// what was run, and what refused. Not what a tool was *given* — that
	// is the conversation, and belongs to Prompt if it belongs anywhere.
	Tool Topic = "tool"
	// Control is the socket other programs talk to, and everything that
	// passes between ghosts: tells they address to each other, and what
	// each one saw the others do.
	Control Topic = "control"
)

// Topics is every topic that can be enabled, for `HAUNT_DEBUG=help` and
// the documentation to stay honest about what exists.
var Topics = []Topic{
	Session, Brain, Backend, Prompt, Balloon, Anim, Input,
	Screen, Memory, Store, Wayland, Place, Curiosity, Control, Tool,
}

// Logger writes lines that pass the level and topic filters. Filters are
// per logger; the destination is shared, so loggers derived with With write
// interleaved lines rather than interleaved halves of lines.
type Logger struct {
	out   *sink
	level Level
	// debug holds the topics whose debug lines are wanted. all means
	// everything, whatever the map says.
	debug map[Topic]bool
	all   bool
	// name tags every line with whose it is. Empty for a session with one
	// ghost in it, where naming it would be noise; set by the supervisor
	// when several are resident, because "placed on DP-1" is unreadable
	// when three ghosts say it at once.
	name string
}

// sink is the shared destination and the lock that serialises writes to it.
type sink struct {
	mu sync.Mutex
	w  io.Writer
}

// New returns a logger writing to w. debug lists the topics whose debug
// output is wanted; the topic "all" enables every one.
func New(w io.Writer, level Level, debug []Topic) *Logger {
	l := &Logger{out: &sink{w: w}, level: level, debug: map[Topic]bool{}}
	for _, t := range debug {
		if t == "all" || t == "*" {
			l.all = true
			continue
		}
		l.debug[t] = true
	}
	return l
}

// With returns a logger that also writes the given debug topics, to the
// same destination. It is for the switches that predate topics and mean
// one specific kind of debug output — `[backend] log_prompts`, say — so that
// asking for the output and being allowed to write it stay the same
// decision. Without it a subsystem can be told "trace yourself" and then
// have every line filtered out from under it.
func (l *Logger) With(debug ...Topic) *Logger {
	if l == nil {
		return nil
	}
	out := &Logger{out: l.out, level: l.level, all: l.all, name: l.name, debug: map[Topic]bool{}}
	for t := range l.debug {
		out.debug[t] = true
	}
	for _, t := range debug {
		if t == "all" || t == "*" {
			out.all = true
			continue
		}
		out.debug[t] = true
	}
	return out
}

// Named returns a logger that tags its lines with a ghost's name, writing
// to the same destination and keeping the same filters. An empty name
// returns the logger unchanged, so a lone ghost's output is exactly what it
// has always been.
func (l *Logger) Named(name string) *Logger {
	if l == nil || name == "" {
		return l
	}
	out := &Logger{out: l.out, level: l.level, all: l.all, name: name, debug: map[Topic]bool{}}
	for t := range l.debug {
		out.debug[t] = true
	}
	return out
}

// Name reports the ghost this logger speaks for, empty when it speaks for
// the session as a whole.
func (l *Logger) Name() string {
	if l == nil {
		return ""
	}
	return l.name
}

// Discard is a logger that writes nothing, for tests that do not care.
func Discard() *Logger { return New(io.Discard, LevelError, nil) }

// Enabled reports whether a line at this level and topic would be written.
// Worth checking before assembling anything expensive — a prompt dump, a
// formatted event — since the argument is evaluated whether or not the line
// survives the filter.
func (l *Logger) Enabled(t Topic, lv Level) bool {
	if l == nil {
		return false
	}
	if lv == LevelDebug {
		return l.level >= LevelDebug || l.all || l.debug[t]
	}
	return lv <= l.level
}

func (l *Logger) log(t Topic, lv Level, format string, args ...any) {
	if !l.Enabled(t, lv) {
		return
	}
	msg := fmt.Sprintf(format, args...)
	var prefix string
	switch lv {
	case LevelError:
		prefix = "haunt: error: "
	case LevelWarn:
		prefix = "haunt: warn: "
	case LevelDebug:
		// The topic is on the line because it is also how you would turn
		// the line off again.
		prefix = "haunt: [" + string(t) + "] "
	default:
		prefix = "haunt: "
	}
	if l.name != "" {
		prefix += l.name + ": "
	}
	l.out.mu.Lock()
	defer l.out.mu.Unlock()
	fmt.Fprintln(l.out.w, prefix+msg)
}

// Error reports something that broke.
func (l *Logger) Error(t Topic, format string, args ...any) { l.log(t, LevelError, format, args...) }

// Warn reports degraded but continuing.
func (l *Logger) Warn(t Topic, format string, args ...any) { l.log(t, LevelWarn, format, args...) }

// Info reports part of the session's ordinary account.
func (l *Logger) Info(t Topic, format string, args ...any) { l.log(t, LevelInfo, format, args...) }

// Debug reports running commentary, written only when its topic is asked
// for.
func (l *Logger) Debug(t Topic, format string, args ...any) { l.log(t, LevelDebug, format, args...) }

// Line writes a message that is already formatted, at info. It exists for
// the brain's own `log` intent, whose text is the brain's to phrase.
func (l *Logger) Line(msg string) {
	if l == nil {
		return
	}
	l.out.mu.Lock()
	defer l.out.mu.Unlock()
	fmt.Fprintln(l.out.w, msg)
}

// Describe summarises the filters in force, for the one line worth printing
// about the log itself when it is not at its defaults.
func (l *Logger) Describe() string {
	if l == nil {
		return "off"
	}
	if l.all {
		return l.level.String() + ", debug: all topics"
	}
	if len(l.debug) == 0 {
		return l.level.String()
	}
	names := make([]string, 0, len(l.debug))
	for t := range l.debug {
		names = append(names, string(t))
	}
	sort.Strings(names)
	return l.level.String() + ", debug: " + strings.Join(names, ",")
}

// FromEnv builds a logger from the environment, over the given defaults:
//
//	HAUNT_LOG=warn|info|debug   the level floor (default info)
//	HAUNT_DEBUG=backend,wayland      debug topics, or "all"
//
// Both are additive to whatever the config file asked for, because the
// environment is what a person reaches for when something is wrong right
// now and they do not want to edit a file to see it.
func FromEnv(w io.Writer, level Level, debug []Topic) *Logger {
	if s := os.Getenv("HAUNT_LOG"); s != "" {
		if lv, ok := ParseLevel(s); ok {
			level = lv
		}
	}
	debug = append(debug, parseTopics(os.Getenv("HAUNT_DEBUG"))...)
	// Kept working because they are in the documentation and in people's
	// shell history: the old single-purpose switches are now topics.
	if os.Getenv("HAUNT_BACKEND_DEBUG") == "1" {
		debug = append(debug, Backend, Prompt)
	}
	if os.Getenv("HAUNT_WAYLAND_DEBUG") == "1" {
		debug = append(debug, Wayland)
	}
	return New(w, level, debug)
}

// parseTopics reads a comma-separated topic list.
func parseTopics(s string) []Topic {
	var out []Topic
	for _, part := range strings.Split(s, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		switch part {
		case "":
		case "1", "true", "yes":
			// HAUNT_DEBUG=1 is what everyone tries first.
			out = append(out, "all")
		default:
			out = append(out, Topic(part))
		}
	}
	return out
}

// KnownTopic reports whether name is a topic the driver actually uses, so a
// typo can be answered rather than silently ignored.
func KnownTopic(name string) bool {
	if name == "all" || name == "*" {
		return true
	}
	for _, t := range Topics {
		if string(t) == name {
			return true
		}
	}
	return false
}

// Recorder returns a logger that keeps every line it writes, and a function
// to read them back. For tests that assert on what the driver reported —
// which is worth asserting on, since a refusal the user never hears about
// is a bug that looks like nothing at all.
func Recorder() (*Logger, func() []string) { return RecorderAt(LevelDebug, "all") }

// RecorderAt is Recorder with the filters a test wants to exercise — the
// point being that "was this line written" and "would this line be written
// in the shipping configuration" are different questions.
func RecorderAt(level Level, debug ...Topic) (*Logger, func() []string) {
	buf := &lineBuffer{}
	return New(buf, level, debug), buf.lines
}

// lineBuffer collects written lines under its own lock, since a logger is
// used from every goroutine the driver has.
type lineBuffer struct {
	mu sync.Mutex
	ls []string
}

func (b *lineBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ls = append(b.ls, strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func (b *lineBuffer) lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.ls...)
}
