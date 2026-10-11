// Package ghost runs one ghost: it loads the manifest, shell, and brain,
// connects renderer input to the event bus, delivers events to the brain,
// and executes the brain's intents against the screen and the store.
package ghost

import (
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/aaronjauregui/haunt/internal/backend"
	"github.com/aaronjauregui/haunt/internal/balloon"
	"github.com/aaronjauregui/haunt/internal/brain"
	"github.com/aaronjauregui/haunt/internal/bus"
	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/control"
	"github.com/aaronjauregui/haunt/internal/desktop"
	"github.com/aaronjauregui/haunt/internal/files"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/mcp"
	"github.com/aaronjauregui/haunt/internal/model"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/quiet"
	"github.com/aaronjauregui/haunt/internal/session"
	"github.com/aaronjauregui/haunt/internal/shell"
	"github.com/aaronjauregui/haunt/internal/sprite"
	"github.com/aaronjauregui/haunt/internal/store"
	"github.com/aaronjauregui/haunt/internal/wlclient"
)

// Store is the persistence contract a ghost run needs. The default is the
// SQLite implementation in internal/store; alternatives (in-memory for
// tests, encrypted, remote-synced) plug in via Options.Store.
type Store interface {
	// Set persists key=value, replacing any existing value.
	Set(key, value string) error
	// Get returns one value, and whether there was one. The other half of
	// Set, and separate from All because almost every read the driver
	// makes wants a single named key — the journal, the intentions, where
	// the ghost is standing.
	Get(key string) (value string, ok bool, err error)
	// Delete forgets one key, for the state the driver keeps on the
	// person's behalf and can be asked to drop again: a picture past the
	// gallery's limit, a house style reset to the package's.
	Delete(key string) error
	// All returns the complete KV map, handed to brains at boot. It is
	// genuinely a whole-store read and should be treated as one: the
	// driver does it once per session (see boot).
	All() (map[string]string, error)
	// Close flushes and releases the backend.
	Close() error
}

// Spots arbitrates opening positions when several ghosts share a screen.
// A ghost running alone has none and opens wherever it was left; the
// supervisor hands each resident ghost a view of the room bound to that
// ghost, so Release needs no argument.
//
// Only *opening* is arbitrated. Once the user has dragged two ghosts into
// a heap, that is where they want them, and a driver that pushed them
// apart again would be arguing with the person holding the mouse.
type Spots interface {
	// Claim reserves a rectangle, returning where it actually fits: the
	// requested spot if it is free, otherwise the nearest free one. speak
	// is the horizontal room the ghost needs beside it for its balloon —
	// standing two ghosts shoulder to shoulder leaves each of them talking
	// over the other, which is worse than overlapping, because a covered
	// balloon is a sentence you never see.
	Claim(output string, x, y, w, h, speak int) (int, int)
	// Moved records a new position for a ghost that has already opened.
	Moved(output string, x, y, w, h int)
	// Release gives the spot back when the ghost stops.
	Release()
}

// Stage is how a ghost sees the other ghosts in the room, and is how they
// see it. A ghost running alone has none: there is nobody to notice it and
// nobody to notice.
//
// What crosses it is an action and a string — what a bystander would have
// seen. That narrowness is the design and not an accident of the
// signature: a ghost may know that its neighbour spoke and what it said,
// and must have no way at all to reach its neighbour's store, prompts or
// journal. See internal/troupe/stage.go for the fan-out behind it.
type Stage interface {
	// Announce publishes one public action to every other resident ghost.
	Announce(action, detail string)
	// Observe starts delivery of what the others do. deliver runs on the
	// stage's goroutine, so it hands off rather than working.
	Observe(deliver func(from, action, detail string))
	// Leave stops delivery when the ghost stops.
	Leave()
}

// Options configures a ghost run.
type Options struct {
	// Store overrides the persistence backend. Nil uses SQLite in StateDir.
	Store Store
	// StateDir overrides where state.db lives; defaults to
	// $XDG_DATA_HOME/haunt/ghosts/<name>.
	StateDir string
	// RecordTo, if non-empty, records the event session to this file.
	RecordTo string
	// X, Y is the sprite's starting position. Ignored unless Placed is
	// set: without it the ghost returns to where it was left.
	X, Y int
	// Placed says X and Y were asked for explicitly, which overrides the
	// remembered position.
	Placed bool
	// Output pins the ghost to a named screen, overriding the remembered
	// one.
	Output string
	// Logger receives everything the driver has to say. Nil builds one
	// from the user's config and environment (see internal/hlog), which is
	// what the CLI wants; tests hand in hlog.Discard() or one that keeps
	// its lines.
	Logger *hlog.Logger
	// OnReady is called once the ghost is on screen and booted.
	OnReady func()
	// Granted reports whether the user granted a capability the manifest
	// declares. Nil means dev mode: everything declared is granted, with a
	// log line. Capabilities not in the manifest are never active.
	Granted func(cap string) bool
	// Consent records an answer to a permission asked for in the moment:
	// allowed writes the grant, refused records that the person does not
	// want to be asked again. Nil means the driver never asks and an
	// ungranted capability is simply refused, which is what a dev-directory
	// run and the tests want.
	Consent func(cap string, allowed bool) error
	// Refused reports a standing "never ask me about this again", so the
	// question is not raised a second time. Nil means nothing was refused.
	Refused func(cap string) bool
	// Spots, when set, keeps this ghost from opening on top of another
	// one. Nil means nothing else is on screen to avoid.
	Spots Spots
	// Budgets, when set, is the hourly ration of backend calls this
	// ghost spends against, shared with the other resident ghosts. Nil
	// gives the ghost one of its own, which is right when it is alone.
	Budgets *backend.Budgets
	// Stage, when set, is the other resident ghosts: what this one does in
	// public reaches them, and what they do reaches it as `observed`
	// events. Nil means it is alone in the room.
	Stage Stage
	// Initiative is a ceiling above this ghost's own curiosity allowance,
	// consulted only for an action that would otherwise be charged and
	// only once the ghost's own allowance has agreed to pay. It is what
	// stops N resident ghosts multiplying the user's bill by N. Nil means
	// there is no one else spending.
	Initiative func(what string) bool
	// IdleTimeoutMS overrides the inactivity threshold for idle events
	// (default 2 minutes).
	IdleTimeoutMS uint32
	// GoalCheckDelay overrides how long after the person goes idle the
	// ghost looks at whether a goal has gone quiet (default 5 minutes; see
	// goal.go), so tests and demos need not wait.
	GoalCheckDelay time.Duration
}

// Ghost is a running instance.
//
// # Locking
//
// There are a dozen small locks here rather than one big one, because the
// things they guard genuinely move independently: animation frames, a
// balloon being redrawn at a new scale, an AI request, the watch loop's
// session model, the daily allowances. A single lock would put a blink
// behind a language model.
//
// The rule that keeps that affordable is: **no lock here is ever held while
// another is acquired.** Every one of them guards a short critical section
// over its own fields — read the state, or write it, and get out — and
// anything slow (a backend call, a compositor round trip, a question on
// screen) happens outside them. There is therefore no lock order to get
// wrong, and adding one is how this stops being true.
//
// The one that looks like an exception is not. aiMu is not a data lock at
// all: it is the ghost's single work slot, held across a whole request so
// that two of them cannot wear one body, and the code under it takes the
// small locks freely.
type Ghost struct {
	manifest *Manifest
	opts     Options

	app     *wlclient.App
	surface *wlclient.SpriteSurface

	// shellMu guards the shell: pose changes come from the intent loop,
	// clip frames from animation timers.
	shellMu sync.Mutex
	shell   *shell.Shell
	// outputName is the screen the ghost is on, remembered along with its
	// position (see place.go).
	outputName string
	// painted is what is currently on screen, so a repaint can be limited
	// to what actually changed.
	painted shell.State

	// paged is the long line the user is reading through (see paging.go).
	pageMu sync.Mutex
	paged  *paged

	// recall is what has been said, for scrolling back through (see
	// recall.go).
	recallMu sync.Mutex
	recall   recallState

	anim          *animator
	reducedMotion bool
	blnTheme      balloon.Theme

	// blnMu guards the balloon surface and the renderer drawing into it.
	// The renderer is replaced whenever the ghost lands on a screen with a
	// different scale — on the dispatch goroutine — while dialogue, the
	// input line and page turns all render from goroutines of their own, so
	// it is read through renderer() rather than off the field.
	blnMu   sync.Mutex
	bln     *wlclient.SpriteSurface
	blnRend *balloon.Renderer
	// blnGen counts balloons shown, so a scheduled "hide this line" can
	// tell whether the line is still the one on screen. blnUp says whether
	// anything is showing at all — both are bookkeeping, kept even when
	// there is no compositor, because the linger logic acts on them.
	blnGen uint64
	blnUp  bool
	// blnWaiting says the balloon on screen is the driver's waiting
	// indicator (see waiting.go) rather than anything the ghost said, so it
	// can be replaced without taking away something the user is reading.
	blnWaiting bool
	// waiters are the indicators past their delay, oldest first. Only the
	// newest draws: a tool's own wait ("painting…") speaks over the turn
	// that called it, and the turn's comes back when the tool is done.
	waitMu  sync.Mutex
	waiters []*waiter
	// blnPicture says the balloon on screen is a picture the ghost drew, so
	// words streaming in after the draw do not paint over it (see
	// modelBrain.run).
	blnPicture bool

	bus   *bus.Bus
	brain brain.Runner
	// mind is the model brain when the ghost runs on one (brain_model.go),
	// and nil for a scripted brain. Its decisions reach the transcript,
	// memory and skills through it.
	mind *modelBrain
	// modelCfg is the model this ghost thinks with ([model] and its
	// [model.ghost.<name>] table).
	modelCfg model.Config
	// cur is the turn in flight on the model brain, nil between turns.
	cur     atomic.Pointer[turn]
	store   Store
	quiet   *quiet.Checker
	desktop *desktop.Watcher

	// stateDir is the ghost's own state directory: state.db, the
	// transcript, memory, learned skills, pictures.
	stateDir string

	// dir is the ghost's own directory, kept because tool servers may be
	// launched from it (see act.go).
	dir string

	// Tool servers and what they offer (see act.go). toolsUp guards the
	// start, which happens at boot or the moment the capability is granted.
	toolMu  sync.Mutex
	toolsUp bool
	servers []*mcp.Server
	toolCat map[string]toolEntry

	backend *backend.Service
	// expressionMode is [animation] expression_inference (expression.go).
	expressionMode string
	cacheDir       string
	// picturesDir holds the gallery's PNGs (gallery.go); picMu guards the
	// pictures this turn has shown, which become their caption, and the
	// counter that names a new one. A draw intent and a decision can
	// overlap, and two pictures given the same name would be one picture,
	// the second written over the first.
	picturesDir string
	picMu       sync.Mutex
	turnPics    []string
	aiMu        sync.Mutex // one AI request at a time per ghost

	// Typed input (see input.go).
	inputMu sync.Mutex
	input   inputState
	// The conversation the line belongs to, if any (see conversation.go).
	convMu sync.Mutex
	conv   conversation
	// drawMu is held while the input line's panel is drawn (drawInput).
	drawMu sync.Mutex

	// An open menu, if the brain asked a question (see choices.go).
	choiceMu sync.Mutex
	choice   choiceState

	// awareness is what the ghost may notice about the screen, and
	// watching guards its poll loop so exactly one runs however many times
	// it is started (see awareness.go).
	awareness config.AwarenessConfig
	watching  atomic.Bool
	// files are the folders the person shared in the config, opened for
	// reading, and filesCfg the [files] section they came from, for its
	// unprompted policy (files.go). Nil until the settings are applied.
	files    *files.Roots
	filesCfg config.FilesConfig
	// sess is the session model the watch loop is feeding, reachable from
	// here so teardown can write its state out while the store is still
	// open. The loop owns Observe; sessMu is what lets anyone else read it.
	sessMu sync.Mutex
	sess   *session.Model
	// seenWindow is the last window the watch loop looked at, kept so a
	// reply can record what the ghost could see when it gave it (why.go).
	// Under sessMu because it is written by the same loop.
	seenWindow WhyWindow

	// idle says the compositor has reported the person away from the
	// machine. Kept as a flag because the one thing that reads it — whether
	// there is anybody there to put a consent question to — is a scheduled
	// poll, which happens on nobody's timetable but its own (see poll.go).
	idle atomic.Bool
	// polls is what each manifest trigger last reported, so an answer that
	// has not changed is not delivered to the brain again (see poll.go).
	polls polls

	// Initiative accounting (see curiosity.go).
	curiosity *allowance
	// drifter is the state of the goal check, the other pass that happens
	// while nobody is there, and goalWatch is what the watch loop is
	// looking for on the person's screen — held in memory so a poll every
	// three seconds costs a walk over at most seven short word lists
	// rather than a scan of the store (see goal.go).
	drifter   drifter
	goalMu    sync.Mutex
	goalWatch []watcher
	// late says the goal check produced a finding that it deliberately did
	// not arm, because it ran while nobody was there. Read on every
	// EventActive, so it is a flag rather than a look at the store.
	late atomic.Bool
	// obligation is the third allowance: things a person put on the ghost's
	// books, kept apart from curiosity because they ration different
	// things. See markObliged in curiosity.go for the line between them.
	obligation      *allowance
	initMu          sync.Mutex
	promptedUntil   time.Time
	initiativeUntil time.Time
	obligedUntil    time.Time

	// asked tracks in-the-moment consent questions, so a refusal is
	// respected for a while and the same question is never on screen twice
	// (see consent.go).
	asked askedFor

	// autoGranted tracks which dev-mode grants have been logged, so the
	// notice appears once per capability rather than once per check.
	autoGranted sync.Map

	timersMu sync.Mutex
	timers   map[string]*time.Timer
	// intentTimers are the standing intentions waiting for their moment
	// (see intention.go). Under the same lock as the brain's timers and in
	// a map of their own, because a brain naming a timer "check" must not
	// silently cancel an intention of the same name.
	intentTimers map[string]*time.Timer
	// notes are the notes the ghost has left on the desktop, on screen
	// (see desknote.go).
	notes deskNotes

	// bg counts the goroutines go_ starts, so teardown can let them finish
	// before the store closes (see panic.go).
	bg background

	log *hlog.Logger
	// intents remembers the last one executed, for crash reports.
	intents intentLog
	recordF *os.File
	done    chan struct{}
	// stopping closes when the session begins tearing down, before the
	// Wayland connection goes away. Anything that paints or waits on a
	// model watches it: drawing into a closing compositor connection, or
	// sitting on a request whose answer nobody will see, is how an orderly
	// shutdown turns into a hang.
	stopping chan struct{}
	// runCtx is the session's context, so in-flight AI requests are
	// cancelled when the ghost is asked to stop rather than held until the
	// model gets round to answering.
	runCtx context.Context
}

// Run loads and starts the ghost in dir, blocking until ctx is cancelled or
// the session fails. The Wayland connection must be usable (WAYLAND_DISPLAY).
func Run(ctx context.Context, dir string, opts Options) error {
	if opts.Logger == nil {
		opts.Logger = hlog.Open(os.Stderr)
	}
	log := opts.Logger
	m, err := LoadManifest(dir)
	if err != nil {
		return err
	}
	for _, w := range m.Warnings() {
		log.Warn(hlog.Session, "%s: %s", filepath.Join(dir, "ghost.toml"), w)
	}
	// Before anything is opened: a config file nobody can parse should fail
	// on the terminal the person is standing at, not halfway through
	// putting a window on their screen.
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	g := &Ghost{
		manifest: m,
		dir:      dir,
		opts:     opts,
		log:      log,
		timers:   map[string]*time.Timer{},
		done:     make(chan struct{}),
		stopping: make(chan struct{}),
		runCtx:   ctx,
	}
	defer close(g.done)

	if err := g.openStore(); err != nil {
		return err
	}
	defer g.store.Close()

	if err := g.openScreen(cfg.ShellScale(m.Ghost.Name)); err != nil {
		return err
	}
	defer g.app.Close()
	if opts.Spots != nil {
		defer opts.Spots.Release()
	}

	// Bus, optionally recording.
	g.bus = bus.New(64)
	if opts.RecordTo != "" {
		if g.recordF, err = os.Create(opts.RecordTo); err != nil {
			return err
		}
		defer g.recordF.Close()
		g.bus.SetRecorder(g.recordF)
	}

	if err := g.applySettings(cfg); err != nil {
		return err
	}
	defer g.files.Close()

	if err := g.startBrain(ctx); err != nil {
		return err
	}

	g.wireInput()

	// Bus -> brain.
	deliveryDone := make(chan struct{})
	go func() {
		defer close(deliveryDone)
		defer g.recoverPanic(hlog.Brain, "event delivery")
		for e := range g.bus.Events() {
			if err := g.brain.Deliver(e); err != nil {
				log.Error(hlog.Brain, "brain delivery failed: %v", err)
				return
			}
		}
	}()

	// Control socket: external programs push events in (haunt say, hooks).
	// The socket is always open, and the "message" capability is checked per
	// event in publishFromControl, so a withdrawn grant takes effect at once.
	//
	// Through g.publishFromControl, not the bus directly: `haunt say` is a
	// person addressing the ghost, and the reply must not be charged to the
	// ghost's own initiative.
	ctl, err := control.Listen(m.Ghost.Name, g.publishFromControl, log)
	if err != nil {
		return err
	}
	defer ctl.Close()
	defer control.Forget(m.Ghost.Name)
	// A model brain's memory is reindexed on request: `haunt memory
	// reindex` asks the running ghost, the one writer of state.db.
	if g.mind != nil {
		ctl.Handle(control.ReqMemoryReindex, g.mind.reindexMemory)
	}

	stopTriggers, err := g.startTriggers(opts.IdleTimeoutMS)
	if err != nil {
		return err
	}
	defer stopTriggers()

	// Take a seat on the stage before booting, so a ghost that comes up
	// second sees whatever its neighbours do from its first moment rather
	// than from whenever the last of them finished starting.
	if opts.Stage != nil {
		opts.Stage.Observe(g.observed)
		defer opts.Stage.Leave()
	}

	// Tool servers, if the ghost declares any and may use them. Before the
	// boot event because the boot event carries what they offer, and
	// startable again later for the same reason window awareness is: the
	// person may say yes during the session (see execAct).
	g.startToolServers()
	defer g.stopToolServers()

	if err := g.boot(); err != nil {
		return err
	}
	defer g.stopAnimation()

	// Wayland event loop until cancelled.
	dispatchErr := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// With the stack and the breadcrumbs: a panic in here ends
				// the session, and "slice bounds out of range" on its own
				// is not enough to find anything.
				dispatchErr <- g.crashReport("the compositor dispatch loop", r, debug.Stack())
			}
		}()
		for {
			if err := g.app.Dispatch(); err != nil {
				// The library unregisters proxies on Destroy without
				// waiting for the server's delete confirmation, so events
				// already in flight for a destroyed surface (balloon
				// swaps) surface as "unable find sender". Benign: skip.
				if strings.Contains(err.Error(), "unable find sender") {
					continue
				}
				dispatchErr <- err
				return
			}
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case err := <-dispatchErr:
		runErr = fmt.Errorf("wayland session: %w", err)
	}
	return g.shutdown(deliveryDone, runErr)
}

// openStore opens the ghost's persistence: the injected backend, or SQLite
// in the state directory. It also settles where generated images are
// cached, which belongs here because the cache is state and lives beside
// the database that holds the rest of it.
func (g *Ghost) openStore() error {
	// The resolved directory, not opts.StateDir: with the option left empty
	// — which is every ordinary run — the two differ, and the cache used to
	// be sent to a temp directory while the database went to XDG. That
	// quietly threw away every generated image on reboot, and with it the
	// point of asking for the same picture twice costing one generation.
	stateDir := g.opts.StateDir
	if stateDir == "" {
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			base = filepath.Join(home, ".local", "share")
		}
		stateDir = filepath.Join(base, "haunt", "ghosts", g.manifest.Ghost.Name)
	}
	g.stateDir = stateDir
	g.cacheDir = filepath.Join(stateDir, "cache")
	g.picturesDir = filepath.Join(stateDir, "pictures")

	if g.opts.Store != nil {
		g.store = g.opts.Store
		return nil
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	var err error
	g.store, err = store.Open(filepath.Join(stateDir, "state.db"))
	return err
}

// openScreen loads the shell, connects to the compositor, decides where the
// ghost opens, and gets it there with a balloon renderer at the right
// scale. factor is the person's [appearance] scale for this ghost, passed
// in because the shell is loaded before the rest of the config is applied.
// On success the caller owns g.app and, if there is a room to be
// arbitrated, the claimed spot.
func (g *Ghost) openScreen(factor float64) error {
	opts, log := g.opts, g.log
	m := g.manifest

	// Shell: a layer stack with named poses, or a single image, or the
	// built-in placeholder — all three arrive here as the same thing.
	var err error
	if g.shell, err = loadShell(g.dir, m.Shell, factor); err != nil {
		return err
	}
	g.logShellScale(m.Shell, factor)
	sp := g.shell.Sprite()
	spriteW, spriteH := sp.Size()

	// Renderer.
	if g.app, err = wlclient.Connect(); err != nil {
		return err
	}
	// Pointer, keyboard and surface traffic, when someone asks for it by
	// name. It is a line per input event, which is unreadable as ordinary
	// output and indispensable when a click is landing somewhere it should
	// not.
	if log.Enabled(hlog.Wayland, hlog.LevelDebug) {
		g.app.EventLog = func(line string) { log.Debug(hlog.Wayland, "%s", line) }
	}
	// Where it sits: an explicit position, or wherever it was left.
	outName, startX, startY := g.startingPlace(g.app, opts.X, opts.Y, opts.Placed, spriteW, spriteH)
	if opts.Output != "" {
		outName = opts.Output
		if o, ok := g.app.OutputNamed(outName); ok && !opts.Placed {
			startX, startY = defaultSpot(o, spriteW, spriteH)
		}
	}
	g.outputName = outName
	// Not on top of somebody else. Two ghosts opening in the same default
	// corner is the ordinary case, not the unlucky one: the corner is
	// computed the same way for both.
	if opts.Spots != nil {
		w, h := spriteW, spriteH
		// The balloon's widest, which is fixed by the theme rather than by
		// this screen — the height cap below depends on the display, the
		// width does not.
		speak := balloon.DefaultTheme().MaxWidth + balloonGap
		claimedX, claimedY := opts.Spots.Claim(outName, startX, startY, w, h, speak)
		if claimedX != startX || claimedY != startY {
			log.Info(hlog.Place, "(%d,%d) was taken; opening at (%d,%d)",
				startX, startY, claimedX, claimedY)
		}
		startX, startY = claimedX, claimedY
	}
	if g.surface, err = g.app.CreateSpriteOpts(sp, int32(startX), int32(startY),
		wlclient.SurfaceOptions{Output: outName}); err != nil {
		return err
	}
	// Record where it ended up, not only where it is dragged to: a ghost
	// started with -x/-y or dropped in a default corner should be in the
	// same place after a restart, without the user having to nudge it
	// first.
	g.savePlace(startX, startY)
	if outName != "" {
		log.Info(hlog.Place, "placed on %s at (%d,%d)", outName, startX, startY)
	}
	if err = g.surface.WaitMapped(); err != nil {
		return err
	}
	// Balloons are capped to a share of the screen: three paragraphs of
	// generated dialogue would otherwise produce a panel taller than the
	// display, which covers everything and reads as the ghost having
	// broken.
	theme := balloon.DefaultTheme()
	if _, outH, known := g.app.OutputSize(); known {
		if capped := int(outH) * 2 / 3; capped > 120 {
			theme.MaxHeight = capped
		}
	}
	g.blnTheme = theme
	if g.blnRend, err = balloon.NewRendererScaled(theme, g.surface.Scale()); err != nil {
		return err
	}
	// No lock needed here and nowhere else: this is before the dispatch
	// goroutine exists.
	// A ghost dragged to a 200% screen needs its text redrawn at that
	// screen's resolution, not stretched from the old one.
	g.surface.OnScaleChanged = func(scale float64) {
		log.Debug(hlog.Wayland, "scale is now %g", scale)
		r, err := balloon.NewRendererScaled(g.blnTheme, scale)
		if err != nil {
			log.Warn(hlog.Balloon, "rescaling balloons: %v", err)
			return
		}
		g.blnMu.Lock()
		g.blnRend = r
		g.blnMu.Unlock()
	}
	return nil
}

// loadShell loads the ghost's shell drawn at factor times its own scale.
// Every shell goes through here, the placeholder included, so that the
// person's [appearance] setting reaches all of them the same way.
func loadShell(dir string, m shell.Manifest, factor float64) (*shell.Shell, error) {
	return shell.LoadWith(dir, m, shell.LoadOptions{ScaleFactor: factor})
}

// logShellScale says once what size the ghost is drawn at, and warns when
// the person's factor asked for more than the loader allows: the ghost
// still appears, but not at the size they wrote down, and the log is the
// only place that can say why.
func (g *Ghost) logShellScale(m shell.Manifest, factor float64) {
	own := m.Scale
	if own == 0 {
		own = 1
	}
	if factor == 0 {
		factor = 1
	}
	got, want := g.shell.Scale(), own*factor
	if math.Abs(got-want) > 1e-9 {
		g.log.Warn(hlog.Anim, "the shell's scale %g times the [appearance] factor %g is %g; drawing at %g",
			own, factor, want, got)
	}
	w, h := g.shell.Size()
	g.log.Debug(hlog.Anim, "shell drawn at scale %g (its own %g, [appearance] factor %g), %dx%d",
		got, own, factor, w, h)
}

// applySettings takes the parsed config file and everything derived from
// it: do-not-disturb, what the ghost may notice, its daily allowances, the
// AI service for pictures, sight and tools, and the model it thinks with.
func (g *Ghost) applySettings(cfg *config.File) error {
	var err error
	// Quiet hours (config windows + manual shush).
	if g.quiet, err = quiet.New(cfg.Quiet); err != nil {
		return err
	}
	// Fullscreen awareness: the ghost keeps out of the way of games and
	// films, which is both courtesy and — for image generation — leaving
	// the GPU alone.
	g.desktop = desktop.New(g.log)

	g.reducedMotion = cfg.Animation.ReducedMotion
	g.expressionMode = cfg.Animation.ExpressionMode()
	g.awareness = cfg.Awareness

	// Initiative allowance. Persisted, so restarting the ghost is not a way
	// to refill it.
	actionsPerDay, _ := cfg.Allowances()
	g.curiosity = newCuriosity(actionsPerDay, g.store, g.log)

	// And the second: what the person asked for, which must not compete with
	// what the ghost thought of.
	g.obligation = newObligation(cfg.ObligationsPerDay(), g.store, g.log)

	// AI services: images, vision and the tool budget (backends are
	// user-configured; absent config just means the capabilities error
	// out when used). Conversation is the model's, [model].
	g.backend = backend.NewServiceWith(cfg.Backend, g.log, g.opts.Budgets)
	g.modelCfg = cfg.ModelFor(g.manifest.Ghost.Name)
	g.applyConversation(cfg.Conversation)

	// The folders the person shared. One that cannot be opened (a drive
	// not mounted) is a warning, not a failure: it is listed as
	// unavailable and tried again when asked for. The warnings name host
	// paths, which belong in the person's log and nowhere the model reads.
	g.filesCfg = cfg.Files
	var warnings []error
	g.files, warnings = files.Open(cfg.Files.Roots)
	for _, w := range warnings {
		g.log.Warn(hlog.Session, "%v", w)
	}
	return nil
}

// startBrain starts the ghost's logic: the model brain when the manifest
// declares no brain (brain_model.go), sandboxed WASM when it does, a
// native process only when the manifest declares (and the user granted)
// the native-brain capability. The scripted two speak the event/intent
// schema down a pipe; the model brain is hosted in the driver and acts
// through decisions.
func (g *Ghost) startBrain(ctx context.Context) error {
	m := g.manifest
	var err error
	switch {
	case m.Brain.Builtin():
		g.log.Info(hlog.Brain, "no brain declared; thinking with %s at %s", g.modelCfg.Name, g.modelCfg.BaseURL)
		stateDir := g.stateDir
		if g.opts.Store != nil {
			// A store handed in is a test's or a host's: the ghost keeps
			// nothing of its own on disk beside it.
			stateDir = ""
		}
		g.mind, err = newModelBrain(g, brainParts{client: model.New(g.modelCfg), cfg: g.modelCfg, stateDir: stateDir})
		if err == nil {
			g.brain = g.mind
		}
	case len(m.Brain.Command) > 0:
		if !g.capabilityActive("native-brain") {
			return fmt.Errorf("ghost %q wants a native brain but the native-brain capability is not granted", m.Ghost.Name)
		}
		g.brain, err = brain.StartProcess(g.dir, m.Brain.Command, g.execIntent, brain.Options{})
	default:
		var wasmBytes []byte
		if wasmBytes, err = os.ReadFile(filepath.Join(g.dir, m.Brain.Wasm)); err != nil {
			return err
		}
		g.brain, err = brain.Start(ctx, wasmBytes, g.execIntent, brain.Options{})
	}
	return err
}

// wireInput connects the renderer's pointer and keyboard callbacks to the
// event bus. Everything here runs on the dispatch goroutine.
func (g *Ghost) wireInput() {
	g.surface.OnClick = g.onClick
	// Right-click opens a line to type at the ghost. A separate gesture
	// from the left click, which belongs to the brain: a scripted ghost's
	// quip on being poked must not be traded away for an input box.
	g.surface.OnRightClick = g.openInput
	g.surface.OnKey = g.onKey
	g.surface.KeyRepeats = g.keyRepeats
	// The wheel over the ghost goes back through what was said, and
	// moving off it puts the past away again (see recall.go).
	g.surface.OnScroll = g.onScroll
	g.surface.OnHover = g.onHover
	// The compositor giving the keyboard away means the user's attention
	// has moved on, so the half-typed line goes with it — but only if the
	// line ever had focus in the first place (see onKeyboardFocus).
	g.surface.OnKeyboardFocus = g.onKeyboardFocus
	g.surface.OnDragEnd = func(x, y int) {
		g.publish(proto.Event{Type: proto.EventDragEnd, X: x, Y: y, Region: g.shell.RegionAt(x, y)})
		// Dragging is how a person says where they want it, so that is
		// what gets remembered.
		sx, sy, sw, sh := g.surface.Geometry()
		g.savePlace(int(sx), int(sy))
		if g.opts.Spots != nil {
			g.opts.Spots.Moved(g.outputName, int(sx), int(sy), int(sw), int(sh))
		}
	}
	// The balloon belongs to the ghost: it follows the sprite while dragged.
	g.surface.OnMove = func(x, y int) {
		g.placeBalloon()
	}
}

// onClick is a left click on the ghost. Runs on the dispatch goroutine.
func (g *Ghost) onClick(x, y int) {
	// In a conversation a poke is only a poke: the line, the view and
	// what is being typed stay as they are, and there are no pages to
	// turn (conversation.go).
	if g.clickInConversation() {
		g.publish(proto.Event{Type: proto.EventClick, X: x, Y: y, Region: g.shell.RegionAt(x, y)})
		g.announce(proto.ActedPoked, "")
		return
	}
	// Poking the ghost puts away whatever was half-typed, the way
	// clicking off an input box does anywhere else — and does it first,
	// so the brain's reaction is not suppressed as "user is typing".
	g.closeInput()
	// Turning away from a question is an answer of its own, and the
	// brain has to hear it or it waits forever.
	if g.choiceOpen() {
		g.dismissChoices()
		return
	}
	// A line scrolled back to is put away by a click, like a page
	// that has been read.
	if g.endRecall() {
		return
	}
	// A balloon with more to read has first claim on the click.
	if g.turnPage() {
		return
	}
	g.publish(proto.Event{Type: proto.EventClick, X: x, Y: y, Region: g.shell.RegionAt(x, y)})
	// Somebody chose this ghost out of the several on screen, which is
	// the most interesting thing the others can see happening.
	g.announce(proto.ActedPoked, "")
}

// startTriggers arms the two clocks that speak to the ghost without anybody
// touching it: the compositor's idle notifications, and the manifest's cron
// entries. The returned function stops the schedule.
func (g *Ghost) startTriggers(idleTimeoutMS uint32) (func(), error) {
	// Idle awareness: after inactivity brains hear idle, and active when
	// the user returns. Compositor support is optional.
	if idleTimeoutMS == 0 {
		idleTimeoutMS = 2 * 60 * 1000
	}
	supported, err := g.app.WatchIdle(idleTimeoutMS,
		func() { g.publish(proto.Event{Type: proto.EventIdle}) },
		func() { g.publish(proto.Event{Type: proto.EventActive}) },
	)
	if err != nil {
		return nil, err
	}
	if !supported {
		g.log.Info(hlog.Session, "compositor lacks ext-idle-notify; idle events disabled")
	}

	// Scheduled triggers from the manifest.
	if len(g.manifest.Triggers) == 0 {
		return func() {}, nil
	}
	sched := cron.New()
	for _, tr := range g.manifest.Triggers {
		name := tr.Name
		if tr.Tool != "" {
			// A poll rather than an occasion: the driver calls somebody
			// else's server and the brain hears what it said (see poll.go).
			// On a goroutine of its own, because a tool call takes as long
			// as the far end takes and the triggers behind it must not
			// queue up waiting for it — or, worse, wait behind a consent
			// question somebody is reading.
			if _, err := sched.AddFunc(tr.Cron, func() {
				g.go_(hlog.Tool, "poll "+name, func() { g.pollTrigger(tr) })
			}); err != nil {
				return nil, fmt.Errorf("trigger %q: %w", name, err)
			}
			continue
		}
		if _, err := sched.AddFunc(tr.Cron, func() {
			// An obligation: the author declared this occasion and the
			// person accepted it when they installed the ghost, so a
			// morning greeting is not the ghost deciding to speak. A brain
			// cannot add one, which is what keeps that honest.
			g.markObliged()
			g.publish(proto.Event{Type: proto.EventCron, Name: name})
		}); err != nil {
			return nil, fmt.Errorf("trigger %q: %w", name, err)
		}
	}
	sched.Start()
	return func() { sched.Stop() }, nil
}

// boot tells the brain the session has started and starts everything that
// should only run once it has: standing intentions, window awareness and
// idle animation. The caller owns stopping the animation.
func (g *Ghost) boot() error {
	// Boot with the persisted KV snapshot, with the faces this shell can
	// wear, and with the tools it may act through: a brain choosing an
	// expression or an action is otherwise guessing names at a driver that
	// silently ignores the ones that do not exist.
	kv, err := g.store.All()
	if err != nil {
		return err
	}
	var poses []string
	if g.shell != nil {
		poses = g.shell.Poses()
	}
	g.publish(proto.Event{
		Type:  proto.EventBoot,
		KV:    visibleKV(kv),
		Poses: poses,
		Tools: g.toolNames(),
	})
	if g.opts.OnReady != nil {
		g.opts.OnReady()
	}

	// Whatever it was still meaning to do when it last stopped. After the
	// boot event, so the greeting is the first thing said and a reminder
	// the ghost has been holding since yesterday does not land on top of it.
	g.armIntentions()
	// And the notes it had left on the desktop, back where they were.
	g.showStoredNotes()

	// Watching the focused window, if the ghost asked for that and the user
	// agreed to it. Startable again later, because the user may say yes
	// during the session (see execWatch).
	g.startWatching()

	// Idle animation: blinking and the like, on the shell's own schedule.
	g.startAnimation()
	return nil
}

// shutdown is the orderly teardown: stop painting and waiting first, then
// tell the brain, stop timers, let the background work finish, drain
// deliveries, and close the brain last so the shutdown event is not lost to
// a closed pipe. The order matters: a balloon being animated into a
// connection that is about to close, or an AI request nobody is left to
// hear, will otherwise hold the whole teardown open.
func (g *Ghost) shutdown(deliveryDone <-chan struct{}, runErr error) error {
	close(g.stopping)
	// The notes' paper goes with the ghost; the notes stay in the store.
	g.destroyNotes()
	// What the day looked like, while there is still a store to put it in:
	// the watch loop's own exit runs after the store has been closed, so
	// this is the only place the last minute of it can be saved.
	g.saveSessionState()
	// The goal check is not finishing now either.
	g.endGoalCheck()
	g.publish(proto.Event{Type: proto.EventShutdown})
	g.timersMu.Lock()
	for _, t := range g.timers {
		t.Stop()
	}
	for _, t := range g.intentTimers {
		t.Stop()
	}
	g.timersMu.Unlock()
	// The background work, before the store it writes into goes away. See
	// waitForBackground: the ghost has usually just been told to stop by
	// somebody who wants their terminal back, so this is bounded.
	g.waitForBackground()
	g.bus.Close()
	<-deliveryDone
	if err := g.brain.Close(); err != nil && runErr == nil {
		runErr = fmt.Errorf("brain: %w", err)
	}
	return runErr
}

// capabilityActive reports whether a capability is both declared by the
// manifest and granted by the user.
func (g *Ghost) capabilityActive(cap string) bool {
	if g.manifest == nil {
		return false
	}
	if !slices.Contains(g.manifest.Ghost.Capabilities, cap) {
		return false
	}
	if g.opts.Granted == nil {
		// Once per capability: this is checked on every use, and memory
		// work checks it several times per exchange.
		if _, seen := g.autoGranted.LoadOrStore(cap, true); !seen {
			g.log.Info(hlog.Session, "dev mode, auto-granting capability %q", cap)
		}
		return true
	}
	// A capability allowed in the balloon a moment ago counts, until the
	// grants file it was written to has been re-read (see consent.go).
	return g.opts.Granted(cap) || g.asked.justAllowed(cap)
}

func (g *Ghost) publish(e proto.Event) {
	if err := g.publishExternal(e); err != nil {
		g.log.Warn(hlog.Session, "publish %s: %v", e.Type, err)
	}
}

// publishExternal is publish for callers that want the error — notably the
// control socket, which reports failures back to `haunt say`. Every event
// enters the bus through here, so causality is recorded in one place.
func (g *Ghost) publishExternal(e proto.Event) error {
	// Causality for the curiosity budget: the user acting opens the window
	// in which whatever the ghost does next is a response rather than an
	// interruption. The bit stays driver-side — brains never see it and so
	// cannot claim to have been provoked.
	//
	// The obligation window is the same idea and is opened at its two
	// sources instead of here (fireIntention, and the cron closure in
	// startTriggers), because whether a due intention is an obligation is a
	// fact recorded on the intention rather than carried on the event —
	// putting it on the event would put it somewhere a brain could read.
	switch {
	case e.Type == proto.EventBoot:
		g.markPromptedFor(bootWindow)
	case promptedEvent(e):
		g.markPrompted()
		// The same causality answers a second question: which ghost
		// `haunt say` means when it is not told. Boot is left out — being
		// started is not being spoken to, and a login that starts three
		// ghosts must not make the last one to boot the one you address.
		if g.manifest != nil {
			control.Touch(g.manifest.Ghost.Name)
		}
	case e.Type == proto.EventIdle:
		g.idle.Store(true)
		g.endResponding()
		// Nobody is waiting, so this is when the ghost looks at whether a
		// goal has gone quiet (goal.go): work that must not happen over
		// somebody's shoulder, and that does not speak.
		g.beginGoalCheck()
	case e.Type == proto.EventActive:
		g.idle.Store(false)
		g.endGoalCheck()
		// And there is somebody to raise things with again. A drift finding
		// is deliberately left unarmed by the pass that made it, because
		// the pass runs precisely when nobody is there to hear it.
		g.armLateIntentions()
	}
	return g.bus.Publish(e)
}

// publishFromControl is the control socket's way in. The socket is opened
// once, at startup, but the permission it stands for can be withdrawn while
// the ghost is running — so the capability is checked per event, and
// `haunt say` is told why it was refused rather than left wondering.
//
// The asymmetry is deliberate and worth knowing: revoking takes effect at
// once. Everything else — llm, imagine, window, screen, tell — is checked
// at the moment it is used and so changes in both directions immediately.
func (g *Ghost) publishFromControl(e proto.Event) error {
	if !g.capabilityActive("message") {
		return fmt.Errorf("the message capability is not granted")
	}
	return g.publishExternal(e)
}

// aiWork describes one of the three intents that reach a backend: imagine,
// look and act. They share a shape — decide whether this was provoked, get
// off the intent loop, be sure of the capability, take the ghost's one AI
// slot, do the work — and the places where they genuinely differ are the
// fields.
type aiWork struct {
	// name is the intent, for the allowance and the log.
	name string
	// capability must be granted before the work starts.
	capability string
	topic      hlog.Topic
	// charged says an unprompted one spends from the daily allowance. Every
	// intent that puts something in front of the person sets it; see the
	// note at IntentLook for the one that does not.
	charged bool
	// refusal, when set, is said back to the brain as an error when the
	// allowance is spent. Silence is right for a remark nobody is waiting
	// on; a tool call somebody is waiting on needs an answer.
	refusal string
	// beforeLock runs on the work's own goroutine before the AI lock is
	// taken, for setup that may itself be slow.
	beforeLock func()
	// run is the work. userDriven is whether a person was waiting when the
	// intent arrived, which only imagine cares about.
	run func(in proto.Intent, userDriven bool)
}

// startAI gates one backend-bound intent and starts it.
//
// The two gates are deliberately on opposite sides of the goroutine.
// Whether this was provoked is a fact about the moment the intent arrived,
// so the allowance is spent here, on the intent loop, before anything can
// take time: a slow backend must not be able to turn a reply into an
// interruption on arrival. The capability question is asked on the other
// side, because it may put a question on screen and wait for a person to
// read it — and holding the ghost's one AI slot while they think would
// leave it deaf to everything else meanwhile.
func (g *Ghost) startAI(in proto.Intent, w aiWork) {
	if w.charged && !g.allowInitiative(w.name) {
		if w.refusal != "" {
			g.publishAIError(w.refusal, in.Tag)
		}
		return
	}
	// Whether a person is waiting travels with the request, so a slow
	// backend cannot turn a reply into an interruption on arrival.
	userDriven := g.respondingToUser()
	// Own goroutine: a slow backend must not block other intents. aiMu
	// keeps one request in flight per ghost.
	g.go_(w.topic, w.name, func() {
		if !g.ensureCapability(w.capability) {
			g.publishAIError(w.capability+" capability not granted", in.Tag)
			return
		}
		if w.beforeLock != nil {
			w.beforeLock()
		}
		g.aiMu.Lock()
		defer g.aiMu.Unlock()
		w.run(in, userDriven)
	})
}

// execIntent runs on the brain's intent-reader goroutine. Wayland calls are
// safe here: go-wayland requests are locked writes to the socket, and the
// dispatch loop only reads events.
func (g *Ghost) execIntent(in proto.Intent) {
	// Remembered before it runs, so a panic reports the intent that caused
	// it rather than the one before.
	g.noteIntent(in)
	defer g.recoverPanic(hlog.Brain, "intent "+string(in.Type))
	switch in.Type {
	case proto.IntentSay:
		// Speaking up unasked is the interruption the budget exists to
		// ration, whether the words came from a model or a table.
		if !g.allowInitiative("say") {
			return
		}
		if len(in.Choices) > 0 {
			// A question, not a remark: it stays up until it is answered,
			// and the answer comes back as an event.
			g.inferExpression(in.Text)
			g.showChoices(in)
			return
		}
		// Anything else the ghost says replaces the question, so the
		// question has to be closed properly rather than just painted
		// over: a menu whose balloon is gone but whose state is open would
		// hold the keyboard and wait for an answer nobody can give.
		g.dismissChoices()
		// Scripted dialogue gets the free tier of expression inference. A
		// brain that also sets an expression runs after this and wins.
		g.inferExpression(in.Text)
		// Scripted dialogue arrives whole, so it is readable from its
		// beginning immediately, pages and all.
		g.showPaged(in.Text)
		// And the mouth moves for about as long as the line would take to
		// say, since there is no stream to follow here.
		g.talkFor(in.Text)
		if in.LingerMS > 0 {
			g.lingerBalloon(g.balloonGen(), time.Duration(in.LingerMS)*time.Millisecond)
		}
	case proto.IntentHideSay:
		g.stopTalking()
		// A conversation's balloon is up for as long as the conversation
		// is: a brain's timer does not take it down (conversation.go).
		if g.inConversation() {
			g.log.Debug(hlog.Balloon, "hide_say ignored: a conversation is open")
			return
		}
		g.clearPaging()
		// Including a question: a brain putting the balloon away has
		// withdrawn whatever was in it. (This is how a stale `hush` timer
		// used to make a menu invisible while it was still listening.)
		g.dismissChoices()
		g.hideBalloon()
	case proto.IntentMove:
		if err := g.surface.MoveTo(int32(in.X), int32(in.Y)); err != nil {
			g.log.Warn(hlog.Wayland, "move failed: %v", err)
			return
		}
		g.placeBalloon()
		// Only the ghost moving itself. Being dragged is the *person*
		// moving it, and an observer told "it moved" for that would be
		// crediting the ghost with something the hand on the mouse did.
		g.announce(proto.ActedMoved, "")
	case proto.IntentSetTimer:
		g.setTimer(in.Name, time.Duration(in.DelayMS)*time.Millisecond)
	case proto.IntentCancelTimer:
		g.timersMu.Lock()
		if t, ok := g.timers[in.Name]; ok {
			t.Stop()
			delete(g.timers, in.Name)
		}
		g.timersMu.Unlock()
	case proto.IntentKVSet:
		if strings.HasPrefix(in.Name, internalKeyPrefix) {
			g.log.Debug(hlog.Store, "kv_set %s dropped: reserved prefix", in.Name)
			return
		}
		if err := g.store.Set(in.Name, in.Value); err != nil {
			g.log.Warn(hlog.Store, "kv_set %s: %v", in.Name, err)
		}
	case proto.IntentLog:
		g.log.Line("ghost: " + in.Text)
	case proto.IntentImagine:
		g.startAI(in, aiWork{
			name: "imagine", capability: "imagine", topic: hlog.Backend, charged: true,
			run: g.execImagine,
		})
	case proto.IntentSetExpression:
		g.setExpression(in.Name)
	case proto.IntentPlayClip:
		// Announced, unlike the clips the driver starts by itself. A
		// blink or a breath is furniture — every ghost on screen is doing
		// it every few seconds, and telling the neighbours about each one
		// would bury what somebody actually *did* under the noise of
		// everybody being alive. A clip a brain asked for is a deliberate
		// flourish, which is a thing to see.
		if g.playClip(in.Name) {
			g.announce(proto.ActedClip, in.Name)
		}
	case proto.IntentLook:
		// Not charged against the daily allowance, unlike its two
		// neighbours here, and the omission is deliberate rather than
		// missed. Looking is rationed by the hourly vision budget it spends
		// from (backend/vision.go) and hedged six further ways in look.go —
		// declared capability, granted capability, a configured vision
		// model, the ignore list, nothing fullscreen, and the ghost
		// visibly showing that it is looking. What the daily allowance
		// rations is interruption, and a look interrupts nobody: its
		// answer goes to the brain, never to the balloon.
		g.startAI(in, aiWork{
			name: "look", capability: "screen", topic: hlog.Screen,
			run: func(in proto.Intent, _ bool) { g.execLook(in) },
		})
	case proto.IntentAct:
		// Acting unbidden is the most interruptive thing a ghost does and
		// is charged like it — which is also the answer to the obvious
		// worry about an agent living on your desktop, since deciding to
		// do anything at all unprompted comes out of a daily allowance a
		// restart cannot refill. Said out loud when refused, because a
		// tool call the model is waiting on needs an answer.
		//
		// The AI lock is taken because acting and talking share the
		// balloon and the face, and two of them at once would be two
		// ghosts wearing one body.
		g.startAI(in, aiWork{
			name: "act", capability: "tools", topic: hlog.Tool, charged: true,
			refusal: "not now: the ghost's allowance for acting unprompted is spent",
			// Granted possibly a moment ago, in which case the servers have
			// never been started. Cheap and idempotent when they have.
			beforeLock: g.startToolServers,
			run:        func(in proto.Intent, _ bool) { g.execAct(in) },
		})
	case proto.IntentWatch:
		// Its own goroutine, like imagine: ensureCapability blocks while the
		// question is on screen, and the intent loop is not a thing to
		// stall while somebody reads.
		g.go_(hlog.Screen, "watch", func() { g.execWatch() })
	case proto.IntentIntend:
		g.execIntend(in)
	case proto.IntentResolve:
		g.execResolve(in.Name)
	case proto.IntentSetAside:
		g.execSetAside(in.Name)
	case proto.IntentLeaveNote:
		if _, err := g.execLeaveNote(in); err != nil {
			g.log.Debug(hlog.Brain, "leave_note intent dropped: %v", err)
		}
	case proto.IntentNote:
		g.execNote(in.Text)
	case proto.IntentTell:
		if in.Name == g.manifest.Ghost.Name {
			g.log.Debug(hlog.Control, "tell intent dropped: ghost addressed itself")
			return
		}
		// Granted already: sent from here, so two tells in a row arrive in
		// the order the brain sent them. Only the question goes elsewhere.
		if g.capabilityActive("tell") {
			g.sendTell(in.Name, in.Text)
			return
		}
		g.go_(hlog.Control, "tell", func() {
			if !g.ensureCapability("tell") {
				g.log.Debug(hlog.Control, "tell intent dropped: capability not granted")
				return
			}
			g.sendTell(in.Name, in.Text)
		})
	}
}

// sendTell delivers one ghost's message to another over the second ghost's
// control socket. The sender's name rides along, which is what keeps the
// causality rules honest: a message with From set is another ghost talking,
// not a person, and does not open a response window.
func (g *Ghost) sendTell(to, text string) {
	err := control.Send(to, proto.Event{
		Type: proto.EventMessage,
		Text: text,
		From: g.manifest.Ghost.Name,
	})
	if err != nil {
		g.log.Warn(hlog.Control, "tell %s: %v", to, err)
	}
}

func (g *Ghost) setTimer(name string, d time.Duration) {
	g.timersMu.Lock()
	defer g.timersMu.Unlock()
	if t, ok := g.timers[name]; ok {
		t.Stop()
	}
	g.timers[name] = time.AfterFunc(d, func() {
		defer g.recoverPanic(hlog.Brain, "timer "+name)
		g.timersMu.Lock()
		delete(g.timers, name)
		g.timersMu.Unlock()
		select {
		case <-g.done:
		default:
			g.publish(proto.Event{Type: proto.EventTimer, Name: name})
		}
	})
}

// renderer returns the balloon renderer for the scale the ghost is
// currently on, and the wider conversation one while a conversation is
// open (conversation.go). Read under the lock: the dispatch goroutine
// swaps it when the compositor announces a different scale, and every
// other goroutine that draws would otherwise be reading a field being
// written.
func (g *Ghost) renderer() *balloon.Renderer {
	g.blnMu.Lock()
	base := g.blnRend
	g.blnMu.Unlock()
	return g.conversationRenderer(base)
}

// showBalloon renders text and places it beside the sprite. During quiet
// hours it drops the balloon; the driver enforces this, not the ghost.
func (g *Ghost) showBalloon(text string) {
	if muted, why := g.quiet.Active(); muted {
		g.log.Debug(hlog.Balloon, "balloon suppressed (%s)", why)
		return
	}
	// A reply arriving over a question closes the question: the balloon is
	// one surface, and a menu whose panel has been painted over would still
	// be holding the keyboard, waiting.
	g.dismissChoices()
	// Never paint over a line the user is in the middle of typing. In a
	// conversation the words go above it instead.
	if g.inputOpen() {
		if g.streamInConversation(text) {
			return
		}
		g.log.Debug(hlog.Balloon, "balloon suppressed (user is typing)")
		return
	}
	g.showBalloonSprite(g.renderer().Render(text))
}

// showBalloonSprite puts a rendered panel on screen beside the ghost. It
// asks no questions about quiet hours or input state — callers decide that,
// because the input line itself must appear regardless of both.
func (g *Ghost) showBalloonSprite(sp *sprite.Sprite) {
	g.hideBalloon()
	// Counted before the surface exists: this is "which line is current",
	// which is a fact about the conversation rather than about Wayland.
	g.blnMu.Lock()
	g.blnGen++
	g.blnUp = true
	g.blnMu.Unlock()
	if g.surface == nil || g.app == nil {
		return // no compositor: tests drive balloons without a screen
	}
	w, h := sp.Size()
	lw, lh := g.renderer().LogicalSize(w, h)
	// Measured before it is placed: which side of the ghost the panel goes
	// on depends on how wide it is.
	x, y := g.balloonPos(int32(lw))
	bln, err := g.app.CreateSpriteOpts(sp, x, y, wlclient.SurfaceOptions{
		PassThrough: true,
		LogicalW:    int32(lw),
		LogicalH:    int32(lh),
	})
	if err != nil {
		g.log.Warn(hlog.Balloon, "balloon failed: %v", err)
		return
	}
	g.blnMu.Lock()
	g.bln = bln
	g.blnWaiting = false
	g.blnPicture = false
	g.blnMu.Unlock()
}

// blnShowing reports whether a balloon is up. Without a compositor there is
// no surface to create, so this is bookkeeping only — which is exactly what
// the linger logic acts on.
func (g *Ghost) blnShowing() bool {
	g.blnMu.Lock()
	defer g.blnMu.Unlock()
	return g.blnUp
}

// balloonGen is which balloon is on screen, for callers that will want to
// know later whether it is still the same one.
func (g *Ghost) balloonGen() uint64 {
	g.blnMu.Lock()
	defer g.blnMu.Unlock()
	return g.blnGen
}

// lingerBalloon takes the current balloon down after d — but only if it is
// still the current balloon. A brain that scheduled its own hide would
// otherwise erase whatever arrived in the meantime, which on a slow model
// is exactly the reply the person was waiting for.
//
// Nothing lingers in a conversation: the balloon is up for as long as it
// is, and its words linger from when it ends (endConversation).
func (g *Ghost) lingerBalloon(gen uint64, d time.Duration) {
	if g.inConversation() {
		g.log.Debug(hlog.Balloon, "no linger: a conversation is open")
		return
	}
	time.AfterFunc(d, func() {
		g.guard(hlog.Balloon, "the balloon's linger", func() {
			select {
			case <-g.done:
				return
			default:
			}
			if g.inConversation() {
				g.log.Debug(hlog.Balloon, "linger expired during a conversation; the balloon stays")
				return
			}
			if g.balloonGen() != gen {
				g.log.Debug(hlog.Balloon, "linger expired on a line that had already been replaced")
				return
			}
			g.log.Debug(hlog.Balloon, "line lingered its time; putting it away")
			g.hideBalloon()
		})
	})
}

// showImageBalloon renders an image into a balloon panel beside the sprite.
// The bool is whether the picture actually reached the screen, which the
// caller needs in order to answer the only question the other ghosts in the
// room can ask about it: was there anything to see (sociality.go).
func (g *Ghost) showImageBalloon(img image.Image) bool {
	// A picture replaces a question, and replacing it means closing it.
	g.dismissChoices()
	if muted, why := g.quiet.Active(); muted {
		g.log.Debug(hlog.Balloon, "image balloon suppressed (%s)", why)
		return false
	}
	// In a conversation a picture goes in the view, as a thumbnail, once
	// it is remembered; there is nothing to paint here.
	if g.clickInConversation() {
		return true
	}
	if g.inputOpen() {
		g.log.Debug(hlog.Balloon, "image balloon suppressed (user is typing)")
		return false
	}
	g.showBalloonSprite(g.renderer().RenderImage(img))
	g.blnMu.Lock()
	g.blnPicture = true
	g.blnMu.Unlock()
	return true
}

// pictureShowing reports whether the balloon holds a picture.
func (g *Ghost) pictureShowing() bool {
	g.blnMu.Lock()
	defer g.blnMu.Unlock()
	return g.blnUp && g.blnPicture
}

// placeBalloon re-anchors the balloon to the sprite's current position.
func (g *Ghost) placeBalloon() {
	g.blnMu.Lock()
	bln := g.bln
	g.blnMu.Unlock()
	if bln == nil {
		return
	}
	// The panel's own width, so a ghost dragged towards the right edge
	// flips its balloon over rather than dragging it off the screen.
	_, _, lw, _ := bln.Geometry()
	x, y := g.balloonPos(lw)
	if err := bln.MoveTo(x, y); err != nil {
		g.log.Warn(hlog.Balloon, "balloon follow failed: %v", err)
	}
}

func (g *Ghost) hideBalloon() {
	g.blnMu.Lock()
	bln := g.bln
	g.bln = nil
	g.blnWaiting = false
	g.blnPicture = false
	g.blnUp = false
	g.blnMu.Unlock()
	if bln != nil {
		bln.Destroy()
	}
}

// setExpression wears a declared pose. An unknown pose is a no-op with a
// log line rather than an error: a brain written for a richer shell should
// degrade quietly on a simpler one, not fall over.
func (g *Ghost) setExpression(pose string) {
	if g.shell == nil {
		return
	}
	g.shellMu.Lock()
	if g.shell.Pose() == pose {
		g.shellMu.Unlock()
		return
	}
	if !g.shell.SetPose(pose) {
		g.shellMu.Unlock()
		g.log.Debug(hlog.Anim, "expression %q is not one of this shell's poses %v",
			pose, g.shell.Poses())
		return
	}
	sp := g.shell.Sprite()
	damage := g.shell.Damage(g.painted)
	g.painted = g.shell.State()
	g.shellMu.Unlock()

	// The counterpart to the clip line: "did its face change" is otherwise
	// as unanswerable as "is it animating", and on a shell whose poses are
	// named something the driver cannot choose the answer is a silent no.
	g.log.Debug(hlog.Anim, "expression %q", pose)
	// Below the SetPose check, so only a face that really changed is
	// announced: a pose the shell does not have changed nothing anybody
	// could have seen.
	g.announce(proto.ActedExpression, pose)

	if g.surface == nil {
		return // no compositor: tests drive the shell without one
	}
	// Poses composite onto one canvas, so the surface can be repainted in
	// place — no flicker, and no re-derived input region — and only the
	// layers the new pose actually changed.
	if err := g.surface.RedrawRect(sp, damage); err != nil {
		g.log.Warn(hlog.Anim, "expression %q: %v", pose, err)
	}
}
