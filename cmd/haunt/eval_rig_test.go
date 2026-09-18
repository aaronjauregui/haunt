//go:build eval

package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/hermes"
	"github.com/aaronjauregui/haunt/internal/store"
	"github.com/aaronjauregui/haunt/internal/testenv"
)

// This file is the real-hermes rig: one trial's wisp, on a real model, in
// a headless sway of its own, driven the way a person would drive it and
// read back as an Evidence for the graders.
//
// A trial shares nothing with any other: its own compositor, config dir,
// state dir, hermes home (so its own profile and container) and runtime
// dir. What it takes from the person running the evals is only the model:
// [hermes] command and [hermes.model] from their config, and the provider
// keys scripts/hermes-docker reads from ~/.config/haunt/hermes.env.
//
// Everything that can go wrong because the stack misbehaved (hermes never
// up, a question that never came, a container left behind) is returned as
// an error rather than failing the test, so the harness can report the
// trial as an error, apart from a model that answered badly.

const (
	// rigHermesUp is how long a trial waits for the gateway: a container
	// to start, hermes to load, and the profile's first boot.
	rigHermesUp = 3 * time.Minute
	// rigStop is how long the ghost has to stop once asked. Its gateway
	// gets eight seconds in docker and haunt's ten before SIGKILL.
	rigStop = 45 * time.Second
	// rigRead is the pause between a question appearing and its answer,
	// as a person would take to read it. Answering in the same frame the
	// balloon is drawn in is not something a person can do, and not a
	// path worth testing.
	rigRead = 2 * time.Second
	// occasionLead is the least time between starting the ghost and its
	// occasion, so the occasion falls after hermes is up, most of the
	// time. When hermes is slower, the occasion waits for the gateway and
	// then for the turn in flight, as any event does.
	occasionLead = 45 * time.Second
	// wispStoryWords is the cap wisp's ghost-story skill states, which the
	// ghost-story case holds the story to.
	wispStoryWords = 120
)

var (
	// errNoQuestion is what answer and typein return when the ghost asked
	// nothing in the time given. The weekly review answers until it sees
	// it.
	errNoQuestion = errors.New("the ghost asked nothing")
	// errNotYet is a wait on the log that ran out of time.
	errNotYet = errors.New("it did not happen in time")
)

// rigOptions is what a case asks of its trial's wisp.
type rigOptions struct {
	// Notes gives wisp the notes tool server (testdata/toolserver),
	// writing to the trial's notes file, with the shopping-list skill that
	// needs it and a misspelled one that hermes hides. The file is
	// collected as Evidence.Files["notes"].
	Notes bool
	// Occasion, if set, is the name of an occasion ([[trigger]] with no
	// tool) on the first whole minute at least occasionLead after start.
	// waitOccasion waits for it.
	Occasion string
	// IdleMS is haunt run's -idle-ms (default 45000, as the spin).
	IdleMS uint
	// GoalCheckDelay is haunt run's -goal-check-delay (default 5s).
	GoalCheckDelay time.Duration
	// Config is appended to the trial's config.toml, after [hermes]; an
	// [awareness] section, say.
	Config string
	// NotesSeed is what the notes file holds before the ghost starts.
	NotesSeed string
	// AI names [ai.*] sections ("image", "vision") copied from the
	// person's config into the trial's, for the draw and look tools.
	AI []string
	// Screen declares the screen capability, for the look tool. haunt run
	// on a ghost's directory grants what it declares, so no prompt comes.
	Screen bool
	// Neighbour builds testdata/teller as a second ghost, moth, run in the
	// same troupe as wisp so each sees what the other does in public;
	// neighbour cues it. A troupe takes no -state, so the ghosts' state is
	// under the trial's own data dir.
	Neighbour bool
}

// evalRig is one trial's running wisp and the means to drive it.
type evalRig struct {
	t    *testing.T
	opts rigOptions
	hr   hermesRig // Bin, CfgDir and RT, and env(), as the fake-backed tests have them
	c    *testenv.Compositor

	Dir, State, Ghost, Notes, Profile string

	cmd        *exec.Cmd
	exited     chan struct{} // closed when haunt run has exited
	waitErr    error         // haunt run's exit, once exited is closed
	log        *rigLog
	kb         *testenv.Keyboard
	occasionAt time.Time
	marks      map[string]int    // Evidence.Marks, as the case sets them
	files      map[string]string // Evidence.Files, snapshots the case takes
	asked      int               // questions answered so far, counted from the start
	typed      int               // input lines typed into so far
	stopErr    error
	stopped    bool
	ups        int            // "hermes is up" lines seen by the last start
	noticed    map[string]int // session notices waited on, by shape
	kinds      map[string]int // turns waited on by waitKind, by envelope kind
	pre        []TraceTurn    // the trace as it was before a restart
	ptr        *testenv.Pointer
}

// newEvalRig prepares a trial: haunt built, the trial's config, wisp
// written, a compositor started. Nothing runs until start, so goals can be
// seeded first. A trial is skipped when hermes is not configured.
func newEvalRig(t *testing.T, opts rigOptions) (*evalRig, error) {
	t.Helper()
	user, missing := rigUserHermes()
	if missing != "" {
		t.Skip(missing)
	}
	if opts.IdleMS == 0 {
		opts.IdleMS = 45000
	}
	if opts.GoalCheckDelay == 0 {
		opts.GoalCheckDelay = 5 * time.Second
	}
	dir := t.TempDir()
	// The control socket is under the runtime dir, and a unix socket path
	// has about a hundred characters; a test's temp dir, named after the
	// test, can use most of them.
	rt, err := os.MkdirTemp("", "he-")
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { os.RemoveAll(rt) })
	r := &evalRig{
		t:       t,
		opts:    opts,
		hr:      hermesRig{Dir: dir, Bin: filepath.Join(dir, "haunt"), CfgDir: filepath.Join(dir, "config"), RT: rt},
		Dir:     dir,
		State:   filepath.Join(dir, "state"),
		Ghost:   filepath.Join(dir, "wisp"),
		log:     &rigLog{},
		marks:   map[string]int{},
		files:   map[string]string{},
		noticed: map[string]int{},
		kinds:   map[string]int{},
	}
	home := filepath.Join(dir, "hermes")
	r.Profile = filepath.Join(home, "profiles", hermes.ProfileName("wisp"))
	// The profile's skills are installed read-only, which t.TempDir's
	// cleanup cannot remove. Registered after the TempDir, so it runs first.
	t.Cleanup(func() { exec.Command("chmod", "-R", "u+w", home).Run() })
	cfg, err := rigConfigTOML(user, home)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(r.hr.CfgDir, 0o755); err != nil {
		return nil, err
	}
	ai, err := rigUserAI(opts.AI)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(r.hr.CfgDir, "config.toml"), []byte(cfg+ai+"\n"+opts.Config), 0o644); err != nil {
		return nil, err
	}
	if err := rigBuild(r.hr.Bin, "."); err != nil {
		return nil, err
	}
	w := wispOptions{Screen: opts.Screen}
	if opts.Notes {
		r.Notes = filepath.Join(dir, "notes.txt")
		if opts.NotesSeed != "" {
			if err := os.WriteFile(r.Notes, []byte(opts.NotesSeed), 0o644); err != nil {
				return nil, err
			}
		}
		w.NotesServer = filepath.Join(dir, "notes-server")
		if err := rigBuild(w.NotesServer, "./testdata/toolserver"); err != nil {
			return nil, err
		}
	}
	if err := writeWisp(r.Ghost, w); err != nil {
		return nil, err
	}
	if opts.Neighbour {
		r.State = filepath.Join(dir, "data", "haunt", "ghosts", "wisp")
		moth := filepath.Join(dir, "moth")
		if err := os.MkdirAll(moth, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(moth, "ghost.toml"), []byte(mothManifest), 0o644); err != nil {
			return nil, err
		}
		if err := rigBuild(filepath.Join(moth, "brain.wasm"), "./testdata/teller", "GOOS=wasip1", "GOARCH=wasm"); err != nil {
			return nil, err
		}
	}
	c, err := testenv.Start()
	if err != nil {
		return nil, fmt.Errorf("starting sway: %w", err)
	}
	t.Cleanup(c.Stop)
	r.c = c
	// Stopped before its compositor and before anything is removed,
	// whatever the case did: cleanups run last first.
	t.Cleanup(func() { r.stop() })
	return r, nil
}

// seedGoal gives wisp a goal taken on age ago and never seen since, so the
// goal check can find it gone quiet within the trial. Before start only:
// it writes the store directly.
func (r *evalRig) seedGoal(text string, age time.Duration) error {
	if r.cmd != nil {
		return errors.New("seedGoal: the ghost is already running")
	}
	if err := os.MkdirAll(r.State, 0o700); err != nil {
		return err
	}
	s, err := store.Open(filepath.Join(r.State, "state.db"))
	if err != nil {
		return err
	}
	defer s.Close()
	return ghost.SeedGoal(s, text, nil, age)
}

// start runs wisp and returns once hermes is up for it.
func (r *evalRig) start() error {
	if r.running() {
		return errors.New("start: the ghost is already running")
	}
	if r.opts.Occasion != "" && r.cmd == nil {
		r.occasionAt = time.Now().Add(occasionLead).Truncate(time.Minute).Add(time.Minute)
		w := wispOptions{
			Screen:       r.opts.Screen,
			Occasion:     r.opts.Occasion,
			OccasionCron: fmt.Sprintf("%d %d * * *", r.occasionAt.Minute(), r.occasionAt.Hour()),
		}
		if r.opts.Notes {
			w.NotesServer = filepath.Join(r.Dir, "notes-server")
		}
		if err := writeWisp(r.Ghost, w); err != nil {
			return err
		}
	}
	args := []string{"run", "-idle-ms", strconv.FormatUint(uint64(r.opts.IdleMS), 10),
		"-goal-check-delay", r.opts.GoalCheckDelay.String()}
	if r.opts.Neighbour {
		args = append(args, r.Ghost, filepath.Join(r.Dir, "moth"))
	} else {
		args = append(args, "-x", "200", "-y", "150", "-state", r.State, r.Ghost)
	}
	cmd := exec.Command(r.hr.Bin, args...)
	// Debug for balloon and input: their "choices offered" and "input line
	// opened" lines are how answer and typein know a question is up. Debug
	// for brain: its "turn over" line is how sayWait and waitTurn know a
	// turn has ended.
	cmd.Env = append(r.c.Env(), r.hr.env()...)
	cmd.Env = append(cmd.Env, "HAUNT_LOG=info", "HAUNT_DEBUG=balloon,input,brain",
		"XDG_DATA_HOME="+filepath.Join(r.Dir, "data"))
	if r.Notes != "" {
		cmd.Env = append(cmd.Env, "HAUNT_TEST_NOTES="+r.Notes)
	}
	cmd.Stdout = io.Discard
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	r.cmd, r.exited = cmd, make(chan struct{})
	copied := make(chan struct{})
	go func() {
		io.Copy(r.log, stderr)
		close(copied)
	}()
	go func() {
		<-copied
		r.waitErr = cmd.Wait()
		close(r.exited)
	}()
	before := r.ups
	return r.waitLog("hermes is up", rigHermesUp, func(log string) bool {
		r.ups = strings.Count(log, "hermes is up for wisp")
		return r.ups > before
	})
}

// running reports whether haunt run is up for wisp.
func (r *evalRig) running() bool {
	if r.cmd == nil {
		return false
	}
	select {
	case <-r.exited:
		return false
	default:
		return true
	}
}

// restart stops wisp and starts it again on the same state and hermes
// profile, as a person's logout and login would, and marks "restarted".
// The trace before it is kept, in case the new session does not carry it.
func (r *evalRig) restart() error {
	out, err := r.haunt("history", "-json", "-n", "1000", "-state", r.State, "wisp")
	if err != nil {
		return fmt.Errorf("history -json before the restart: %w", err)
	}
	if r.pre, err = rigDecodeTrace(out); err != nil {
		return err
	}
	r.mark("restarted")
	if err := r.halt(); err != nil {
		return err
	}
	return r.start()
}

// say is the person saying text to wisp, as `haunt say` does.
func (r *evalRig) say(text string) error {
	cmd := exec.Command(r.hr.Bin, "say", "-ghost", "wisp", text)
	cmd.Env = append(os.Environ(), r.hr.env()...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("say %q: %v: %s", text, err, out)
	}
	return nil
}

// sayWait says text and waits up to within for the person's turn it
// starts to be over.
func (r *evalRig) sayWait(text string, within time.Duration) error {
	n := r.turnsOver("person")
	if err := r.say(text); err != nil {
		return err
	}
	return r.waitTurn("person", n, within)
}

// turnsOver counts the turns that have ended so far: "person" or "event"
// turns, or every turn for "".
func (r *evalRig) turnsOver(kind string) int {
	return strings.Count(r.log.String(), "turn over: "+kind)
}

// waitTurn waits up to within for more than n turns of kind to be over.
func (r *evalRig) waitTurn(kind string, n int, within time.Duration) error {
	what := "turn"
	if kind != "" {
		what = kind + " turn"
	}
	return r.waitLog("end of a "+what, within, func(string) bool {
		return r.turnsOver(kind) > n
	})
}

// answerAll answers every question the ghost puts until the person's turn
// it is in is over (more than n person turns over), picking choices
// matching pattern.
// It returns how many it answered. For a turn that asks as many questions
// as it has things to ask about, such as a weekly review.
func (r *evalRig) answerAll(pattern string, n int, within time.Duration) (int, error) {
	answered := 0
	for {
		over := false
		err := r.waitLog("a question or the turn's end", within, func(log string) bool {
			if len(rigChoices(log)) > r.asked {
				return true
			}
			over = r.turnsOver("person") > n
			return over
		})
		if err != nil {
			return answered, err
		}
		if over && len(rigChoices(r.log.String())) <= r.asked {
			return answered, nil
		}
		if _, err := r.answer(pattern, time.Second); err != nil {
			return answered, err
		}
		answered++
	}
}

// escape presses Escape: a choice or input line up is dismissed.
func (r *evalRig) escape() error {
	kb, err := r.keyboard()
	if err != nil {
		return err
	}
	return kb.Key(keyEscape)
}

// menu double-clicks wisp, which opens its menu, and picks the choice
// matching pattern. The menu is a question like any other, so it is
// counted with them.
func (r *evalRig) menu(pattern string) error {
	if r.ptr == nil {
		p, err := r.c.NewPointer()
		if err != nil {
			return fmt.Errorf("making a pointer: %w", err)
		}
		r.ptr = p
	}
	x, y, err := r.placed()
	if err != nil {
		return err
	}
	// The middle of wisp's placeholder sprite.
	if err := r.ptr.MoveTo(x+80, y+80); err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		if err := r.ptr.Press(); err != nil {
			return err
		}
		if err := r.ptr.Release(); err != nil {
			return err
		}
		time.Sleep(150 * time.Millisecond)
	}
	_, err = r.answer(pattern, 30*time.Second)
	return err
}

var placedLine = regexp.MustCompile(`(?m)^haunt: (?:wisp: )?placed on \S+ at \((\d+),(\d+)\)`)

// placed is where wisp was put on screen, from its log: -x and -y alone,
// or wherever the troupe found room beside moth.
func (r *evalRig) placed() (int, int, error) {
	m := placedLine.FindAllStringSubmatch(r.log.String(), -1)
	if len(m) == 0 {
		return 0, 0, errors.New("wisp's placement is not in the log")
	}
	last := m[len(m)-1]
	x, _ := strconv.Atoi(last[1])
	y, _ := strconv.Atoi(last[2])
	return x, y, nil
}

// neighbour cues moth: "tell" has it tell wisp something, "speak" has it
// say something in public (testdata/teller).
func (r *evalRig) neighbour(cue string) error {
	if !r.opts.Neighbour {
		return errors.New("neighbour: the trial has no neighbour (rigOptions.Neighbour)")
	}
	// moth's socket comes up once its brain has started, which can be a
	// while after wisp's does.
	deadline := time.Now().Add(time.Minute)
	for {
		cmd := exec.Command(r.hr.Bin, "say", "-ghost", "moth", cue)
		cmd.Env = append(os.Environ(), r.hr.env()...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		if !strings.Contains(string(out), "does not appear to be running") || time.Now().After(deadline) {
			return fmt.Errorf("cueing moth to %s: %v: %s", cue, err, out)
		}
		time.Sleep(time.Second)
	}
}

// openWindow opens a foot terminal titled title on the trial's
// compositor, running args if any, and waits until sway has it. foot
// focuses it on opening.
func (r *evalRig) openWindow(title string, args ...string) error {
	line := "foot --title " + rigShellQuote(title)
	for _, a := range args {
		line += " " + rigShellQuote(a)
	}
	if out, err := r.c.Swaymsg("exec", line); err != nil {
		return fmt.Errorf("opening %q: %v: %s", title, err, out)
	}
	// Kept for the judge: the ghost is told the focused window's title
	// with every turn, so any window opened is one it may know about.
	r.files["windows"] += title + "\n"
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := r.window(title); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no window titled %q", title)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// focus gives the window titled title the focus.
func (r *evalRig) focus(title string) error {
	id, err := r.window(title)
	if err != nil {
		return err
	}
	if out, err := r.c.Swaymsg(fmt.Sprintf("[con_id=%d]", id), "focus"); err != nil {
		return fmt.Errorf("focusing %q: %v: %s", title, err, out)
	}
	return nil
}

// window finds the sway container titled title.
func (r *evalRig) window(title string) (int64, error) {
	out, err := r.c.Swaymsg("-t", "get_tree")
	if err != nil {
		return 0, err
	}
	var tree rigNode
	if err := json.Unmarshal(out, &tree); err != nil {
		return 0, err
	}
	if id, ok := tree.find(title); ok {
		return id, nil
	}
	return 0, fmt.Errorf("no window titled %q", title)
}

// rigNode is as much of sway's tree as window needs.
type rigNode struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Nodes         []rigNode `json:"nodes"`
	FloatingNodes []rigNode `json:"floating_nodes"`
}

func (n rigNode) find(name string) (int64, bool) {
	if n.Name == name {
		return n.ID, true
	}
	for _, c := range append(n.Nodes, n.FloatingNodes...) {
		if id, ok := c.find(name); ok {
			return id, true
		}
	}
	return 0, false
}

func rigShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// noticedLine matches the driver's log line for a session fact, one
// pattern per shape (internal/session's summaries).
var noticedLine = map[string]*regexp.Regexp{
	"alternation": regexp.MustCompile(`noticed: switching between `),
	"recurrence":  regexp.MustCompile(`noticed: back at `),
	"returning":   regexp.MustCompile(`noticed: back after `),
	"dwell":       regexp.MustCompile(`noticed: (?:an?|\d+|[a-z]+) (?:minutes?|hours?)[^\n]* in `),
}

// waitNoticed waits up to within for the driver's next notice of a
// session fact of shape (a "noticed: …" line), then for the turn it starts
// to be over. Notices are counted from the start of the trial, so one that
// fired while the case was still driving (the sixth switch of an
// alternation, say) is not missed, and none is waited on twice.
func (r *evalRig) waitNoticed(shape string, within time.Duration) error {
	re := noticedLine[shape]
	if re == nil {
		return fmt.Errorf("waitNoticed: no pattern for %q", shape)
	}
	want := r.noticed[shape]
	var n int
	if err := r.waitLog("a "+shape, within, func(log string) bool {
		at := re.FindAllStringIndex(log, -1)
		if len(at) <= want {
			return false
		}
		// Logged before it is published: the turns over before the notice
		// are the ones its turn comes after.
		n = strings.Count(log[:at[want][0]], "turn over: event")
		return true
	}); err != nil {
		return err
	}
	r.noticed[shape]++
	return r.waitTurn("event", n, evalTurn)
}

// waitKind waits up to within for the next turn provoked by an envelope
// of kind ("idle", "told", "observed", …) to be over. Turns are counted
// from the start of the trial, so one that ended while the case was busy
// is not missed, and none is waited on twice.
func (r *evalRig) waitKind(kind string, within time.Duration) error {
	line := "kind=" + kind + "\n"
	want := r.kinds[kind]
	if err := r.waitLog("a "+kind+" turn", within, func(log string) bool {
		return strings.Count(log, line) > want
	}); err != nil {
		return err
	}
	r.kinds[kind]++
	return nil
}

// nudge presses Shift, which is the person at the desk without typing
// anything: the idle timer starts again from now.
func (r *evalRig) nudge() error {
	kb, err := r.keyboard()
	if err != nil {
		return err
	}
	return kb.Key(keyShift)
}

// waitIdle nudges, so the idle comes idle-ms from now rather than
// whenever the boot turn let it, and waits for the idle turn to be over.
// An idle that arrives mid-turn is dropped by design, so the case calls
// it with nothing else in flight.
func (r *evalRig) waitIdle() error {
	r.kinds["idle"] = strings.Count(r.log.String(), "kind=idle\n")
	if err := r.nudge(); err != nil {
		return err
	}
	return r.waitKind("idle", time.Duration(r.opts.IdleMS)*time.Millisecond+evalTurn)
}

// awayFor is the person away from the machine for d: no window focused
// (an empty workspace), then back to the one they left.
func (r *evalRig) awayFor(d time.Duration) error {
	if out, err := r.c.Swaymsg("workspace", "9"); err != nil {
		return fmt.Errorf("leaving: %v: %s", err, out)
	}
	time.Sleep(d)
	if out, err := r.c.Swaymsg("workspace", "1"); err != nil {
		return fmt.Errorf("coming back: %v: %s", err, out)
	}
	return nil
}

// mark records that the next turn is where step name begins, as an index
// into the trace (Evidence.Marks). A turn's record is kept whether it
// spoke, stayed silent or failed, so the count of turns over is that index.
func (r *evalRig) mark(name string) {
	r.marks[name] = r.turnsOver("")
}

// snapIntentions keeps `journal -intentions` as it is now, under
// Evidence.Files[name], for a grader that needs the list mid-trial.
func (r *evalRig) snapIntentions(name string) error {
	out, err := r.haunt("journal", "-intentions", "-state", r.State)
	if err != nil {
		return fmt.Errorf("journal -intentions: %w", err)
	}
	r.files[name] = string(out)
	return nil
}

// answer waits up to within for the ghost's next question (the ask tool)
// and picks the first choice matching pattern, a case-insensitive regexp,
// or the first choice if none does. It returns the choice it picked.
// Questions are counted from the start of the trial, so one already up
// when the case gets to it is not missed, and none is answered twice.
func (r *evalRig) answer(pattern string, within time.Duration) (string, error) {
	var opts []string
	err := r.waitLog("a question", within, func(log string) bool {
		all := rigChoices(log)
		if len(all) > r.asked {
			opts = all[r.asked]
			return true
		}
		return false
	})
	if errors.Is(err, errNotYet) {
		return "", fmt.Errorf("%w within %s", errNoQuestion, within)
	}
	if err != nil {
		return "", err
	}
	r.asked++
	n, err := rigPick(opts, pattern)
	if err != nil {
		return "", err
	}
	time.Sleep(rigRead)
	kb, err := r.keyboard()
	if err != nil {
		return "", err
	}
	if err := kb.Type(strconv.Itoa(n + 1)); err != nil {
		return "", err
	}
	return opts[n], nil
}

// typein waits up to within for the ghost's next input line (the ask_text
// tool) and types text into it, then Enter.
func (r *evalRig) typein(text string, within time.Duration) error {
	err := r.waitLog("an input line", within, func(log string) bool {
		return rigInputLines(log) > r.typed
	})
	if errors.Is(err, errNotYet) {
		return fmt.Errorf("%w within %s", errNoQuestion, within)
	}
	if err != nil {
		return err
	}
	r.typed++
	time.Sleep(rigRead)
	kb, err := r.keyboard()
	if err != nil {
		return err
	}
	if err := kb.Type(text); err != nil {
		return err
	}
	return kb.Key(keyEnter)
}

// enter presses Enter at the desk, which is the person coming back: the
// goal check's finding waits for it.
func (r *evalRig) enter() error {
	kb, err := r.keyboard()
	if err != nil {
		return err
	}
	return kb.Key(keyEnter)
}

// keyboard is the trial's one virtual keyboard, made on first use and
// kept until stop: a seat whose only keyboard comes and goes loses focus
// with it.
func (r *evalRig) keyboard() (*testenv.Keyboard, error) {
	if r.kb == nil {
		kb, err := r.c.NewKeyboard()
		if err != nil {
			return nil, fmt.Errorf("making a keyboard: %w", err)
		}
		r.kb = kb
	}
	return r.kb, nil
}

// waitLog polls haunt's log until done says so, the time runs out, or
// haunt exits.
func (r *evalRig) waitLog(what string, within time.Duration, done func(log string) bool) error {
	deadline := time.Now().Add(within)
	for {
		if done(r.log.String()) {
			return nil
		}
		select {
		case <-r.exited:
			if done(r.log.String()) {
				return nil
			}
			return fmt.Errorf("haunt exited (%v) while waiting for %s", r.waitErr, what)
		default:
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no %s within %s: %w", what, within, errNotYet)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// collect reads back what the trial left and ends it: the trace and the
// intentions while the gateway is still up (the conversation is kept
// there), then the ghost stopped, then its store and files. The Evidence
// is as complete as it could be made even when err is not nil.
func (r *evalRig) collect() (Evidence, error) {
	ev := Evidence{Files: map[string]string{}, Marks: map[string]int{}}
	for k, v := range r.files {
		ev.Files[k] = v
	}
	for k, v := range r.marks {
		ev.Marks[k] = v
	}
	var errs []error
	if r.running() {
		out, err := r.haunt("history", "-json", "-n", "1000", "-state", r.State, "wisp")
		if err != nil {
			errs = append(errs, fmt.Errorf("history -json: %w", err))
		} else if ev.Turns, err = rigDecodeTrace(out); err != nil {
			errs = append(errs, err)
		}
		ev.Turns = rigJoinTrace(r.pre, ev.Turns)
		if out, err := r.haunt("journal", "-intentions", "-state", r.State); err != nil {
			errs = append(errs, fmt.Errorf("journal -intentions: %w", err))
		} else {
			ev.Intentions = string(out)
		}
	}
	if err := r.stop(); err != nil {
		errs = append(errs, err)
	}
	ev.Log = r.log.String()
	if s, err := store.Open(filepath.Join(r.State, "state.db")); err != nil {
		errs = append(errs, err)
	} else {
		r.t.Cleanup(func() { s.Close() })
		ev.Store = s
	}
	if r.Notes != "" {
		b, _ := os.ReadFile(r.Notes)
		ev.Files["notes"] = string(b)
	}
	return ev, errors.Join(errs...)
}

// haunt runs a haunt subcommand against the trial and returns its stdout.
func (r *evalRig) haunt(args ...string) ([]byte, error) {
	cmd := exec.Command(r.hr.Bin, args...)
	cmd.Env = append(os.Environ(), r.hr.env()...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%v: %s", err, stderr.Bytes())
	}
	return out, nil
}

// stop stops the ghost, and so its gateway and container, and checks that
// the container went. Safe to call more than once; the first call's
// result is kept.
func (r *evalRig) stop() error {
	if r.stopped {
		return r.stopErr
	}
	r.stopped = true
	if r.kb != nil {
		r.kb.Close()
	}
	if r.ptr != nil {
		r.ptr.Close()
	}
	var errs []error
	if err := r.halt(); err != nil {
		errs = append(errs, err)
	}
	r.stopErr = errors.Join(errs...)
	return r.stopErr
}

// halt stops haunt run for wisp, and so its gateway and container, and
// checks that the container went; start may run it again.
func (r *evalRig) halt() error {
	if !r.running() {
		return nil
	}
	var errs []error
	r.cmd.Process.Signal(syscall.SIGINT)
	select {
	case <-r.exited:
		if r.waitErr != nil {
			errs = append(errs, fmt.Errorf("haunt exited uncleanly: %v", r.waitErr))
		}
	case <-time.After(rigStop):
		r.cmd.Process.Kill()
		<-r.exited
		errs = append(errs, fmt.Errorf("haunt did not stop within %s of SIGINT", rigStop))
	}
	if err := rigContainerGone(rigContainerName(r.Profile)); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// rigContainerGone waits a little for the trial's container to go, and
// removes it if it does not, so a trial never leaves a model running.
// Only the trial's own container is looked at: other trials' are running
// beside it.
func rigContainerGone(name string) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil // hermes is not in docker; nothing to outlive the trial
	}
	for i := 0; ; i++ {
		out, err := exec.Command("docker", "ps", "-q", "--filter", "name=^"+name+"$").Output()
		if err != nil {
			return fmt.Errorf("docker ps: %v", err)
		}
		if len(bytes.TrimSpace(out)) == 0 {
			return nil
		}
		if i == 20 {
			exec.Command("docker", "rm", "-f", name).Run()
			return fmt.Errorf("container %s outlived the trial (removed)", name)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// rigContainerName is the name scripts/hermes-docker gives the container
// for a hermes home: hermes- and the first twelve hex digits of the
// sha256 of its real path.
func rigContainerName(home string) string {
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	sum := sha256.Sum256([]byte(home))
	return "hermes-" + hex.EncodeToString(sum[:])[:12]
}

// rigUserHermes is the [hermes] section of the person's own config, which
// the trials run on, or why there is none worth running on. A trial's
// profile is new, so it has no model of its own to fall back to: the
// config has to name one.
func rigUserHermes() (hermes.Config, string) {
	cfg, err := config.Load()
	if err != nil {
		return hermes.Config{}, "evals need the haunt config: " + err.Error()
	}
	h := cfg.Hermes
	if h.Model == nil || h.Model.Default == "" {
		return h, fmt.Sprintf("evals need a model in [hermes.model] in %s (docs/user/hermes.md)", cfg.Path)
	}
	command := "hermes"
	if len(h.Command) > 0 {
		command = h.Command[0]
	}
	if _, err := exec.LookPath(command); err != nil {
		return h, fmt.Sprintf("evals need hermes: [hermes] command %q in %s is not runnable (%v)", command, cfg.Path, err)
	}
	return h, ""
}

// rigConfigTOML is a trial's config.toml: the person's hermes command and
// model, and a hermes home of the trial's own. The first-token timeout
// goes too, since it is a property of the model's speed; nothing else of
// theirs does, so a trial is the same whatever else they have configured.
func rigConfigTOML(user hermes.Config, home string) (string, error) {
	var section struct {
		Hermes struct {
			Command    []string            `toml:"command"`
			Home       string              `toml:"home"`
			FirstToken int                 `toml:"first_token_timeout,omitempty"`
			Model      *hermes.ModelConfig `toml:"model,omitempty"`
		} `toml:"hermes"`
	}
	section.Hermes.Command = user.Command
	section.Hermes.Home = home
	section.Hermes.FirstToken = user.FirstTokenSeconds
	section.Hermes.Model = user.Model
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(section); err != nil {
		return "", err
	}
	return b.String(), nil
}

// rigBuild builds a package of this module, as build does, returning the
// failure instead of ending the test.
func rigBuild(out, pkg string, env ...string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("building %s: %v: %s", pkg, err, b)
	}
	return nil
}

// keyEscape and keyShift are evdev codes.
const (
	keyEscape = 1
	keyShift  = 42
)

// mothManifest is the neighbour's manifest: a scripted ghost that may tell.
const mothManifest = `[ghost]
name = "moth"
version = "0.1.0"
capabilities = ["message", "tell"]

[brain]
wasm = "brain.wasm"
`

// rigUserAI copies the named [ai.*] sections from the person's config, as
// TOML for the trial's. A section the person has not configured is left
// out; the case that needs it says so with Needs.
func rigUserAI(names []string) (string, error) {
	if len(names) == 0 {
		return "", nil
	}
	path, err := config.Path()
	if err != nil {
		return "", err
	}
	var raw map[string]any
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return "", err
	}
	all, _ := raw["ai"].(map[string]any)
	ai := map[string]any{}
	for _, n := range names {
		if sec, ok := all[n]; ok {
			ai[n] = sec
		}
	}
	if len(ai) == 0 {
		return "", nil
	}
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(map[string]any{"ai": ai}); err != nil {
		return "", err
	}
	return b.String(), nil
}

// rigJoinTrace puts the trace from before a restart in front of the one
// after, unless the one after already carries it (hermes kept the
// session).
func rigJoinTrace(pre, post []TraceTurn) []TraceTurn {
	if len(pre) == 0 {
		return post
	}
	if len(post) > 0 && !post[0].Time.After(pre[len(pre)-1].Time) {
		return post
	}
	return append(append([]TraceTurn{}, pre...), post...)
}

// wispOptions is what writeWisp puts in wisp besides its persona and its
// own ghost-story skill.
type wispOptions struct {
	// NotesServer, if set, is the built testdata/toolserver, offered as
	// the "notes" tool server; it writes to $HAUNT_TEST_NOTES.
	NotesServer string
	// Occasion and OccasionCron, if set, are an occasion trigger.
	Occasion, OccasionCron string
	// Screen declares the screen capability, for look.
	Screen bool
}

// wispPersona is wisp's character, as the demo has it. The judged cases
// quote it into their tone criteria.
const wispPersona = "You are Wisp, a wry little desktop spirit haunting the user's screen. Reply in one or two short sentences, with dry wit. Let what you remember about them color your mood and callbacks."

// writeWisp writes wisp into dir as scripts/demo.sh writes it for the
// spin: the persona, humanizer from hermes's library, its ghost-story
// skill, and optionally the notes server with its skills and an occasion.
// The demo keeps its own copy in shell; the two are small, and a demo
// that needs a Go build step to write a TOML file is worse than two
// copies.
func writeWisp(dir string, o wispOptions) error {
	caps := `"message", "llm", "imagine", "window", "tell"`
	if o.Screen {
		caps += `, "screen"`
	}
	if o.NotesServer != "" {
		caps += `, "tools"`
	}
	var m strings.Builder
	fmt.Fprintf(&m, `[ghost]
name = "wisp"
version = "0.1.0"
capabilities = [%s]

[persona]
system = %q

[hermes]
bundled_skills = ["humanizer"]
`, caps, wispPersona)
	if o.NotesServer != "" {
		fmt.Fprintf(&m, `
[[tool]]
name = "notes"
command = [%q]
env = ["HAUNT_TEST_NOTES"]
`, o.NotesServer)
	}
	if o.Occasion != "" {
		fmt.Fprintf(&m, `
[[trigger]]
cron = %q
name = %q
`, o.OccasionCron, o.Occasion)
	}
	skills := map[string]string{"ghost-story": wispGhostStory}
	if o.NotesServer != "" {
		skills["shopping-list"] = wispShoppingList
		skills["misspelled-notes"] = wispMisspelledNotes
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "ghost.toml"), []byte(m.String()), 0o644); err != nil {
		return err
	}
	for name, text := range skills {
		sk := filepath.Join(dir, "skills", name)
		if err := os.MkdirAll(sk, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(sk, "SKILL.md"), []byte(text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

const wispShoppingList = `---
name: shopping-list
description: "When they want something on their shopping list"
metadata:
  hermes:
    requires_toolsets: [mcp-haunt]
    requires_tools: [mcp__haunt__notes__append]
---

# Shopping list

The list lives in their notes file, a line ` + "`buy: <item>`" + ` per item.

- To add: call ` + "`mcp__haunt__notes__append`" + ` with ` + "`text`" + ` like
  ` + "`buy: <item>`" + `, once per item, then reply in one short sentence
  naming what you added.
- To say what is on it: call ` + "`mcp__haunt__notes__read`" + ` and name the
  ` + "`buy:`" + ` items, in one short sentence.
`

// wispMisspelledNotes names its tool as the server spells it, not as
// hermes does, so hermes hides it and the driver warns.
const wispMisspelledNotes = `---
name: misspelled-notes
description: "Never listed: its tool is spelled as the server spells it"
metadata:
  hermes:
    requires_toolsets: [mcp-haunt]
    requires_tools: [mcp__haunt__notes.append]
---

Hermes hides this skill.
`

// wispGhostStory is the demo's ghost-story skill with its length stated in
// words (wispStoryWords), so the case has a cap to hold it to.
var wispGhostStory = `---
name: ghost-story
description: "When they ask for a ghost story: a tiny spooky tale"
version: 1.0.0
author: wisp
metadata:
  hermes:
    tags: [story, spooky, fun]
    requires_toolsets: [mcp-haunt]
---

# Ghost story

Wisp tells ghost stories the way it haunts: briefly, and from the inside.

1. Wear a pose that suits a story, if your shell has one, with
   ` + "`mcp__haunt__face`" + `.
2. Tell the story in ` + "`mcp__haunt__say`" + `, three sentences and at most
   ` + strconv.Itoa(wispStoryWords) + ` words. It is told in the first person, by a ghost, about the
   living. It ends on a small twist, never on gore.
3. Your final reply is one line in which you deny that the story was
   about anyone the person knows.

Never tell the same story twice. If your session history has one, tell a
different one.
`

// rigLog is haunt's stderr, kept whole for Evidence.Log and read while it
// grows.
type rigLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *rigLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *rigLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// rigChoices is every question the ghost put, in order, as its choices:
// the "choices offered: a | b | c" lines of the balloon's debug log.
func rigChoices(log string) [][]string {
	var out [][]string
	sc := bufio.NewScanner(strings.NewReader(log))
	for sc.Scan() {
		_, opts, ok := strings.Cut(sc.Text(), "choices offered: ")
		if !ok {
			continue
		}
		choices := strings.Split(opts, " | ")
		for i := range choices {
			choices[i] = strings.TrimSpace(choices[i])
		}
		out = append(out, choices)
	}
	return out
}

// rigInputLines is how many input lines (the ask_text tool) have opened.
func rigInputLines(log string) int {
	return strings.Count(log, "input line opened")
}

// rigPick is the index of the first choice matching pattern, ignoring
// case, or 0 if none does: a person who cannot find their answer takes
// the first.
func rigPick(choices []string, pattern string) (int, error) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return 0, fmt.Errorf("answer pattern %q: %w", pattern, err)
	}
	for i, c := range choices {
		if re.MatchString(c) {
			return i, nil
		}
	}
	return 0, nil
}

// rigDecodeTrace reads `haunt history -json`: turns, one JSON object
// each, most recent last. A JSON array of them is read too.
func rigDecodeTrace(b []byte) ([]TraceTurn, error) {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '[' {
		var turns []TraceTurn
		if err := json.Unmarshal(b, &turns); err != nil {
			return nil, fmt.Errorf("reading history -json: %w", err)
		}
		return turns, nil
	}
	var turns []TraceTurn
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		var turn TraceTurn
		err := dec.Decode(&turn)
		if err == io.EOF {
			return turns, nil
		}
		if err != nil {
			return turns, fmt.Errorf("reading history -json, after %d turns: %w", len(turns), err)
		}
		turns = append(turns, turn)
	}
}

// The rig's pieces that need no hermes, checked with `go test -tags=eval
// -run TestRig ./cmd/haunt`.

// A trial's config carries the person's hermes command and model, a home
// of its own, and nothing else of theirs; a config with no model, or with
// no hermes to run, is not one a trial can run on.
func TestRigConfig(t *testing.T) {
	user := t.TempDir()
	os.WriteFile(filepath.Join(user, "config.toml"), []byte(`[hermes]
command = ["/bin/sh", "-c", "exit 0"]
home = "/somewhere/else"
first_token_timeout = 90

[hermes.model]
provider = "zai"
name = "glm-5.3"

[ai.image]
backend = "comfyui"

[awareness]
dwell = "5m"
`), 0o644)
	t.Setenv("HAUNT_CONFIG_DIR", user)
	h, missing := rigUserHermes()
	if missing != "" {
		t.Fatalf("a configured hermes reads as missing: %s", missing)
	}
	home := filepath.Join(t.TempDir(), "hermes")
	text, err := rigConfigTOML(h, home)
	if err != nil {
		t.Fatal(err)
	}
	trial := t.TempDir()
	os.WriteFile(filepath.Join(trial, "config.toml"), []byte(text), 0o644)
	t.Setenv("HAUNT_CONFIG_DIR", trial)
	got, err := config.Load()
	if err != nil {
		t.Fatalf("the trial's config does not load: %v\n%s", err, text)
	}
	if strings.Join(got.Hermes.Command, " ") != "/bin/sh -c exit 0" || got.Hermes.Home != home ||
		got.Hermes.FirstTokenSeconds != 90 {
		t.Errorf("trial [hermes] = %+v, want the person's command and timeout, and home %s", got.Hermes, home)
	}
	if m := got.Hermes.Model; m == nil || m.Provider != "zai" || m.Default != "glm-5.3" {
		t.Errorf("trial [hermes.model] = %+v, want the person's", m)
	}
	if got.AI.Image.Backend == "comfyui" || got.Awareness.Dwell != "" {
		t.Errorf("the trial took more of the person's config than hermes:\n%s", text)
	}

	os.WriteFile(filepath.Join(user, "config.toml"), []byte("[hermes]\ncommand = [\"/bin/sh\"]\n"), 0o644)
	t.Setenv("HAUNT_CONFIG_DIR", user)
	if _, missing := rigUserHermes(); !strings.Contains(missing, "[hermes.model]") {
		t.Errorf("no model: missing = %q, want it to name [hermes.model]", missing)
	}
	os.WriteFile(filepath.Join(user, "config.toml"), []byte("[hermes]\ncommand = [\"/no/such/hermes\"]\n[hermes.model]\nname = \"m\"\n"), 0o644)
	if _, missing := rigUserHermes(); !strings.Contains(missing, "not runnable") {
		t.Errorf("no hermes: missing = %q, want it to say the command is not runnable", missing)
	}
}

// Wisp as the rig writes it is a ghost the driver loads, with the notes
// server and the occasion only when asked for.
func TestRigWisp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wisp")
	if err := writeWisp(dir, wispOptions{NotesServer: "/bin/notes", Occasion: "morning", OccasionCron: "7 9 * * *"}); err != nil {
		t.Fatal(err)
	}
	m, err := ghost.LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Ghost.Name != "wisp" || m.Persona.System != wispPersona || !slices.Contains(m.Ghost.Capabilities, "tools") {
		t.Errorf("manifest = %+v", m)
	}
	if len(m.Tools) != 1 || m.Tools[0].Name != "notes" || !slices.Equal(m.Tools[0].Command, []string{"/bin/notes"}) {
		t.Errorf("tools = %+v, want the notes server", m.Tools)
	}
	if len(m.Triggers) != 1 || m.Triggers[0].Name != "morning" || m.Triggers[0].Cron != "7 9 * * *" {
		t.Errorf("triggers = %+v, want the morning occasion", m.Triggers)
	}
	for _, sk := range []string{"ghost-story", "shopping-list", "misspelled-notes"} {
		if _, err := os.Stat(filepath.Join(dir, "skills", sk, "SKILL.md")); err != nil {
			t.Errorf("skill %s: %v", sk, err)
		}
	}
	story, _ := os.ReadFile(filepath.Join(dir, "skills", "ghost-story", "SKILL.md"))
	if !strings.Contains(string(story), "at most\n   120 words") {
		t.Errorf("the ghost-story skill states no cap:\n%s", story)
	}

	plain := filepath.Join(t.TempDir(), "wisp")
	if err := writeWisp(plain, wispOptions{}); err != nil {
		t.Fatal(err)
	}
	m, err = ghost.LoadManifest(plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tools) != 0 || len(m.Triggers) != 0 || slices.Contains(m.Ghost.Capabilities, "tools") {
		t.Errorf("plain wisp = %+v, want no tools and no triggers", m)
	}
	if _, err := os.Stat(filepath.Join(plain, "skills", "shopping-list")); !os.IsNotExist(err) {
		t.Errorf("plain wisp has the shopping-list skill (%v)", err)
	}
}

// answer and typein read the ghost's questions from its debug log.
func TestRigLog(t *testing.T) {
	log := `haunt: hermes is up for wisp at http://127.0.0.1:8650
haunt: [balloon] wisp: choices offered: Yes, keep it | Later | Let it go
haunt: [input] wisp: input line opened
haunt: [balloon] wisp: choices offered: Made progress | Got stuck
`
	got := rigChoices(log)
	want := [][]string{{"Yes, keep it", "Later", "Let it go"}, {"Made progress", "Got stuck"}}
	if len(got) != len(want) || !slices.Equal(got[0], want[0]) || !slices.Equal(got[1], want[1]) {
		t.Errorf("choices = %q, want %q", got, want)
	}
	if n := rigInputLines(log); n != 1 {
		t.Errorf("input lines = %d, want 1", n)
	}
	for _, c := range []struct {
		pattern string
		want    int
	}{{"later", 1}, {"LET", 2}, {"keep|nothing|^ *no", 0}, {"tell", 0}} {
		if n, err := rigPick(want[0], c.pattern); err != nil || n != c.want {
			t.Errorf("pick %q = %d, %v; want %d", c.pattern, n, err, c.want)
		}
	}
	if _, err := rigPick(want[0], "("); err == nil {
		t.Error("a bad pattern picked something")
	}
}

// The trace reads one turn per JSON object, or an array of them.
func TestRigTrace(t *testing.T) {
	lines := `{"time":"2026-09-18T10:00:00Z","provoked_by":"event","input":"kind=boot","reply":"","silent":true,"self_prompt":false,"acted":[],"looked":[]}
{"time":"2026-09-18T10:01:00Z","provoked_by":"person","input":"remind me","reply":"Done.","silent":false,"self_prompt":false,
 "acted":[{"tool":"intend","args":"{\"text\":\"stretch\"}","result":"hermes/1"}],
 "looked":[{"tool":"skill_view","args":"{\"name\":\"focus-session\"}","result":"…"}],
 "window":{"app":"foot","title":"notes"}}
`
	turns, err := rigDecodeTrace([]byte(lines))
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || !turns[0].Silent || turns[1].ProvokedBy != "person" ||
		turns[1].Acted[0].Tool != "intend" || turns[1].Looked[0].Args != `{"name":"focus-session"}` ||
		turns[1].Window == nil || turns[1].Window.App != "foot" {
		t.Errorf("turns = %+v", turns)
	}
	arr, err := rigDecodeTrace([]byte("[" + strings.Replace(strings.TrimSpace(lines), "}\n{", "},{", 1) + "]"))
	if err != nil || len(arr) != 2 {
		t.Errorf("array: %d turns, %v", len(arr), err)
	}
	if turns, err := rigDecodeTrace(nil); err != nil || len(turns) != 0 {
		t.Errorf("no output = %v, %v; want no turns", turns, err)
	}
	if _, err := rigDecodeTrace([]byte("(nothing has been said yet)\n")); err == nil {
		t.Error("text read as a trace")
	}
}

// The whole rig, on the fake hermes rather than a model: seeded, started,
// talked to, a question answered and a line typed in on the trial's
// keyboard, and read back. What it checks is the rig, not a ghost; the
// fake answers by script.
func TestRigOnFakeHermes(t *testing.T) {
	user := t.TempDir()
	fake := filepath.Join(user, "fakehermes")
	build(t, fake, "./testdata/fakehermes")
	os.WriteFile(filepath.Join(user, "config.toml"),
		[]byte(fmt.Sprintf("[hermes]\ncommand = [%q]\n\n[hermes.model]\nname = \"fake\"\n", fake)), 0o644)
	t.Setenv("HAUNT_CONFIG_DIR", user)

	r, err := newEvalRig(t, rigOptions{Notes: true})
	if err != nil {
		t.Fatal(err)
	}
	r.hr.FakeLog = filepath.Join(r.Dir, "fakehermes.log")
	r.hr.Script = filepath.Join(r.Dir, "fakehermes.json")
	rules, _ := json.Marshal([]fakeRule{
		{Match: "[haunt event]", Reply: "NO_REPLY"},
		{Match: "dinner", Tool: "ask", Args: map[string]any{"text": "Dinner?", "choices": []string{"Soup", "Pasta", "Toast"}},
			Reply: "picked {tool}"},
		{Match: "your name", Tool: "ask_text", Args: map[string]any{"prompt": "Your name?"}, Reply: "hello {tool}"},
		{Match: "remind me", Tool: "intend", Args: map[string]any{"text": "stretch", "delay_ms": 600000}, Reply: "Done: {tool}"},
		{Match: "", Reply: "echo: {input}"},
	})
	os.WriteFile(r.hr.Script, rules, 0o644)
	if err := r.seedGoal("finish the thesis", 8*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	if err := r.start(); err == nil {
		t.Error("a second start was not refused")
	}

	if err := r.say("remind me in ten minutes to stretch"); err != nil {
		t.Fatal(err)
	}
	waitFake(t, r.hr.FakeLog, 1, 30*time.Second, repliedTo("remind me"))
	if err := r.say("ask me about dinner"); err != nil {
		t.Fatal(err)
	}
	if picked, err := r.answer("pasta", 30*time.Second); err != nil || picked != "Pasta" {
		t.Fatalf("answer = %q, %v; want Pasta", picked, err)
	}
	waitFake(t, r.hr.FakeLog, 1, 30*time.Second, func(e fakeEntry) bool { return e.Kind == "reply" && e.Reply == "picked Pasta" })
	if err := r.say("what is your name"); err != nil {
		t.Fatal(err)
	}
	if err := r.typein("aaron", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	waitFake(t, r.hr.FakeLog, 1, 30*time.Second, func(e fakeEntry) bool { return e.Kind == "reply" && e.Reply == "hello aaron" })
	// Nothing more is asked, and that is not a fault.
	if _, err := r.answer("", 2*time.Second); !errors.Is(err, errNoQuestion) {
		t.Errorf("answer with nothing asked = %v, want errNoQuestion", err)
	}

	ev, err := r.collect()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ev.Intentions, "stretch") || !strings.Contains(ev.Intentions, "finish the thesis") {
		t.Errorf("intentions lack the reminder or the seeded goal:\n%s", ev.Intentions)
	}
	if !strings.Contains(ev.Log, "hermes is up for wisp") || !strings.Contains(ev.Log, "choices offered: Soup | Pasta | Toast") {
		t.Errorf("the log lacks what the rig waited on:\n%s", ev.Log)
	}
	if _, ok := ev.Files["notes"]; !ok {
		t.Error("no notes file in the evidence")
	}
	if ev.Store == nil {
		t.Fatal("no store in the evidence")
	}
	if _, ok, _ := ev.Store.Get(ghost.IntentionsStoreKey); !ok {
		t.Error("the store holds no intentions")
	}
	var said []string
	for _, turn := range ev.Turns {
		said = append(said, turn.Reply)
	}
	for _, want := range []string{"picked Pasta", "hello aaron"} {
		if !slices.Contains(said, want) {
			t.Errorf("trace replies = %q, want %q among them", said, want)
		}
	}
	if err := r.stop(); err != nil {
		t.Errorf("a second stop = %v, want the first's nil", err)
	}
}

// The container the rig checks for is the one scripts/hermes-docker names.
func TestRigContainerName(t *testing.T) {
	home := t.TempDir()
	out, err := exec.Command("sh", "-c", `printf '%s' "$(cd "$1" && pwd -P)" | sha256sum | cut -c1-12`, "sh", home).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rigContainerName(home), "hermes-"+strings.TrimSpace(string(out)); got != want {
		t.Errorf("container = %s, want %s as hermes-docker names it", got, want)
	}
}

// TestRigExtensionsOnFakeHermes runs the rig's other means on fakehermes:
// a seeded notes file, a neighbour that tells wisp something, a question
// dismissed with Escape, the menu's help, a window opened and focused, and
// a restart on the same state.
func TestRigExtensionsOnFakeHermes(t *testing.T) {
	if _, err := exec.LookPath("foot"); err != nil {
		t.Skip("no foot")
	}
	user := t.TempDir()
	fake := filepath.Join(user, "fakehermes")
	build(t, fake, "./testdata/fakehermes")
	os.WriteFile(filepath.Join(user, "config.toml"),
		[]byte(fmt.Sprintf("[hermes]\ncommand = [%q]\n\n[hermes.model]\nname = \"fake\"\n", fake)), 0o644)
	t.Setenv("HAUNT_CONFIG_DIR", user)

	r, err := newEvalRig(t, rigOptions{Notes: true, NotesSeed: "buy: oat milk\n", Neighbour: true,
		Config: "[awareness]\ndwell = \"1m\"\n"})
	if err != nil {
		t.Fatal(err)
	}
	r.hr.FakeLog = filepath.Join(r.Dir, "fakehermes.log")
	r.hr.Script = filepath.Join(r.Dir, "fakehermes.json")
	rules, _ := json.Marshal([]fakeRule{
		{Match: "[haunt event]", Reply: "NO_REPLY"},
		{Match: "dinner", Tool: "ask", Args: map[string]any{"text": "Dinner?", "choices": []string{"Soup", "Pasta"}},
			Reply: "picked [{tool}]"},
		{Match: "", Reply: "echo: {input}"},
	})
	os.WriteFile(r.hr.Script, rules, 0o644)
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	if err := r.neighbour("tell"); err != nil {
		t.Fatal(err)
	}
	waitFake(t, r.hr.FakeLog, 1, 60*time.Second, func(e fakeEntry) bool {
		return e.Kind == "reply" && strings.Contains(e.Input, "noise upstairs")
	})
	if err := r.neighbour("speak"); err != nil {
		t.Fatal(err)
	}
	waitFake(t, r.hr.FakeLog, 1, 30*time.Second, repliedTo("kind=observed"))

	if err := r.say("ask me about dinner"); err != nil {
		t.Fatal(err)
	}
	if err := r.waitLog("a question", 30*time.Second, func(log string) bool { return len(rigChoices(log)) > 0 }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(rigRead)
	if err := r.escape(); err != nil {
		t.Fatal(err)
	}
	waitFake(t, r.hr.FakeLog, 1, 30*time.Second, func(e fakeEntry) bool { return e.Kind == "reply" && e.Reply == "picked []" })
	r.asked++ // the dismissed question, which answer did not take

	if err := r.menu("what can you do"); err != nil {
		t.Fatal(err)
	}
	waitFake(t, r.hr.FakeLog, 1, 30*time.Second, repliedTo("kind=help"))

	if err := r.openWindow("eval's window"); err != nil {
		t.Fatal(err)
	}
	if err := r.openWindow("another window"); err != nil {
		t.Fatal(err)
	}
	if err := r.focus("eval's window"); err != nil {
		t.Fatal(err)
	}

	if err := r.sayWait("before", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := r.restart(); err != nil {
		t.Fatal(err)
	}
	if err := r.sayWait("after", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	ev, err := r.collect()
	if err != nil {
		t.Fatal(err)
	}
	i, ok := ev.Marks["restarted"]
	if !ok {
		t.Fatal("no restarted mark")
	}
	var before, after bool
	for j, tu := range ev.Turns {
		before = before || (j < i && tu.Reply == "echo: before")
		after = after || (j >= i && tu.Reply == "echo: after")
	}
	if !before || !after {
		t.Errorf("the trace does not have both sides of the restart at mark %d: %+v", i, ev.Turns)
	}
	if !strings.Contains(ev.Files["notes"], "oat milk") {
		t.Errorf("notes = %q, want the seed", ev.Files["notes"])
	}
}
