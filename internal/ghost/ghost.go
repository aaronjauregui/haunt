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
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/quiet"
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
	// X, Y is the sprite's starting position.
	X, Y int
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
	blnRend *balloon.Renderer

	blnMu sync.Mutex
	bln   *wlclient.SpriteSurface

	bus   *bus.Bus
	brain brain.Runner
	store Store
	quiet *quiet.Checker

	aiSvc    *ai.Service
	cacheDir string
	aiMu     sync.Mutex // one AI request at a time per ghost

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

	// Shell.
	var sp *sprite.Sprite
	if m.Shell.Sprite != "" {
		if sp, err = sprite.Load(filepath.Join(dir, m.Shell.Sprite)); err != nil {
			return err
		}
	} else {
		sp = sprite.Builtin(160)
	}

	// Renderer.
	if g.app, err = wlclient.Connect(); err != nil {
		return err
	}
	defer g.app.Close()
	if g.surface, err = g.app.CreateSprite(sp, int32(opts.X), int32(opts.Y)); err != nil {
		return err
	}
	if err = g.surface.WaitMapped(); err != nil {
		return err
	}
	if g.blnRend, err = balloon.NewRenderer(balloon.DefaultTheme()); err != nil {
		return err
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
		g.publish(proto.Event{Type: proto.EventClick, X: x, Y: y})
	}
	g.surface.OnDragEnd = func(x, y int) {
		g.publish(proto.Event{Type: proto.EventDragEnd, X: x, Y: y})
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
		ctl, err := control.Listen(m.Ghost.Name, g.bus.Publish, opts.Log)
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
		g.opts.Log(fmt.Sprintf("haunt: dev mode, auto-granting capability %q", cap))
		return true
	}
	return g.opts.Granted(cap)
}

func (g *Ghost) publish(e proto.Event) {
	if err := g.bus.Publish(e); err != nil {
		g.opts.Log(fmt.Sprintf("haunt: publish %s: %v", e.Type, err))
	}
}

// execIntent runs on the brain's intent-reader goroutine. Wayland calls are
// safe here: go-wayland requests are locked writes to the socket, and the
// dispatch loop only reads events.
func (g *Ghost) execIntent(in proto.Intent) {
	switch in.Type {
	case proto.IntentSay:
		g.showBalloon(in.Text)
	case proto.IntentHideSay:
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
		// Own goroutine: a slow backend must not block other intents.
		// aiMu keeps one request in flight per ghost.
		go func() {
			g.aiMu.Lock()
			defer g.aiMu.Unlock()
			g.execChat(in)
		}()
	case proto.IntentImagine:
		go func() {
			g.aiMu.Lock()
			defer g.aiMu.Unlock()
			g.execImagine(in)
		}()
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
	g.hideBalloon()
	sp := g.blnRend.Render(text)
	x, y := g.balloonPos()
	bln, err := g.app.CreateSpriteOpts(sp, x, y, wlclient.SurfaceOptions{PassThrough: true})
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
	g.hideBalloon()
	sp := g.blnRend.RenderImage(img)
	x, y := g.balloonPos()
	bln, err := g.app.CreateSpriteOpts(sp, x, y, wlclient.SurfaceOptions{PassThrough: true})
	if err != nil {
		g.opts.Log(fmt.Sprintf("haunt: image balloon failed: %v", err))
		return
	}
	g.blnMu.Lock()
	g.bln = bln
	g.blnMu.Unlock()
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
