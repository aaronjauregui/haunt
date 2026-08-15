// Package ghost runs one ghost: it loads the manifest, shell, and brain,
// connects renderer input to the event bus, delivers events to the brain,
// and executes the brain's intents against the screen and the store.
package ghost

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/aaronjauregui/haunt/internal/ai"
	"github.com/aaronjauregui/haunt/internal/balloon"
	"github.com/aaronjauregui/haunt/internal/brain"
	"github.com/aaronjauregui/haunt/internal/bus"
	"github.com/aaronjauregui/haunt/internal/control"
	"github.com/aaronjauregui/haunt/internal/desktop"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/quiet"
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
	// All returns the complete KV map, handed to brains at boot.
	All() (map[string]string, error)
	// Close flushes and releases the backend.
	Close() error
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
	// Log receives driver/brain log lines; defaults to stderr.
	Log func(string)
	// OnReady is called once the ghost is on screen and booted.
	OnReady func()
	// Granted reports whether the user granted a capability the manifest
	// declares. Nil means dev mode: everything declared is granted, with a
	// log line. Capabilities not in the manifest are never active.
	Granted func(cap string) bool
	// IdleTimeoutMS overrides the inactivity threshold for idle events
	// (default 2 minutes).
	IdleTimeoutMS uint32
}

// Ghost is a running instance.
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

	anim          *animator
	reducedMotion bool
	blnRend       *balloon.Renderer
	blnTheme      balloon.Theme

	blnMu sync.Mutex
	bln   *wlclient.SpriteSurface

	bus     *bus.Bus
	brain   brain.Runner
	store   Store
	quiet   *quiet.Checker
	desktop *desktop.Watcher

	aiSvc    *ai.Service
	cacheDir string
	aiMu     sync.Mutex // one AI request at a time per ghost

	// Typed input (see input.go).
	inputMu sync.Mutex
	input   inputState

	// Initiative accounting (see curiosity.go).
	curiosity       *curiosity
	initMu          sync.Mutex
	promptedUntil   time.Time
	initiativeUntil time.Time

	// autoGranted tracks which dev-mode grants have been logged, so the
	// notice appears once per capability rather than once per check.
	autoGranted sync.Map

	timersMu sync.Mutex
	timers   map[string]*time.Timer

	recordF *os.File
	done    chan struct{}
}

// Run loads and starts the ghost in dir, blocking until ctx is cancelled or
// the session fails. The Wayland connection must be usable (WAYLAND_DISPLAY).
func Run(ctx context.Context, dir string, opts Options) error {
	if opts.Log == nil {
		opts.Log = func(s string) { fmt.Fprintln(os.Stderr, s) }
	}
	m, err := LoadManifest(dir)
	if err != nil {
		return err
	}
	g := &Ghost{
		manifest: m,
		opts:     opts,
		timers:   map[string]*time.Timer{},
		done:     make(chan struct{}),
	}
	defer close(g.done)

	// State: the injected backend, or SQLite in the state directory.
	if opts.Store != nil {
		g.store = opts.Store
	} else {
		stateDir := opts.StateDir
		if stateDir == "" {
			base := os.Getenv("XDG_DATA_HOME")
			if base == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				base = filepath.Join(home, ".local", "share")
			}
			stateDir = filepath.Join(base, "haunt", "ghosts", m.Ghost.Name)
		}
		if err := os.MkdirAll(stateDir, 0o755); err != nil {
			return err
		}
		if g.store, err = store.Open(filepath.Join(stateDir, "state.db")); err != nil {
			return err
		}
	}
	defer g.store.Close()

	// Shell: a layer stack with named poses, or a single image, or the
	// built-in placeholder — all three arrive here as the same thing.
	if m.Shell.Sprite == "" && len(m.Shell.Layers) == 0 {
		g.shell = shell.Builtin(160)
	} else if g.shell, err = shell.Load(dir, m.Shell); err != nil {
		return err
	}
	sp := g.shell.Sprite()

	// Renderer.
	if g.app, err = wlclient.Connect(); err != nil {
		return err
	}
	defer g.app.Close()
	// Where it sits: an explicit position, or wherever it was left.
	outName, startX, startY := g.startingPlace(g.app, opts.X, opts.Y, opts.Placed)
	if opts.Output != "" {
		outName = opts.Output
		if o, ok := g.app.OutputNamed(outName); ok && !opts.Placed {
			startX, startY = defaultSpot(o)
		}
	}
	g.outputName = outName
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
		opts.Log(fmt.Sprintf("haunt: placed on %s at (%d,%d)", outName, startX, startY))
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
	// A ghost dragged to a 200% screen needs its text redrawn at that
	// screen's resolution, not stretched from the old one.
	g.surface.OnScaleChanged = func(scale float64) {
		opts.Log(fmt.Sprintf("haunt: scale is now %g", scale))
		r, err := balloon.NewRendererScaled(g.blnTheme, scale)
		if err != nil {
			opts.Log(fmt.Sprintf("haunt: rescaling balloons: %v", err))
			return
		}
		g.blnMu.Lock()
		g.blnRend = r
		g.blnMu.Unlock()
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

	// Quiet hours (config windows + manual shush).
	if g.quiet, err = quiet.Load(); err != nil {
		return err
	}
	// Fullscreen awareness: the ghost keeps out of the way of games and
	// films, which is both courtesy and — for image generation — leaving
	// the GPU alone.
	g.desktop = desktop.New(opts.Log)

	anim, err := loadAnimationConfig()
	if err != nil {
		return err
	}
	g.reducedMotion = anim.ReducedMotion

	// Initiative allowance. Persisted, so restarting the ghost is not a way
	// to refill it.
	actionsPerDay, err := loadCuriosityConfig()
	if err != nil {
		return err
	}
	g.curiosity = newCuriosity(actionsPerDay, g.store, opts.Log)

	// AI services (backends are user-configured; absent config just means
	// the capabilities error out when used).
	aiCfg, err := ai.LoadConfig()
	if err != nil {
		return err
	}
	g.aiSvc = ai.NewService(aiCfg, opts.Log)
	g.cacheDir = filepath.Join(opts.StateDir, "cache")
	if opts.StateDir == "" {
		g.cacheDir = filepath.Join(os.TempDir(), "haunt-cache-"+m.Ghost.Name)
	}

	// Brain: sandboxed WASM by default; a native process only when the
	// manifest declares (and the user granted) the native-brain capability.
	if len(m.Brain.Command) > 0 {
		if !g.capabilityActive("native-brain") {
			return fmt.Errorf("ghost %q wants a native brain but the native-brain capability is not granted", m.Ghost.Name)
		}
		if g.brain, err = brain.StartProcess(dir, m.Brain.Command, g.execIntent, brain.Options{}); err != nil {
			return err
		}
	} else {
		wasmBytes, err := os.ReadFile(filepath.Join(dir, m.Brain.Wasm))
		if err != nil {
			return err
		}
		if g.brain, err = brain.Start(ctx, wasmBytes, g.execIntent, brain.Options{}); err != nil {
			return err
		}
	}

	// Input -> bus.
	g.surface.OnClick = func(x, y int) {
		// Poking the ghost puts away whatever was half-typed, the way
		// clicking off an input box does anywhere else — and does it first,
		// so the brain's reaction is not suppressed as "user is typing".
		g.closeInput()
		// A balloon with more to read has first claim on the click.
		if g.turnPage() {
			return
		}
		g.publish(proto.Event{Type: proto.EventClick, X: x, Y: y, Region: g.shell.RegionAt(x, y)})
	}
	// Right-click opens a line to type at the ghost. A separate gesture
	// from the left click, which belongs to the brain: a scripted ghost's
	// quip on being poked must not be traded away for an input box.
	g.surface.OnRightClick = g.openInput
	g.surface.OnKey = g.onKey
	g.surface.OnKeyboardFocus = func(focused bool) {
		if !focused {
			// The compositor gave the keyboard to something else: the
			// user's attention has moved on, so the half-typed line goes
			// with it.
			g.closeInput()
		}
	}
	g.surface.OnDragEnd = func(x, y int) {
		g.publish(proto.Event{Type: proto.EventDragEnd, X: x, Y: y, Region: g.shell.RegionAt(x, y)})
		// Dragging is how a person says where they want it, so that is
		// what gets remembered.
		if sx, sy, _, _ := g.surface.Geometry(); true {
			g.savePlace(int(sx), int(sy))
		}
	}
	// The balloon belongs to the ghost: it follows the sprite while dragged.
	g.surface.OnMove = func(x, y int) {
		g.placeBalloon()
	}

	// Bus -> brain.
	deliveryDone := make(chan struct{})
	go func() {
		defer close(deliveryDone)
		for e := range g.bus.Events() {
			if err := g.brain.Deliver(e); err != nil {
				opts.Log(fmt.Sprintf("haunt: brain delivery failed: %v", err))
				return
			}
		}
	}()

	// Control socket: external programs push events in (haunt say, hooks).
	// Requires the "message" capability: declared in the manifest and
	// granted by the user (dev runs auto-grant declared capabilities).
	if g.capabilityActive("message") {
		// Through g.publishExternal, not the bus directly: `haunt say` is a
		// person addressing the ghost, and the reply must not be charged to
		// the ghost's own initiative.
		ctl, err := control.Listen(m.Ghost.Name, g.publishExternal, opts.Log)
		if err != nil {
			return err
		}
		defer ctl.Close()
	}

	// Idle awareness: after inactivity brains hear idle, and active when
	// the user returns. Compositor support is optional.
	idleTimeoutMS := opts.IdleTimeoutMS
	if idleTimeoutMS == 0 {
		idleTimeoutMS = 2 * 60 * 1000
	}
	supported, err := g.app.WatchIdle(idleTimeoutMS,
		func() { g.publish(proto.Event{Type: proto.EventIdle}) },
		func() { g.publish(proto.Event{Type: proto.EventActive}) },
	)
	if err != nil {
		return err
	}
	if !supported {
		opts.Log("haunt: compositor lacks ext-idle-notify; idle events disabled")
	}

	// Scheduled triggers from the manifest.
	if len(m.Triggers) > 0 {
		sched := cron.New()
		for _, tr := range m.Triggers {
			name := tr.Name
			if _, err := sched.AddFunc(tr.Cron, func() {
				g.publish(proto.Event{Type: proto.EventCron, Name: name})
			}); err != nil {
				return fmt.Errorf("trigger %q: %w", name, err)
			}
		}
		sched.Start()
		defer sched.Stop()
	}

	// Boot with the persisted KV snapshot.
	kv, err := g.store.All()
	if err != nil {
		return err
	}
	g.publish(proto.Event{Type: proto.EventBoot, KV: visibleKV(kv)})
	if opts.OnReady != nil {
		opts.OnReady()
	}

	// Idle animation: blinking and the like, on the shell's own schedule.
	g.startAnimation()
	defer g.stopAnimation()

	// Repair the memory index if the embedding model changed since it was
	// written. In the background: it is a long job on a large index, and
	// the ghost should be talking meanwhile.
	go g.reembedStale(ctx)

	// Wayland event loop until cancelled.
	dispatchErr := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				dispatchErr <- fmt.Errorf("dispatch panic: %v", r)
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

	// Orderly shutdown: tell the brain, stop timers, drain deliveries, then
	// close the brain so the shutdown event is not lost to a closed pipe.
	g.publish(proto.Event{Type: proto.EventShutdown})
	g.timersMu.Lock()
	for _, t := range g.timers {
		t.Stop()
	}
	g.timersMu.Unlock()
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
	declared := false
	for _, c := range g.manifest.Ghost.Capabilities {
		if c == cap {
			declared = true
			break
		}
	}
	if !declared {
		return false
	}
	if g.opts.Granted == nil {
		// Once per capability: this is checked on every use, and memory
		// work checks it several times per exchange.
		if _, seen := g.autoGranted.LoadOrStore(cap, true); !seen {
			g.opts.Log(fmt.Sprintf("haunt: dev mode, auto-granting capability %q", cap))
		}
		return true
	}
	return g.opts.Granted(cap)
}

func (g *Ghost) publish(e proto.Event) {
	if err := g.publishExternal(e); err != nil {
		g.opts.Log(fmt.Sprintf("haunt: publish %s: %v", e.Type, err))
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
	switch {
	case e.Type == proto.EventBoot:
		g.markPromptedFor(bootWindow)
	case promptedEvent(e):
		g.markPrompted()
	case e.Type == proto.EventIdle:
		g.endResponding()
	}
	return g.bus.Publish(e)
}

// execIntent runs on the brain's intent-reader goroutine. Wayland calls are
// safe here: go-wayland requests are locked writes to the socket, and the
// dispatch loop only reads events.
func (g *Ghost) execIntent(in proto.Intent) {
	switch in.Type {
	case proto.IntentSay:
		// Speaking up unasked is the interruption the budget exists to
		// ration, whether the words came from a model or a table.
		if !g.allowInitiative("say") {
			return
		}
		// Scripted dialogue gets the free tier of expression inference. A
		// brain that also sets an expression runs after this and wins.
		g.inferExpression(in.Text, false)
		// Scripted dialogue arrives whole, so it is readable from its
		// beginning immediately, pages and all.
		g.showPaged(in.Text)
		// And the mouth moves for about as long as the line would take to
		// say, since there is no stream to follow here.
		g.talkFor(in.Text)
	case proto.IntentHideSay:
		g.stopTalking()
		g.clearPaging()
		g.hideBalloon()
	case proto.IntentMove:
		if err := g.surface.MoveTo(int32(in.X), int32(in.Y)); err != nil {
			g.opts.Log(fmt.Sprintf("haunt: move failed: %v", err))
		}
		g.placeBalloon()
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
			g.opts.Log(fmt.Sprintf("haunt: kv_set %s dropped: reserved prefix", in.Name))
			return
		}
		if err := g.store.Set(in.Name, in.Value); err != nil {
			g.opts.Log(fmt.Sprintf("haunt: kv_set %s: %v", in.Name, err))
		}
	case proto.IntentLog:
		g.opts.Log("ghost: " + in.Text)
	case proto.IntentChat:
		// Gate before the goroutine: the answer to "was this provoked?"
		// belongs to the moment the intent arrived, not to whenever a
		// backend gets around to it.
		if !g.allowInitiative("chat") {
			return
		}
		// Whether a person is waiting travels with the request, so a slow
		// backend cannot turn a reply into an interruption on arrival.
		userDriven := g.respondingToUser()
		// Own goroutine: a slow backend must not block other intents.
		// aiMu keeps one request in flight per ghost.
		go func() {
			g.aiMu.Lock()
			defer g.aiMu.Unlock()
			g.execChat(in, userDriven)
		}()
	case proto.IntentImagine:
		if !g.allowInitiative("imagine") {
			return
		}
		userDriven := g.respondingToUser()
		go func() {
			g.aiMu.Lock()
			defer g.aiMu.Unlock()
			g.execImagine(in, userDriven)
		}()
	case proto.IntentSetExpression:
		g.setExpression(in.Name)
	case proto.IntentPlayClip:
		g.playClip(in.Name)
	case proto.IntentNote:
		g.execNote(in.Text)
	case proto.IntentTell:
		if !g.capabilityActive("tell") {
			g.opts.Log(fmt.Sprintf("haunt: tell intent dropped: capability not granted"))
			return
		}
		if in.Name == g.manifest.Ghost.Name {
			g.opts.Log("haunt: tell intent dropped: ghost addressed itself")
			return
		}
		err := control.Send(in.Name, proto.Event{
			Type: proto.EventMessage,
			Text: in.Text,
			From: g.manifest.Ghost.Name,
		})
		if err != nil {
			g.opts.Log(fmt.Sprintf("haunt: tell %s: %v", in.Name, err))
		}
	}
}

func (g *Ghost) setTimer(name string, d time.Duration) {
	g.timersMu.Lock()
	defer g.timersMu.Unlock()
	if t, ok := g.timers[name]; ok {
		t.Stop()
	}
	g.timers[name] = time.AfterFunc(d, func() {
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

// balloonPos computes the balloon anchor: to the right of the sprite,
// vertically near its top.
func (g *Ghost) balloonPos() (int32, int32) {
	x, y, w, _ := g.surface.Geometry()
	return x + w + 12, y
}

// showBalloon renders text and places it beside the sprite. During quiet
// hours it drops the balloon; the driver enforces this, not the ghost.
func (g *Ghost) showBalloon(text string) {
	if muted, why := g.quiet.Active(); muted {
		g.opts.Log(fmt.Sprintf("haunt: balloon suppressed (%s)", why))
		return
	}
	// Never paint over a line the user is in the middle of typing.
	if g.inputOpen() {
		g.opts.Log("haunt: balloon suppressed (user is typing)")
		return
	}
	g.showBalloonSprite(g.blnRend.Render(text))
}

// showBalloonSprite puts a rendered panel on screen beside the ghost. It
// asks no questions about quiet hours or input state — callers decide that,
// because the input line itself must appear regardless of both.
func (g *Ghost) showBalloonSprite(sp *sprite.Sprite) {
	g.hideBalloon()
	if g.surface == nil {
		return // no compositor: tests drive balloons without a screen
	}
	x, y := g.balloonPos()
	w, h := sp.Size()
	lw, lh := g.blnRend.LogicalSize(w, h)
	bln, err := g.app.CreateSpriteOpts(sp, x, y, wlclient.SurfaceOptions{
		PassThrough: true,
		LogicalW:    int32(lw),
		LogicalH:    int32(lh),
	})
	if err != nil {
		g.opts.Log(fmt.Sprintf("haunt: balloon failed: %v", err))
		return
	}
	g.blnMu.Lock()
	g.bln = bln
	g.blnMu.Unlock()
}

// showImageBalloon renders an image into a balloon panel beside the sprite.
func (g *Ghost) showImageBalloon(img image.Image) {
	if muted, why := g.quiet.Active(); muted {
		g.opts.Log(fmt.Sprintf("haunt: image balloon suppressed (%s)", why))
		return
	}
	if g.inputOpen() {
		g.opts.Log("haunt: image balloon suppressed (user is typing)")
		return
	}
	g.showBalloonSprite(g.blnRend.RenderImage(img))
}

// placeBalloon re-anchors the balloon to the sprite's current position.
func (g *Ghost) placeBalloon() {
	g.blnMu.Lock()
	bln := g.bln
	g.blnMu.Unlock()
	if bln == nil {
		return
	}
	x, y := g.balloonPos()
	if err := bln.MoveTo(x, y); err != nil {
		g.opts.Log(fmt.Sprintf("haunt: balloon follow failed: %v", err))
	}
}

func (g *Ghost) hideBalloon() {
	g.blnMu.Lock()
	bln := g.bln
	g.bln = nil
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
		g.opts.Log(fmt.Sprintf("haunt: expression %q is not one of this shell's poses %v",
			pose, g.shell.Poses()))
		return
	}
	sp := g.shell.Sprite()
	damage := g.shell.Damage(g.painted)
	g.painted = g.shell.State()
	g.shellMu.Unlock()

	if g.surface == nil {
		return // no compositor: tests drive the shell without one
	}
	// Poses composite onto one canvas, so the surface can be repainted in
	// place — no flicker, and no re-derived input region — and only the
	// layers the new pose actually changed.
	if err := g.surface.RedrawRect(sp, damage); err != nil {
		g.opts.Log(fmt.Sprintf("haunt: expression %q: %v", pose, err))
	}
}
