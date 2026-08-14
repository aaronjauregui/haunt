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

	"github.com/aaronjauregui/haunt/internal/wayland/layershell"
)

// Layer-shell enums (from wlr-layer-shell-unstable-v1).
const (
	LayerOverlay = 3

	AnchorTop  = 1
	AnchorLeft = 4

	KeyboardInteractivityNone = 0
)

const btnLeft = 0x110 // BTN_LEFT from linux/input-event-codes.h

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
	layerShell *layershell.LayerShell

	// pointerFocus tracks which sprite surface the pointer is over.
	surfaces     map[uint32]*SpriteSurface
	pointerFocus *SpriteSurface

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
			const capPointer = 1
			if e.Capabilities&capPointer != 0 && a.pointer == nil {
				p, err := a.seat.GetPointer()
				if err != nil {
					return
				}
				a.pointer = p
				a.initPointer(p)
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
		a.mu.Lock()
		defer a.mu.Unlock()
		s := a.pointerFocus
		if s == nil {
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
		}
	})
	p.SetButtonHandler(func(e client.PointerButtonEvent) {
		// Callbacks are collected under the lock but invoked outside it, so
		// they may safely call locked surface methods.
		var fire func()
		a.mu.Lock()
		s := a.pointerFocus
		if s == nil || e.Button != btnLeft {
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
