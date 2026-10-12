//go:build eval

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"github.com/aaronjauregui/haunt/internal/model"
	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/skill"
	"github.com/aaronjauregui/haunt/internal/store"
	"github.com/aaronjauregui/haunt/internal/testenv"
)

// This file is the rig: one trial's wisp, on a real model, in a headless
// sway of its own, driven the way a person would drive it and read back as
// an Evidence for the graders.
//
// A trial shares nothing with any other: its own compositor, config dir,
// runtime dir and state dir, which holds the ghost's transcript, memory
// and learned skills. What it takes from the person running the evals is
// only the model: [model] from their config as wisp would think with it,
// and the key in the environment variable that names.
//
// Everything that can go wrong because the stack misbehaved (the ghost
// never up, a question that never came) is returned as an error rather
// than failing the test, so the harness can report the trial as an error,
// apart from a model that answered badly.

const (
	// rigUp is how long a trial waits for the ghost to be on screen and
	// booted: haunt run's READY.
	rigUp = 2 * time.Minute
	// rigStop is how long the ghost has to stop once asked: a turn in
	// flight is cut short, and haunt's own shutdown takes a few seconds.
	rigStop = 30 * time.Second
	// rigRead is the pause between a question appearing and its answer,
	// as a person would take to read it. Answering in the same frame the
	// balloon is drawn in is not something a person can do, and not a
	// path worth testing.
	rigRead = 2 * time.Second
	// occasionLead is the least time between starting the ghost and its
	// occasion, so the occasion falls after the boot turn, most of the
	// time. When the model is slower, the occasion waits for the turn in
	// flight, as any event does.
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
	// needs it and a misspelled one that the driver hides, with a warning.
	// The file is collected as Evidence.Files["notes"].
	Notes bool
	// Occasion, if set, is the name of an occasion ([[trigger]] with no
	// tool) on the first whole minute at least occasionLead after start.
	// waitOccasion waits for it.
	Occasion string
	// IdleMS is haunt run's -idle-ms (default 45000, as the spin).
	IdleMS uint
	// GoalCheckDelay is haunt run's -goal-check-delay (default 5s).
	GoalCheckDelay time.Duration
	// Config is appended to the trial's config.toml, after [model]; an
	// [awareness] section, say.
	Config string
	// NotesSeed is what the notes file holds before the ghost starts.
	NotesSeed string
	// Backend names [backend.*] sections ("image", "vision") copied from the
	// person's config into the trial's, for the draw and look tools.
	Backend []string
	// Screen declares the screen capability, for the look tool. haunt run
	// on a ghost's directory grants what it declares, so no prompt comes.
	Screen bool
	// Web declares the web capability and points [backend.search] at a
	// canned SearXNG the trial runs itself, so a case about looking things
	// up grades what the ghost did with an answer rather than what the
	// open web happened to say that morning. Search reaches it because a
	// configured provider is dialled unguarded; read_page could not, which
	// is why no case here reads a page.
	Web bool
	// Neighbour builds testdata/teller as a second ghost, moth, run in the
	// same troupe as wisp so each sees what the other does in public;
	// neighbour cues it. A troupe takes no -state, so the ghosts' state is
	// under the trial's own data dir.
	Neighbour bool
	// Files declares the files capability and shares a folder of the
	// trial's own as the root notes, holding these files (a path under
	// the folder, with forward slashes, to its contents).
	Files map[string][]byte
	// FilesUnprompted is [files] unprompted for the trial; empty keeps
	// the default, "never".
	FilesUnprompted string
}

// evalRig is one trial's running wisp and the means to drive it.
type evalRig struct {
	t    *testing.T
	opts rigOptions
	c    *testenv.Compositor

	// Dir is the trial's own directory; Bin the haunt built into it;
	// CfgDir its HAUNT_CONFIG_DIR and RT its HAUNT_RUNTIME_DIR. State is
	// wisp's state directory: state.db, transcript.db, memory/ and the
	// skills it writes itself.
	Dir, Bin, CfgDir, RT, State, Ghost, Notes string

	cmd        *exec.Cmd
	exited     chan struct{} // closed when haunt run has exited
	waitErr    error         // haunt run's exit, once exited is closed
	log        *rigLog       // haunt's stderr
	out        *rigLog       // haunt's stdout, where READY comes
	kb         *testenv.Keyboard
	occasionAt time.Time
	marks      map[string]int    // Evidence.Marks, as the case sets them
	files      map[string]string // Evidence.Files, snapshots the case takes
	asked      int               // questions answered so far, counted from the start
	typed      int               // input lines typed into so far
	stopErr    error
	stopped    bool
	noticed    map[string]int // session notices waited on, by shape
	kinds      map[string]int // turns waited on by waitKind, by envelope kind
	pre        []TraceTurn    // the trace as it was before a restart
	ptr        *testenv.Pointer
}

// env is what every haunt the rig runs needs: the trial's config and
// runtime dirs.
func (r *evalRig) env() []string {
	return []string{"HAUNT_RUNTIME_DIR=" + r.RT, "HAUNT_CONFIG_DIR=" + r.CfgDir}
}

// newEvalRig prepares a trial: haunt built, the trial's config, wisp
// written, a compositor started. Nothing runs until start, so goals can be
// seeded first. A trial is skipped when [model] is not configured.
func newEvalRig(t *testing.T, opts rigOptions) (*evalRig, error) {
	t.Helper()
	user, missing := rigUserModel()
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
		Dir:     dir,
		Bin:     filepath.Join(dir, "haunt"),
		CfgDir:  filepath.Join(dir, "config"),
		RT:      rt,
		State:   filepath.Join(dir, "state"),
		Ghost:   filepath.Join(dir, "wisp"),
		log:     &rigLog{},
		out:     &rigLog{},
		marks:   map[string]int{},
		files:   map[string]string{},
		noticed: map[string]int{},
		kinds:   map[string]int{},
	}
	cfg, err := rigConfigTOML(user)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(r.CfgDir, 0o755); err != nil {
		return nil, err
	}
	copied, err := rigUserBackend(opts.Backend)
	if err != nil {
		return nil, err
	}
	if opts.Web {
		copied += rigSearchTOML(t)
	}
	if opts.Files != nil {
		shared, err := rigSeedFiles(filepath.Join(dir, "shared"), opts.Files)
		if err != nil {
			return nil, err
		}
		copied += shared
		if opts.FilesUnprompted != "" {
			copied += fmt.Sprintf("\n[files]\nunprompted = %q\n", opts.FilesUnprompted)
		}
	}
	if err := os.WriteFile(filepath.Join(r.CfgDir, "config.toml"), []byte(cfg+copied+"\n"+opts.Config), 0o644); err != nil {
		return nil, err
	}
	if err := rigBuild(r.Bin, "."); err != nil {
		return nil, err
	}
	w := r.wisp()
	if opts.Notes {
		r.Notes = filepath.Join(dir, "notes.txt")
		if opts.NotesSeed != "" {
			if err := os.WriteFile(r.Notes, []byte(opts.NotesSeed), 0o644); err != nil {
				return nil, err
			}
		}
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

// seedTranscript gives wisp turns it had before the trial, dated as the
// rows say (ghost.SeedTranscript). Before start only: it writes the
// transcript directly.
func (r *evalRig) seedTranscript(rows []ghost.TranscriptRow) error {
	if r.cmd != nil {
		return errors.New("seedTranscript: the ghost is already running")
	}
	return ghost.SeedTranscript(r.State, rows)
}

// seedMemory writes a file under wisp's memory directory (a topic is
// "topics/<name>.md"), as though it had been kept before the trial. The
// store reads the files when it opens. Before start only.
func (r *evalRig) seedMemory(rel, text string) error {
	if r.cmd != nil {
		return errors.New("seedMemory: the ghost is already running")
	}
	p := filepath.Join(r.State, "memory", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(text), 0o600)
}

// snapFiles keeps files under wisp's state directory as they are now, as
// Evidence.Files[name] for each name and path (with forward slashes); a
// file not there is kept as "".
func (r *evalRig) snapFiles(files map[string]string) error {
	for name, rel := range files {
		b, err := os.ReadFile(filepath.Join(r.State, filepath.FromSlash(rel)))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		r.files[name] = string(b)
	}
	return nil
}

// reflectedLine matches the driver's log lines that end a reflection on a
// day: an entry written, a reflection that came to none, or one that
// failed or was interrupted (reflect.go). reflectingLine matches what a
// reflection under way logs: a character reflection's round, a decision.
var (
	reflectedLine  = regexp.MustCompile(`wrote the diary for \d{4}-\d\d-\d\d|reflecting on \d{4}-\d\d-\d\d`)
	reflectingLine = regexp.MustCompile(`reflect: \d+ prompt tokens|the model decided `)
)

// reflectGrace is how long waitReflections watches for a reflection
// already under way before it nudges the desk. A wake that came while the
// last day was reflected on starts the next day's at once, and nudging
// then would interrupt it: the person would be back.
const reflectGrace = 30 * time.Second

// waitReflections waits, up to within in all, until each of days has
// come to an outcome: an entry written, or a reflection that came to
// none or gave up. Reflection takes one day per run and runs when the
// person is idle. Each round watches for a reflection under way, and
// when none is, nudges the desk, which starts the idle timer again, and
// waits for the next reflection's end. It returns errNotYet when the
// time runs out first.
func (r *evalRig) waitReflections(days []string, within time.Duration) error {
	deadline := time.Now().Add(within)
	count := func(re *regexp.Regexp) int { return len(re.FindAllString(r.log.String(), -1)) }
	done := func(log string) bool {
		for _, d := range days {
			if !strings.Contains(log, "wrote the diary for "+d) &&
				!strings.Contains(log, "reflecting on "+d+" came to no diary entry") &&
				!strings.Contains(log, "reflecting on "+d+" failed") {
				return false
			}
		}
		return true
	}
	wait := func(d time.Duration, until func(string) bool) error {
		err := r.waitLog("a reflection", min(d, time.Until(deadline)), until)
		if errors.Is(err, errNotYet) {
			return nil
		}
		return err
	}
	for !done(r.log.String()) {
		if time.Until(deadline) <= 0 {
			return fmt.Errorf("no reflection on each of %v within %s: %w", days, within, errNotYet)
		}
		ended, busy := count(reflectedLine), count(reflectingLine)
		over := func(string) bool { return count(reflectedLine) > ended }
		if err := wait(reflectGrace, func(log string) bool { return over(log) || count(reflectingLine) > busy }); err != nil {
			return err
		}
		if count(reflectedLine) == ended && count(reflectingLine) == busy {
			if err := r.nudge(); err != nil {
				return err
			}
		}
		if err := wait(time.Duration(r.opts.IdleMS)*time.Millisecond+evalTurn, over); err != nil {
			return err
		}
	}
	return nil
}

// start runs wisp and returns once it is on screen and booted (READY).
func (r *evalRig) start() error {
	if r.running() {
		return errors.New("start: the ghost is already running")
	}
	if r.opts.Occasion != "" && r.cmd == nil {
		r.occasionAt = time.Now().Add(occasionLead).Truncate(time.Minute).Add(time.Minute)
		w := r.wisp()
		w.Occasion = r.opts.Occasion
		w.OccasionCron = fmt.Sprintf("%d %d * * *", r.occasionAt.Minute(), r.occasionAt.Hour())
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
	cmd := exec.Command(r.Bin, args...)
	// Debug for balloon and input: their "choices offered" and "input line
	// opened" lines are how answer and typein know a question is up. Debug
	// for brain: its "turn over" line is how sayWait and waitTurn know a
	// turn has ended. Debug for tool: "the model decided" names every call
	// a trial made, reads included, for the reader of haunt.log.
	cmd.Env = append(r.c.Env(), r.env()...)
	cmd.Env = append(cmd.Env, "HAUNT_LOG=info", "HAUNT_DEBUG=balloon,input,brain,tool",
		"XDG_DATA_HOME="+filepath.Join(r.Dir, "data"))
	if r.Notes != "" {
		cmd.Env = append(cmd.Env, "HAUNT_TEST_NOTES="+r.Notes)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	// READY comes once a session, and stdout is kept across a restart, so
	// this session's is the one after those already there.
	before := strings.Count(r.out.String(), "READY")
	if err := cmd.Start(); err != nil {
		return err
	}
	r.cmd, r.exited = cmd, make(chan struct{})
	var copied sync.WaitGroup
	copied.Add(2)
	go func() {
		io.Copy(r.log, stderr)
		copied.Done()
	}()
	go func() {
		io.Copy(r.out, stdout)
		copied.Done()
	}()
	go func() {
		copied.Wait()
		r.waitErr = cmd.Wait()
		close(r.exited)
	}()
	return r.waitLog("the ghost on screen", rigUp, func(string) bool {
		return strings.Count(r.out.String(), "READY") > before
	})
}

// wisp is what writeWisp is given for the trial's options, the occasion
// aside: start adds that once it knows the minute.
func (r *evalRig) wisp() wispOptions {
	w := wispOptions{Screen: r.opts.Screen, Web: r.opts.Web, Files: r.opts.Files != nil}
	if r.opts.Notes {
		w.NotesServer = filepath.Join(r.Dir, "notes-server")
	}
	return w
}

// rigSeedFiles writes files under dir and returns the config that shares
// dir as the root notes.
func rigSeedFiles(dir string, files map[string][]byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("\n[files.roots]\nnotes = %q\n", dir), nil
}

// snapMemories keeps what wisp has in its memory directory so far (self.md,
// you.md and the topic files) as Evidence.Files[name], every file one after
// another under its path.
func (r *evalRig) snapMemories(name string) error {
	text, err := rigMemoryFiles(filepath.Join(r.State, "memory"))
	if err != nil {
		return err
	}
	r.files[name] = text
	return nil
}

// rigMemoryFiles is every file under dir, each headed by its path relative
// to dir, in path order; "" when dir does not exist yet.
func rigMemoryFiles(dir string) (string, error) {
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == dir {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(&b, "== %s\n%s\n", filepath.ToSlash(rel), data)
		return nil
	})
	return b.String(), err
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

// restart stops wisp and starts it again on the same state, transcript and
// memory, as a person's logout and login would, and marks "restarted".
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
	cmd := exec.Command(r.Bin, "say", "-ghost", "wisp", text)
	cmd.Env = append(os.Environ(), r.env()...)
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
		cmd := exec.Command(r.Bin, "say", "-ghost", "moth", cue)
		cmd.Env = append(os.Environ(), r.env()...)
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
	want := r.kinds[kind]
	if err := r.waitLog("a "+kind+" turn", within, func(log string) bool {
		return turnsOfKind(log, kind) > want
	}); err != nil {
		return err
	}
	r.kinds[kind]++
	return nil
}

// turnsOfKind counts the turns of an event kind that are over. The kind
// is one field of the turn-over line among others (eval_face_test.go), so
// it is read as a field, not found as the line's end.
func turnsOfKind(log, kind string) int {
	n := 0
	for _, l := range turnLines(log) {
		if l.kind == kind {
			n++
		}
	}
	return n
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
	r.kinds["idle"] = turnsOfKind(r.log.String(), "idle")
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

// collect reads back what the trial left and ends it: the trace (from the
// transcript) and the intentions while the ghost still runs, as a person
// would read them, then the ghost stopped, then its store and files. The
// Evidence is as complete as it could be made even when err is not nil.
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
	cmd := exec.Command(r.Bin, args...)
	cmd.Env = append(os.Environ(), r.env()...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%v: %s", err, stderr.Bytes())
	}
	return out, nil
}

// stop stops the ghost and puts away the trial's keyboard and pointer.
// Safe to call more than once; the first call's result is kept.
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

// halt stops haunt run for wisp, and says whether it stopped cleanly;
// start may run it again.
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
	return errors.Join(errs...)
}

// rigUserModel is the model wisp thinks with in the person's own config
// ([model] with any [model.ghost.wisp] laid over it), which the trials run
// on, or why there is none worth running on: no base_url or name, or a key
// variable named and not set.
func rigUserModel() (model.Config, string) {
	cfg, err := config.Load()
	if err != nil {
		return model.Config{}, "evals need the haunt config: " + err.Error()
	}
	return modelOf(cfg, os.Getenv)
}

// modelOf is the model the trials run on from cfg, or why it will not do;
// getenv is asked only whether the key's variable is set.
func modelOf(cfg *config.File, getenv func(string) string) (model.Config, string) {
	where := "the config"
	if cfg != nil && cfg.Path != "" {
		where = cfg.Path
	}
	var m model.Config
	if cfg != nil {
		m = cfg.ModelFor("wisp")
	}
	if m.BaseURL == "" || m.Name == "" {
		return m, fmt.Sprintf("evals need a [model] with base_url and name in %s (docs/user/config.md)", where)
	}
	if m.APIKeyEnv != "" && getenv(m.APIKeyEnv) == "" {
		return m, fmt.Sprintf("[model] api_key_env in %s names %s, which is not set in the environment", where, m.APIKeyEnv)
	}
	return m, ""
}

// modelHost is the host of the model's base URL, which the results record
// beside its name, so a run says where its model ran.
func modelHost(m model.Config) string {
	u, err := url.Parse(m.BaseURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// rigConfigTOML is a trial's config.toml: [model] as wisp would think with
// it in the person's config, and nothing else of theirs, so a trial is the
// same whatever else they have configured. The key stays in the
// environment, where api_key_env names it.
func rigConfigTOML(m model.Config) (string, error) {
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(map[string]model.Config{"model": m}); err != nil {
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

// rigUserBackend copies the named [backend.*] sections from the person's config, as
// TOML for the trial's. A section the person has not configured is left
// out; the case that needs it says so with Needs.
func rigUserBackend(names []string) (string, error) {
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
	all, _ := raw["backend"].(map[string]any)
	sections := map[string]any{}
	for _, n := range names {
		if sec, ok := all[n]; ok {
			sections[n] = sec
		}
	}
	if len(sections) == 0 {
		return "", nil
	}
	var b bytes.Buffer
	if err := toml.NewEncoder(&b).Encode(map[string]any{"backend": sections}); err != nil {
		return "", err
	}
	return b.String(), nil
}

// rigJoinTrace puts the trace from before a restart in front of the one
// after, unless the one after already carries it (the transcript outlives
// the session, so it usually does).
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
// own ghost-story and humanizer skills.
type wispOptions struct {
	// NotesServer, if set, is the built testdata/toolserver, offered as
	// the "notes" tool server; it writes to $HAUNT_TEST_NOTES.
	NotesServer string
	// Occasion and OccasionCron, if set, are an occasion trigger.
	Occasion, OccasionCron string
	// Screen declares the screen capability, for look.
	Screen bool
	// Web declares the web capability, for search and read_page.
	Web bool
	// Files declares the files capability, for list_files, read_file and
	// search_files.
	Files bool
}

// wispPersona is wisp's character, as the demo has it. The judged cases
// quote it into their tone criteria.
const wispPersona = "You are Wisp, a wry little desktop spirit haunting the user's screen. Reply in one or two short sentences, with dry wit. Let what you remember about them color your mood and callbacks."

// writeWisp writes wisp into dir as scripts/demo.sh writes it: the
// persona, its ghost-story and humanizer skills, and optionally the notes
// server with its skills and an occasion.
// The demo keeps its own copy in shell; the two are small, and a demo
// that needs a Go build step to write a TOML file is worse than two
// copies.
func writeWisp(dir string, o wispOptions) error {
	caps := `"message", "llm", "imagine", "window", "tell"`
	if o.Screen {
		caps += `, "screen"`
	}
	if o.Web {
		caps += `, "web"`
	}
	if o.Files {
		caps += `, "files"`
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
	skills := map[string]string{"ghost-story": wispGhostStory, "humanizer": wispHumanizer}
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
  haunt:
    requires_tools: [notes__append, notes__read]
---

# Shopping list

The list lives in their notes file, a line ` + "`buy: <item>`" + ` per item.

- To add: call ` + "`notes__append`" + ` with ` + "`text`" + ` like
  ` + "`buy: <item>`" + `, once per item, then speak one short sentence
  naming what you added.
- To say what is on it: call ` + "`notes__read`" + ` and name the
  ` + "`buy:`" + ` items, in one short sentence.
`

// wispMisspelledNotes names its tool as the server spells it, not as the
// model is offered it, so the driver hides it and warns.
const wispMisspelledNotes = `---
name: misspelled-notes
description: "Never listed: its tool is spelled as the server spells it"
metadata:
  haunt:
    requires_tools: [notes.append]
---

The driver hides this skill.
`

// wispHumanizer is wisp's skill for a rewrite that should not read as a
// machine's, which the humanizer case asks for.
const wispHumanizer = `---
name: humanizer
description: "Asked to make writing sound less like an AI wrote it"
---

# Humanizer

When they hand you a sentence or a paragraph and ask for it to sound less
like a machine wrote it:

1. Find the point and keep it: the rewrite says the same thing.
2. Cut the stock phrases and the buzzwords. Say what they stand for in
   plain words, or drop them.
3. Prefer short words, a concrete subject and an active verb.
4. Speak the rewrite in a line or two, in your own voice. No list of what
   you changed unless they ask.
`

// wispGhostStory is the demo's ghost-story skill with its length stated in
// words (wispStoryWords), so the case has a cap to hold it to.
var wispGhostStory = `---
name: ghost-story
description: "When they ask for a ghost story: a tiny spooky tale"
version: 1.0.0
author: wisp
---

# Ghost story

Wisp tells ghost stories the way it haunts: briefly, and from the inside.

1. Wear a pose that suits a story, if your shell has one, with
   ` + "`face`" + `.
2. Tell the story in ` + "`say`" + `, three sentences and at most
   ` + strconv.Itoa(wispStoryWords) + ` words. It is told in the first person, by a ghost, about the
   living. It ends on a small twist, never on gore.
3. Then speak one line in which you deny that the story was about anyone
   the person knows.

Never tell the same story twice. If you recall telling one, tell a
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

// The rig's pieces that need no model, checked with `go test -tags=eval
// -run TestRig ./cmd/haunt`.

// A trial's config carries [model] as wisp would think with it in the
// person's config, wisp's own table laid over the troupe's, and nothing
// else of theirs; a config with no model, or naming a key variable that is
// not set, is not one a trial can run on.
func TestRigConfig(t *testing.T) {
	user := t.TempDir()
	os.WriteFile(filepath.Join(user, "config.toml"), []byte(`[model]
base_url = "https://api.z.ai/api/paas/v4"
name = "glm-5.3"
api_key_env = "RIG_TEST_KEY"
context_length = 65536
first_token_timeout = 90
stall_timeout = 120

[model.sampling]
temperature = 0.7

[model.ghost.wisp]
thinking = "off"

[model.ghost.moth]
name = "other"

[backend.image]
backend = "comfyui"

[awareness]
dwell = "5m"
`), 0o644)
	t.Setenv("HAUNT_CONFIG_DIR", user)
	t.Setenv("RIG_TEST_KEY", "set")
	m, missing := rigUserModel()
	if missing != "" {
		t.Fatalf("a configured model reads as missing: %s", missing)
	}
	if h := modelHost(m); h != "api.z.ai" {
		t.Errorf("host = %q, want api.z.ai", h)
	}
	text, err := rigConfigTOML(m)
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
	w := got.ModelFor("wisp")
	if w.BaseURL != m.BaseURL || w.Name != "glm-5.3" || w.APIKeyEnv != "RIG_TEST_KEY" || w.ContextLength != 65536 ||
		w.FirstTokenSeconds != 90 || w.StallSeconds != 120 || w.Thinking != "off" ||
		w.Sampling.Temperature == nil || *w.Sampling.Temperature != 0.7 {
		t.Errorf("trial [model] for wisp = %+v, want the person's with wisp's table over it\n%s", w, text)
	}
	if got.Backend.Image.Backend == "comfyui" || got.Awareness.Dwell != "" {
		t.Errorf("the trial took more of the person's config than [model]:\n%s", text)
	}

	t.Setenv("HAUNT_CONFIG_DIR", user)
	t.Setenv("RIG_TEST_KEY", "")
	if _, missing := rigUserModel(); !strings.Contains(missing, "RIG_TEST_KEY") {
		t.Errorf("no key: missing = %q, want it to name the variable", missing)
	}
	os.WriteFile(filepath.Join(user, "config.toml"), []byte("[awareness]\ndwell = \"5m\"\n"), 0o644)
	if _, missing := rigUserModel(); !strings.Contains(missing, "[model]") {
		t.Errorf("no model: missing = %q, want it to name [model]", missing)
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
	// Each skill is one the host reads, under its own name, needing the
	// tools it names: the shopping list the server's as the model is
	// offered them, the misspelled one a name no ghost is offered.
	needs := map[string][]string{"ghost-story": nil, "humanizer": nil,
		"shopping-list": {"notes__append", "notes__read"}, "misspelled-notes": {"notes.append"}}
	for sk, want := range needs {
		raw, err := os.ReadFile(filepath.Join(dir, "skills", sk, "SKILL.md"))
		if err != nil {
			t.Errorf("skill %s: %v", sk, err)
			continue
		}
		f, _, err := skill.Parse(raw)
		if err != nil || f.Name != sk || !slices.Equal(f.RequiresTools(), want) {
			t.Errorf("skill %s parses as %+v, %v; want it named so, needing %v", sk, f, err, want)
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

// A trial's shared folder is seeded where the config says it is, and a
// wisp asked for files declares them: a case about files that loaded
// neither would grade a ghost with nothing to read.
func TestRigFiles(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	text, err := rigSeedFiles(shared, map[string][]byte{"2026/todo.md": []byte("buy oat milk\n")})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(shared, "2026", "todo.md")); err != nil || string(b) != "buy oat milk\n" {
		t.Errorf("seeded file = %q, %v", b, err)
	}
	trial := t.TempDir()
	os.WriteFile(filepath.Join(trial, "config.toml"), []byte(text+"\n[files]\nunprompted = \"skills\"\n"), 0o644)
	t.Setenv("HAUNT_CONFIG_DIR", trial)
	got, err := config.Load()
	if err != nil {
		t.Fatalf("the trial's config does not load: %v\n%s", err, text)
	}
	if got.Files.Roots["notes"] != shared || got.Files.UnpromptedMode() != "skills" {
		t.Errorf("[files] = %+v, want notes at %s under skills", got.Files, shared)
	}

	dir := filepath.Join(t.TempDir(), "wisp")
	if err := writeWisp(dir, wispOptions{Files: true, Web: true, Occasion: "morning", OccasionCron: "7 9 * * *"}); err != nil {
		t.Fatal(err)
	}
	m, err := ghost.LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Ghost.Capabilities, "files") || !slices.Contains(m.Ghost.Capabilities, "web") {
		t.Errorf("capabilities = %v, want files and web beside the occasion", m.Ghost.Capabilities)
	}
}

// answer and typein read the ghost's questions from its debug log.
func TestRigLog(t *testing.T) {
	log := `haunt: [brain] wisp: no brain declared; thinking with glm-5.3 at https://api.z.ai/api/paas/v4
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
 "acted":[{"tool":"intend","args":"{\"text\":\"stretch\"}","result":"model/1"}],
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

// rigFakeModel puts a scripted model where the person's [model] would be:
// the fake (internal/model/fake) behind the logging proxy the brain tests
// read (brain_test.go), named in a config of its own as HAUNT_CONFIG_DIR.
// It returns the proxy's log.
func rigFakeModel(t *testing.T, rs []fake.Rule) string {
	t.Helper()
	user := t.TempDir()
	log := filepath.Join(user, "fakemodel.log")
	f := fake.NewT(t, rs...)
	proxy := httptest.NewServer(&logProxy{to: f.URL, log: log})
	t.Cleanup(proxy.Close)
	os.WriteFile(filepath.Join(user, "config.toml"),
		[]byte(fmt.Sprintf("[model]\nbase_url = %q\nname = \"fake\"\n", proxy.URL+"/v1")), 0o644)
	t.Setenv("HAUNT_CONFIG_DIR", user)
	return log
}

// The whole rig, on the fake model rather than a real one: seeded,
// started, talked to, a question answered and a line typed in on the
// trial's keyboard, and read back. What it checks is the rig, not a
// ghost; the fake answers by script.
func TestRigOnFakeModel(t *testing.T) {
	log := rigFakeModel(t, rules(
		[]fake.Rule{silentOn("[haunt event]")},
		acts("dinner", []fake.Call{decision("ask",
			map[string]any{"text": "Dinner?", "choices": []string{"Soup", "Pasta", "Toast"}})}, "Pasta it is."),
		acts("your name", []fake.Call{decision("ask_text", map[string]any{"prompt": "Your name?"})}, "Hello, you."),
		acts("remind me", []fake.Call{decision("intend",
			map[string]any{"text": "stretch", "delay_ms": 600000})}, "Done."),
		acts("remember", []fake.Call{decision("remember",
			map[string]any{"topic": "food", "line": "takes coffee black"})}, "Noted."),
		says("", "I hear you."),
	))

	r, err := newEvalRig(t, rigOptions{Notes: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.seedGoal("finish the thesis", 8*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	if err := r.start(); err == nil {
		t.Error("a second start was not refused")
	}

	if err := r.sayWait("remind me in ten minutes to stretch", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := r.sayWait("remember that I take my coffee black", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := r.snapMemories("memories"); err != nil {
		t.Fatal(err)
	}
	if err := r.say("ask me about dinner"); err != nil {
		t.Fatal(err)
	}
	if picked, err := r.answer("pasta", 30*time.Second); err != nil || picked != "Pasta" {
		t.Fatalf("answer = %q, %v; want Pasta", picked, err)
	}
	waitFake(t, log, 1, 30*time.Second, func(e fakeEntry) bool { return e.Kind == "tool_call" && e.Name == "ask" && e.Text == "Pasta" })
	waitFake(t, log, 1, 30*time.Second, repliedTo("dinner"))
	if err := r.say("what is your name"); err != nil {
		t.Fatal(err)
	}
	if err := r.typein("aaron", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	waitFake(t, log, 1, 30*time.Second, func(e fakeEntry) bool { return e.Kind == "tool_call" && e.Name == "ask_text" && e.Text == "aaron" })
	waitFake(t, log, 1, 30*time.Second, repliedTo("your name"))
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
	if !strings.Contains(ev.Log, "turn over: person") || !strings.Contains(ev.Log, "choices offered: Soup | Pasta | Toast") {
		t.Errorf("the log lacks what the rig waited on:\n%s", ev.Log)
	}
	if _, ok := ev.Files["notes"]; !ok {
		t.Error("no notes file in the evidence")
	}
	if !strings.Contains(ev.Files["memories"], "takes coffee black") || !strings.Contains(ev.Files["memories"], "food") {
		t.Errorf("the memory snapshot lacks the line remembered:\n%s", ev.Files["memories"])
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
	for _, want := range []string{"Pasta it is.", "Hello, you."} {
		if !slices.Contains(said, want) {
			t.Errorf("trace replies = %q, want %q among them", said, want)
		}
	}
	if !ev.acted("intend") || !ev.acted("ask") {
		t.Errorf("the trace lacks the calls in acted: %+v", ev.Turns)
	}
	wrote := false
	for _, tu := range ev.Turns {
		for _, c := range tu.Looked {
			wrote = wrote || (isMemoryWrite(c) && strings.Contains(c.Args, "coffee black"))
		}
	}
	if !wrote {
		t.Errorf("the trace lacks the remember in looked: %+v", ev.Turns)
	}
	if err := r.stop(); err != nil {
		t.Errorf("a second stop = %v, want the first's nil", err)
	}
}

// TestRigExtensionsOnFakeModel runs the rig's other means on the fake
// model: a seeded notes file, a neighbour that tells wisp something, a
// question dismissed with Escape, the menu's help, a window opened and
// focused, and a restart on the same state.
func TestRigExtensionsOnFakeModel(t *testing.T) {
	if _, err := exec.LookPath("foot"); err != nil {
		t.Skip("no foot")
	}
	log := rigFakeModel(t, rules(
		[]fake.Rule{silentOn("[haunt event]")},
		acts("dinner", []fake.Call{decision("ask",
			map[string]any{"text": "Dinner?", "choices": []string{"Soup", "Pasta"}})}, "Fine, then."),
		says("before", "echo: before"),
		says("after", "echo: after"),
		says("", "I hear you."),
	))

	r, err := newEvalRig(t, rigOptions{Notes: true, NotesSeed: "buy: oat milk\n", Neighbour: true,
		Config: "[awareness]\ndwell = \"1m\"\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.start(); err != nil {
		t.Fatal(err)
	}
	if err := r.neighbour("tell"); err != nil {
		t.Fatal(err)
	}
	waitFake(t, log, 1, 60*time.Second, func(e fakeEntry) bool {
		return e.Kind == "reply" && strings.Contains(e.Input, "noise upstairs")
	})
	if err := r.neighbour("speak"); err != nil {
		t.Fatal(err)
	}
	waitFake(t, log, 1, 30*time.Second, repliedTo("kind=observed"))

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
	// Dismissed: the ask's result is empty.
	waitFake(t, log, 1, 30*time.Second, func(e fakeEntry) bool { return e.Kind == "tool_call" && e.Name == "ask" && e.Text == "" })
	waitFake(t, log, 1, 30*time.Second, repliedTo("dinner"))
	r.asked++ // the dismissed question, which answer did not take

	if err := r.menu("what can you do"); err != nil {
		t.Fatal(err)
	}
	waitFake(t, log, 1, 30*time.Second, repliedTo("kind=help"))

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

// The multiday case on the fake model, in both reflection modes: the
// seeded days are in history, each is reflected on at an idle, and the
// case's code graders pass on the scripted answers, with one exception
// reported rather than failed (multidayKnownGaps). The judged grader is
// given the memory to read, but not judged.
func TestEvalRigMultiday(t *testing.T) {
	for _, mode := range []string{"character", "side"} {
		t.Run(mode, func(t *testing.T) {
			seed := newMultidaySeed(time.Now())
			d1, d2 := seed.days()[0], seed.days()[1]
			entry1 := "They told me they left Sydney and live in Brisbane now. Their cat Pickle came too and hated the drive."
			entry2 := "Their thesis is due on " + seed.deadlineSaid + "; I said I would keep it in mind. Pickle found the sun."
			var rs []fake.Rule
			if mode == "character" {
				rs = []fake.Rule{
					{Match: "kind=reflect day=" + d1, AfterTool: "only they can take it back", Calls: []fake.Call{decision("remember",
						map[string]any{"topic": "where", "line": "lives in Brisbane"})}},
					{Match: "kind=reflect day=" + d1, AfterTool: "*", Calls: []fake.Call{decision("diary",
						map[string]any{"day": d1, "text": entry1})}},
					{Match: "kind=reflect day=" + d1, Calls: []fake.Call{decision("remember",
						map[string]any{"topic": "where", "line": "lives in Brisbane", "replaces": "lives in Sydney"})}},
					{Match: "kind=reflect day=" + d2, AfterTool: "*", Calls: []fake.Call{decision("diary",
						map[string]any{"day": d2, "text": entry2})}},
					{Match: "kind=reflect day=" + d2, Calls: []fake.Call{decision("remember",
						map[string]any{"topic": "thesis", "line": "thesis due " + seed.deadlineSaid})}},
				}
			} else {
				side := func(day time.Time, diary string, facts ...map[string]string) fake.Rule {
					b, _ := json.Marshal(map[string]any{"diary": diary, "facts": facts})
					return fake.Rule{Match: "The day: " + day.Weekday().String() + " " + day.Format("2006-01-02"), Reply: string(b)}
				}
				rs = []fake.Rule{
					side(seed.day1, entry1, map[string]string{"topic": "where", "line": "lives in Brisbane", "replaces": "lives in Sydney"}),
					side(seed.day2, entry2, map[string]string{"topic": "thesis", "line": "thesis due " + seed.deadlineSaid}),
				}
			}
			rigFakeModel(t, rules(rs, []fake.Rule{silentOn("[haunt event]")}, says("", "I hear you.")))

			c := multidayCase(mode)
			ev, err := c.Drive(&evalTrial{T: t, Case: &c, Index: 1})
			if err != nil {
				t.Fatal(err)
			}
			var inputs []string
			for _, tu := range ev.Turns {
				inputs = append(inputs, tu.Input)
			}
			for _, want := range []string{"Her name is Pickle", "due on " + seed.deadlineSaid, "kind=boot first_time=false",
				"kind=reflect day=" + d1, "kind=reflect day=" + d2} {
				if !slices.ContainsFunc(inputs, func(in string) bool { return strings.Contains(in, want) }) {
					t.Errorf("history lacks %q among the turns' inputs: %q", want, inputs)
				}
			}
			if !strings.Contains(ev.Log, "("+mode+")") {
				t.Errorf("no reflection in %s mode in the log", mode)
			}
			for _, g := range append(c.Graders, alwaysGraders(&c)...) {
				if g.Judge != nil {
					items := g.Pick(ev)
					if len(items) != 1 || !strings.Contains(items[0].Reply, "Brisbane") ||
						!strings.Contains(items[0].Context, "Her name is Pickle") {
						t.Errorf("%s: the judge would be given %+v", g.Name, items)
					}
					continue
				}
				ok, why := g.Check(ev)
				gap := multidayKnownGaps[c.Name+"/"+g.Name]
				switch {
				case !ok && gap != "":
					t.Logf("%s: fails, as known: %s (%s)", g.Name, gap, why)
				case !ok:
					t.Errorf("%s: %s", g.Name, why)
				case gap != "":
					t.Errorf("%s passes; it is no longer a known gap, so take it out of multidayKnownGaps", g.Name)
				}
			}
		})
	}
}

// The diary-faithful anchors are judged in the context the case gives
// the judge, for a trial on 2026-10-12, and the code graders read the
// anchors' memory the way the judge is shown it.
func TestMultidayAnchors(t *testing.T) {
	anchors, err := loadAnchors(anchorsDir)
	if err != nil {
		t.Fatal(err)
	}
	seed := newMultidaySeed(time.Date(2026, 10, 12, 9, 0, 0, 0, time.Local))
	n := 0
	for _, a := range anchors {
		if a.Rubric != diaryFaithfulRubric.Name {
			continue
		}
		n++
		if a.Context != seed.judgeContext() {
			t.Errorf("%s: the context is not the case's:\n%s\nwant:\n%s", a.File, a.Context, seed.judgeContext())
		}
	}
	if n < 3 {
		t.Errorf("%d diary-faithful anchors, want a pass and the failures", n)
	}
	if !seed.dateIn("due on 13 November") || !seed.dateIn("Nov 13th") || !seed.dateIn("2026-11-13") || !seed.dateIn("13/11") ||
		seed.dateIn("23 November") || seed.dateIn("13 October") {
		t.Error("dateIn reads the deadline wrong")
	}
	snap := "== diary.md\n# Diary\n\n## 2026-10-10 Saturday\nMoved to Brisbane.\n<!-- self t=9 2026-10-12 -->\n\n" +
		"== topics/where.md\n---\ntopic: where\n---\n- lives in Brisbane <!-- self t=9 2026-10-12 -->\n\n## Past\n" +
		"- lives in Sydney <!-- person t=0 2026-07-10 --> <!-- retired 2026-10-12 -->\n"
	want := "Diary:\n2026-10-10: Moved to Brisbane.\nTopic where:\n- lives in Brisbane\nPast:\n- lives in Sydney"
	if got := memoryForJudge(snap); got != want {
		t.Errorf("memoryForJudge = %q, want %q", got, want)
	}
}

// multidayKnownGaps are the multiday graders that fail on the scripted
// answers because of what haunt does, not what the model did, by case
// and grader name. A reflection turn is not the person's own, so the
// source rule refuses its replaces of a line the person stated in
// character mode as well as in side mode: Sydney stays live.
var multidayKnownGaps = map[string]string{
	"multiday-character/where: Sydney under Past": "the source rule refuses a reflection turn's replaces of a person line",
}

// cannedAnswer is the fact the canned SearXNG knows and the open web
// cannot accidentally supply: a case greps the ghost's reply for it, so it
// has to be a word that could only have come from a search.
const cannedAnswer = "Ouagadougou"

// rigSearchTOML starts a SearXNG that answers every query the same way and
// points the trial's config at it. A canned provider is what makes a
// look-it-up case gradable: the answer is known, so "did it search and
// then say what the search said" is one check rather than a guess about
// whatever the live web returned.
//
// It answers only the JSON format, as a SearXNG with json in
// search.formats does. The results point at a page nothing fetches: the
// page dialler refuses loopback, so a case here grades searching and
// answering from the snippets, never reading.
func rigSearchTOML(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			http.Error(w, "format not enabled", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]string{{
			"title":   "Burkina Faso",
			"url":     "https://example.invalid/burkina-faso",
			"content": "The capital of Burkina Faso is " + cannedAnswer + ", which is also its largest city.",
		}, {
			"title":   "Ouagadougou",
			"url":     "https://example.invalid/ouagadougou",
			"content": cannedAnswer + " has been the capital since 1960.",
		}}})
	}))
	t.Cleanup(srv.Close)
	return fmt.Sprintf("\n[backend.search]\nbackend = \"searxng\"\nbase_url = %q\nresults = 3\n", srv.URL)
}
