// Package wlclient wraps the Wayland client connection: registry binding,
// shm buffer management, and layer-shell sprite surfaces with alpha-based
// input regions and pointer dragging.
package wlclient

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/rajveermalviya/go-wayland/wayland/client"
	"github.com/rajveermalviya/go-wayland/wayland/stable/viewporter"
	ext_idle_notify "github.com/rajveermalviya/go-wayland/wayland/staging/ext-idle-notify-v1"
	fractionalscale "github.com/rajveermalviya/go-wayland/wayland/staging/fraction-sclae-v1"

	"github.com/aaronjauregui/haunt/internal/wayland/layershell"
)

// Layer-shell enums (from wlr-layer-shell-unstable-v1).
const (
	LayerOverlay = 3

	AnchorTop  = 1
	AnchorLeft = 4

	KeyboardInteractivityNone = 0
)

// BTN_LEFT/BTN_RIGHT from linux/input-event-codes.h.
const (
	btnLeft  = 0x110
	btnRight = 0x111
)

// App is a connected Wayland client with the globals Haunt needs.
//
// Threading model: the go-wayland library does no locking, so a.mu
// serializes all request writes and shared state. Exactly one goroutine may
// call Dispatch (the dispatch loop); event handlers run on it and take mu
// before issuing requests. Public surface methods lock mu and are safe from
// any goroutine.
type App struct {
	mu sync.Mutex

	display    *client.Display
	registry   *client.Registry
	compositor *client.Compositor
	shm        *client.Shm
	seat       *client.Seat
	pointer    *client.Pointer
	keyboard   *client.Keyboard
	layerShell *layershell.LayerShell
	idleMgr    *ext_idle_notify.IdleNotifier
	viewporter *viewporter.Viewporter
	fracScale  *fractionalscale.FractionalScaleManager

	// outputs are the screens, keyed by their registry name so hotplug can
	// remove them. A desktop companion lives on one of several monitors and
	// has to be told which, so this is a list rather than a single screen.
	outputs map[uint32]*Output

	// pointerFocus tracks which sprite surface the pointer is over;
	// keyboardFocus which one the compositor is sending keys to.
	surfaces      map[uint32]*SpriteSurface
	pointerFocus  *SpriteSurface
	keyboardFocus *SpriteSurface

	// repeatRate (keys per second) and repeatDelay are the compositor's
	// key repeat settings, from wl_keyboard.repeat_info. repeatGen names
	// the key currently repeating: bumping it stops that repeat, and a
	// repeat that finds it moved on delivers nothing more.
	repeatRate  int32
	repeatDelay time.Duration
	repeatGen   uint64

	// EventLog, when set, receives a line per input or surface event. The
	// driver points it at its own log's wayland topic; the PoC uses it to
	// expose protocol activity to tests. Nil means this costs nothing,
	// which matters for a callback on the pointer motion path.
	EventLog func(string)

	// OnOutputsChanged fires when a screen appears or disappears, so a
	// ghost stranded on an unplugged monitor can be brought back.
	OnOutputsChanged func()
}

func (a *App) logEvent(format string, args ...any) {
	if a.EventLog != nil {
		a.EventLog(fmt.Sprintf(format, args...))
	}
}

// Connect dials the compositor named by WAYLAND_DISPLAY and binds globals.
func Connect() (*App, error) {
	display, err := client.Connect("")
	if err != nil {
		return nil, fmt.Errorf("connecting to wayland display: %w", err)
	}
	a := &App{
		display:  display,
		surfaces: map[uint32]*SpriteSurface{},
		outputs:  map[uint32]*Output{},
	}

	registry, err := display.GetRegistry()
	if err != nil {
		return nil, err
	}
	a.registry = registry
	ctx := display.Context()

	registry.SetGlobalHandler(func(e client.RegistryGlobalEvent) {
		switch e.Interface {
		case "wl_compositor":
			a.compositor = client.NewCompositor(ctx)
			registry.Bind(e.Name, e.Interface, minu(e.Version, 4), a.compositor)
		case "wl_shm":
			a.shm = client.NewShm(ctx)
			registry.Bind(e.Name, e.Interface, 1, a.shm)
		case "wl_seat":
			if a.seat == nil {
				a.seat = client.NewSeat(ctx)
				registry.Bind(e.Name, e.Interface, minu(e.Version, 5), a.seat)
			}
		case "zwlr_layer_shell_v1":
			a.layerShell = layershell.NewLayerShell(ctx)
			registry.Bind(e.Name, e.Interface, minu(e.Version, 4), a.layerShell)
		case "wl_output":
			a.bindOutput(ctx, registry, e.Name, e.Version)
		case "wp_viewporter":
			a.viewporter = viewporter.NewViewporter(ctx)
			registry.Bind(e.Name, e.Interface, 1, a.viewporter)
		case "wp_fractional_scale_manager_v1":
			a.fracScale = fractionalscale.NewFractionalScaleManager(ctx)
			registry.Bind(e.Name, e.Interface, 1, a.fracScale)
		case "ext_idle_notifier_v1":
			a.idleMgr = ext_idle_notify.NewIdleNotifier(ctx)
			registry.Bind(e.Name, e.Interface, 1, a.idleMgr)
		}
	})

	registry.SetGlobalRemoveHandler(func(e client.RegistryGlobalRemoveEvent) {
		a.mu.Lock()
		out := a.outputs[e.Name]
		delete(a.outputs, e.Name)
		a.mu.Unlock()
		if out != nil {
			a.logEvent("output %s disconnected", out.Name)
			if a.OnOutputsChanged != nil {
				a.OnOutputsChanged()
			}
		}
	})

	if err := a.Roundtrip(); err != nil {
		return nil, err
	}
	if a.compositor == nil || a.shm == nil {
		return nil, fmt.Errorf("compositor is missing wl_compositor or wl_shm")
	}
	if a.layerShell == nil {
		return nil, fmt.Errorf("compositor does not support zwlr_layer_shell_v1 (wlroots-based compositor required)")
	}

	if a.seat != nil {
		a.seat.SetCapabilitiesHandler(func(e client.SeatCapabilitiesEvent) {
			const capPointer, capKeyboard = 1, 2
			if e.Capabilities&capPointer != 0 && a.pointer == nil {
				p, err := a.seat.GetPointer()
				if err != nil {
					return
				}
				a.pointer = p
				a.initPointer(p)
			}
			if e.Capabilities&capKeyboard != 0 && a.keyboard == nil {
				k, err := a.seat.GetKeyboard()
				if err != nil {
					return
				}
				a.keyboard = k
				a.initKeyboard(k)
			}
		})
		if err := a.Roundtrip(); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func minu(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}

// Output is one screen, in the compositor's logical coordinates.
type Output struct {
	// Name is the connector as the compositor calls it ("DP-1"), when it
	// says. It is what a person means by "the left monitor", and what the
	// driver persists to put a ghost back where it was.
	Name string
	// X, Y is the output's position in the global layout; W, H its logical
	// size.
	X, Y, W, H int32
	Scale      int32

	wl       *client.Output
	rawW     int32
	rawH     int32
	registry uint32
}

// bindOutput starts tracking a screen the compositor has announced.
func (a *App) bindOutput(ctx *client.Context, registry *client.Registry, name, version uint32) {
	out := &Output{wl: client.NewOutput(ctx), Scale: 1, registry: name}
	registry.Bind(name, "wl_output", minu(version, 4), out.wl)

	out.wl.SetGeometryHandler(func(e client.OutputGeometryEvent) {
		a.mu.Lock()
		out.X, out.Y = e.X, e.Y
		a.mu.Unlock()
	})
	out.wl.SetModeHandler(func(e client.OutputModeEvent) {
		const currentMode = 1
		if e.Flags&currentMode == 0 {
			return
		}
		a.mu.Lock()
		out.rawW, out.rawH = e.Width, e.Height
		out.recalc()
		a.mu.Unlock()
	})
	out.wl.SetScaleHandler(func(e client.OutputScaleEvent) {
		a.mu.Lock()
		out.Scale = e.Factor
		out.recalc()
		a.mu.Unlock()
	})
	out.wl.SetNameHandler(func(e client.OutputNameEvent) {
		a.mu.Lock()
		out.Name = e.Name
		a.mu.Unlock()
	})

	a.mu.Lock()
	a.outputs[name] = out
	a.mu.Unlock()
}

// recalc converts the mode to logical pixels; callers hold the lock.
func (o *Output) recalc() {
	scale := o.Scale
	if scale < 1 {
		scale = 1
	}
	o.W, o.H = o.rawW/scale, o.rawH/scale
}

// Outputs lists the screens, ordered left to right then top to bottom, so
// "the first one" means something stable to a person looking at them.
func (a *App) Outputs() []Output {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Output, 0, len(a.outputs))
	for _, o := range a.outputs {
		if o.W == 0 || o.H == 0 {
			continue // announced but not yet configured
		}
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].X != out[j].X {
			return out[i].X < out[j].X
		}
		return out[i].Y < out[j].Y
	})
	return out
}

// OutputNamed finds a screen by connector name.
func (a *App) OutputNamed(name string) (Output, bool) {
	for _, o := range a.Outputs() {
		if o.Name == name {
			return o, true
		}
	}
	return Output{}, false
}

// OutputSize is the first screen's logical size, for callers that only need
// a bound — a balloon three paragraphs tall is a wall on any monitor.
func (a *App) OutputSize() (w, h int32, known bool) {
	outs := a.Outputs()
	if len(outs) == 0 {
		return 0, 0, false
	}
	return outs[0].W, outs[0].H, true
}

// Roundtrip flushes requests and dispatches events until the compositor
// confirms it has processed everything sent so far.
func (a *App) Roundtrip() error {
	cb, err := a.display.Sync()
	if err != nil {
		return err
	}
	done := false
	cb.SetDoneHandler(func(client.CallbackDoneEvent) { done = true })
	ctx := a.display.Context()
	for !done {
		if err := ctx.Dispatch(); err != nil {
			return err
		}
	}
	return nil
}

// Dispatch processes one batch of incoming events (blocking).
func (a *App) Dispatch() error {
	return a.display.Context().Dispatch()
}

// Close tears down the connection.
func (a *App) Close() error {
	return a.display.Context().Close()
}

// WatchIdle asks the compositor for idle notifications after the given
// timeout of user inactivity. Callbacks run on the dispatch goroutine.
// Returns false when the compositor does not offer ext-idle-notify.
func (a *App) WatchIdle(timeoutMS uint32, onIdle, onResume func()) (bool, error) {
	if a.idleMgr == nil || a.seat == nil {
		return false, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	n, err := a.idleMgr.GetIdleNotification(timeoutMS, a.seat)
	if err != nil {
		return false, err
	}
	n.SetIdledHandler(func(ext_idle_notify.IdleNotificationIdledEvent) { onIdle() })
	n.SetResumedHandler(func(ext_idle_notify.IdleNotificationResumedEvent) { onResume() })
	return true, nil
}

func (a *App) initPointer(p *client.Pointer) {
	p.SetEnterHandler(func(e client.PointerEnterEvent) {
		if e.Surface == nil {
			return // an event about a surface we have already destroyed
		}
		if e.Surface == nil {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if s, ok := a.surfaces[e.Surface.ID()]; ok {
			a.logEvent("enter surface=%d local=(%.0f,%.0f)", e.Surface.ID(), e.SurfaceX, e.SurfaceY)
			a.pointerFocus = s
			s.pointerX, s.pointerY = e.SurfaceX, e.SurfaceY
		}
	})
	p.SetLeaveHandler(func(e client.PointerLeaveEvent) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.pointerFocus != nil {
			a.pointerFocus.dragging = false
			a.pointerFocus = nil
		}
	})
	p.SetMotionHandler(func(e client.PointerMotionEvent) {
		var fire func()
		a.mu.Lock()
		s := a.pointerFocus
		if s == nil {
			a.mu.Unlock()
			return
		}
		a.logEvent("motion local=(%.0f,%.0f) dragging=%v pos=(%d,%d)", e.SurfaceX, e.SurfaceY, s.dragging, s.X, s.Y)
		s.pointerX, s.pointerY = e.SurfaceX, e.SurfaceY
		if s.dragging && !s.opts.Fixed {
			// Surface-local coordinates keep their pre-drag base while the
			// surface moves under a stationary pointer (sway does not rebase
			// mid-drag), so position from the drag origin, not incrementally.
			s.X = s.dragOriginX + int32(e.SurfaceX-s.grabX)
			s.Y = s.dragOriginY + int32(e.SurfaceY-s.grabY)
			s.layerSurface.SetMargin(s.Y, 0, 0, s.X)
			s.wlSurface.Commit()
			if s.OnMove != nil {
				x, y := int(s.X), int(s.Y)
				cb := s.OnMove
				fire = func() { cb(x, y) }
			}
		}
		a.mu.Unlock()
		if fire != nil {
			fire()
		}
	})
	p.SetButtonHandler(func(e client.PointerButtonEvent) {
		// Callbacks are collected under the lock but invoked outside it, so
		// they may safely call locked surface methods.
		var fire func()
		a.mu.Lock()
		s := a.pointerFocus
		if s == nil {
			a.mu.Unlock()
			return
		}
		// Right-click is the "talk to me" gesture, kept separate from the
		// left button so a ghost's own click reactions are undisturbed.
		if e.Button == btnRight {
			onRight := s.OnRightClick
			a.mu.Unlock()
			const statePressed = 1
			if e.State == statePressed && onRight != nil {
				onRight()
			}
			return
		}
		if e.Button != btnLeft {
			a.mu.Unlock()
			return
		}
		const statePressed = 1
		a.logEvent("button state=%d at local=(%.0f,%.0f) pos=(%d,%d)", e.State, s.pointerX, s.pointerY, s.X, s.Y)
		if e.State == statePressed {
			// A fixed surface still reports the click; it just does not
			// travel with the pointer while the button is down. Dragging is
			// tracked either way, because a click is a press and a release
			// with no movement between them.
			s.dragging = true
			s.pressX, s.pressY = s.pointerX, s.pointerY
			s.grabX, s.grabY = s.pointerX, s.pointerY
			s.dragOriginX, s.dragOriginY = s.X, s.Y
		} else {
			wasDragging := s.dragging
			s.dragging = false
			moved := s.X != s.dragOriginX || s.Y != s.dragOriginY
			if s.opts.Fixed {
				// It never moved, so "moved" has to mean the pointer did.
				// A few pixels of hand-shake is still a click.
				const slop = 4
				moved = abs(s.pointerX-s.pressX) > slop || abs(s.pointerY-s.pressY) > slop
			}
			switch {
			case wasDragging && moved && s.OnDragEnd != nil:
				x, y := int(s.X), int(s.Y)
				cb := s.OnDragEnd
				fire = func() { cb(x, y) }
			case wasDragging && !moved && s.OnClick != nil:
				// A press+release without movement is a click.
				x, y := int(s.pointerX), int(s.pointerY)
				cb := s.OnClick
				fire = func() { cb(x, y) }
			}
		}
		a.mu.Unlock()
		if fire != nil {
			fire()
		}
	})
}

// initKeyboard routes key events to whichever surface holds keyboard focus.
// The keymap the compositor sends is deliberately ignored: interpreting xkb
// means libxkbcommon and therefore cgo, so scancodes go to the caller raw
// and internal/textinput maps them (US layout only — see its doc comment).
//
// Handlers run on the dispatch goroutine and are invoked outside a.mu, like
// the pointer ones, so a callback may issue requests without deadlocking.
func (a *App) initKeyboard(k *client.Keyboard) {
	k.SetEnterHandler(func(e client.KeyboardEnterEvent) {
		if e.Surface == nil {
			return
		}
		a.mu.Lock()
		s := a.surfaces[e.Surface.ID()]
		a.keyboardFocus = s
		a.mu.Unlock()
		a.logEvent("keyboard enter surface=%d", e.Surface.ID())
		if s != nil && s.OnKeyboardFocus != nil {
			s.OnKeyboardFocus(true)
		}
	})
	k.SetRepeatInfoHandler(func(e client.KeyboardRepeatInfoEvent) {
		a.mu.Lock()
		a.repeatRate = e.Rate
		a.repeatDelay = time.Duration(e.Delay) * time.Millisecond
		a.mu.Unlock()
		a.logEvent("key repeat rate=%d delay=%dms", e.Rate, e.Delay)
	})
	k.SetLeaveHandler(func(e client.KeyboardLeaveEvent) {
		a.mu.Lock()
		s := a.keyboardFocus
		a.keyboardFocus = nil
		a.repeatGen++
		a.mu.Unlock()
		a.logEvent("keyboard leave surface=%d", e.Surface.ID())
		if s != nil && s.OnKeyboardFocus != nil {
			s.OnKeyboardFocus(false)
		}
	})
	k.SetKeyHandler(func(e client.KeyboardKeyEvent) {
		const statePressed = 1
		down := e.State == statePressed
		a.mu.Lock()
		s := a.keyboardFocus
		// Any key event ends the current repeat: a release of the held key
		// obviously, and a press of another because the newest key is the
		// one that repeats, as everywhere else on the desktop.
		a.repeatGen++
		gen := a.repeatGen
		rate, delay := a.repeatRate, a.repeatDelay
		a.mu.Unlock()
		a.logEvent("key %d state=%d", e.Key, e.State)
		if s == nil || s.OnKey == nil {
			return
		}
		// Asked before the press is delivered, while the surface is in the
		// state the key was pressed in: a key that closes a menu must not
		// go on repeating into whatever opens after it.
		repeats := down && rate > 0 && s.KeyRepeats != nil && s.KeyRepeats(e.Key)
		s.OnKey(e.Key, down)
		if repeats {
			go a.repeatKey(s, e.Key, gen, rate, delay)
		}
	})
}

// repeatKey delivers further presses of a held key until something ends
// the repeat. Wayland leaves key repeat to the client — the compositor
// sends one press and one release, and the rate and delay to repeat at in
// between — so without this a held Backspace deletes one character.
//
// The repeats arrive from this goroutine rather than the dispatch one, so
// OnKey must be safe to call from either. A release racing the last tick
// can let one repeat land after it, the same as any client that repeats
// on a timer. Which keys repeat is the
// surface's call (KeyRepeats): that belongs to the keymap, which this
// package does not read.
func (a *App) repeatKey(s *SpriteSurface, code uint32, gen uint64, rate int32, delay time.Duration) {
	live := func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.repeatGen == gen && a.keyboardFocus == s && !s.destroyed
	}
	time.Sleep(delay)
	t := time.NewTicker(time.Second / time.Duration(rate))
	defer t.Stop()
	for live() {
		s.OnKey(code, true)
		<-t.C
	}
}

// TakeKeyboard asks the compositor to send keys to s, or to stop.
//
// While it wants keys the surface takes *exclusive* interactivity, and the
// timing is the whole point: on-demand focus is surrendered as soon as the
// pointer wanders onto something else, so under focus-follows-mouse a
// half-typed line evaporates the moment you reach for the mouse. Exclusive
// keeps the keyboard until the line is closed, which is what every
// launcher on this layer does for the same reason. The ghost holds it only
// while a line is open, so it never swallows keystrokes meant for whatever
// you were actually working in.
func (a *App) TakeKeyboard(s *SpriteSurface, want bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s.destroyed {
		return nil
	}
	mode := uint32(layershell.LayerSurfaceKeyboardInteractivityNone)
	if want {
		mode = uint32(layershell.LayerSurfaceKeyboardInteractivityExclusive)
	}
	if err := s.layerSurface.SetKeyboardInteractivity(mode); err != nil {
		return err
	}
	return s.wlSurface.Commit()
}

// abs is math.Abs without the import, for pointer slop.
func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
