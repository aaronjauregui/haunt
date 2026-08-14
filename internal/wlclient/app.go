// Package wlclient wraps the Wayland client connection: registry binding,
// shm buffer management, and layer-shell sprite surfaces with alpha-based
// input regions and pointer dragging.
package wlclient

import (
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/rajveermalviya/go-wayland/wayland/client"
	ext_idle_notify "github.com/rajveermalviya/go-wayland/wayland/staging/ext-idle-notify-v1"

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

// debugf logs pointer/protocol events when HAUNT_DEBUG is set.
func debugf(format string, args ...any) {
	if os.Getenv("HAUNT_DEBUG") != "" {
		log.Printf(format, args...)
	}
}

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

	// pointerFocus tracks which sprite surface the pointer is over;
	// keyboardFocus which one the compositor is sending keys to.
	surfaces      map[uint32]*SpriteSurface
	pointerFocus  *SpriteSurface
	keyboardFocus *SpriteSurface

	// EventLog, when set, receives a line per input event. Used by the PoC
	// to expose protocol activity to tests.
	EventLog func(string)
}

func (a *App) logEvent(format string, args ...any) {
	debugf(format, args...)
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
	a := &App{display: display, surfaces: map[uint32]*SpriteSurface{}}

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
		case "ext_idle_notifier_v1":
			a.idleMgr = ext_idle_notify.NewIdleNotifier(ctx)
			registry.Bind(e.Name, e.Interface, 1, a.idleMgr)
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
		if s.dragging {
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
			s.dragging = true
			s.grabX, s.grabY = s.pointerX, s.pointerY
			s.dragOriginX, s.dragOriginY = s.X, s.Y
		} else {
			wasDragging := s.dragging
			s.dragging = false
			moved := s.X != s.dragOriginX || s.Y != s.dragOriginY
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
		a.mu.Lock()
		s := a.surfaces[e.Surface.ID()]
		a.keyboardFocus = s
		a.mu.Unlock()
		a.logEvent("keyboard enter surface=%d", e.Surface.ID())
		if s != nil && s.OnKeyboardFocus != nil {
			s.OnKeyboardFocus(true)
		}
	})
	k.SetLeaveHandler(func(e client.KeyboardLeaveEvent) {
		a.mu.Lock()
		s := a.keyboardFocus
		a.keyboardFocus = nil
		a.mu.Unlock()
		a.logEvent("keyboard leave surface=%d", e.Surface.ID())
		if s != nil && s.OnKeyboardFocus != nil {
			s.OnKeyboardFocus(false)
		}
	})
	k.SetKeyHandler(func(e client.KeyboardKeyEvent) {
		a.mu.Lock()
		s := a.keyboardFocus
		a.mu.Unlock()
		const statePressed = 1
		a.logEvent("key %d state=%d", e.Key, e.State)
		if s != nil && s.OnKey != nil {
			s.OnKey(e.Key, e.State == statePressed)
		}
	})
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
