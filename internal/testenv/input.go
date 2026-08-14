package testenv

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rajveermalviya/go-wayland/wayland/client"

	"github.com/aaronjauregui/haunt/internal/wayland/virtualpointer"
)

// Pointer injects synthetic pointer input into the headless compositor via
// the zwlr_virtual_pointer_v1 protocol, from its own client connection.
type Pointer struct {
	display *client.Display
	ptr     *virtualpointer.VirtualPointer
	// extent is the output size absolute motions are mapped onto.
	extentW, extentH uint32
	timeMS           uint32
}

const (
	btnLeft       = 0x110
	btnRight      = 0x111
	statePressed  = 1
	stateReleased = 0
)

// NewPointer connects to the compositor and creates a virtual pointer.
func (c *Compositor) NewPointer() (*Pointer, error) {
	display, err := client.Connect(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), c.Display))
	if err != nil {
		return nil, fmt.Errorf("connecting input client: %w", err)
	}
	p := &Pointer{display: display, extentW: 1280, extentH: 720}

	registry, err := display.GetRegistry()
	if err != nil {
		return nil, err
	}
	ctx := display.Context()
	var seat *client.Seat
	var mgr *virtualpointer.VirtualPointerManager
	registry.SetGlobalHandler(func(e client.RegistryGlobalEvent) {
		switch e.Interface {
		case "wl_seat":
			if seat == nil {
				seat = client.NewSeat(ctx)
				registry.Bind(e.Name, e.Interface, 5, seat)
			}
		case "zwlr_virtual_pointer_manager_v1":
			mgr = virtualpointer.NewVirtualPointerManager(ctx)
			registry.Bind(e.Name, e.Interface, e.Version, mgr)
		}
	})
	if err := p.roundtrip(); err != nil {
		return nil, err
	}
	if mgr == nil {
		return nil, fmt.Errorf("compositor does not support zwlr_virtual_pointer_manager_v1")
	}
	if p.ptr, err = mgr.CreateVirtualPointer(seat); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Pointer) roundtrip() error {
	cb, err := p.display.Sync()
	if err != nil {
		return err
	}
	done := false
	cb.SetDoneHandler(func(client.CallbackDoneEvent) { done = true })
	for !done {
		if err := p.display.Context().Dispatch(); err != nil {
			return err
		}
	}
	return nil
}

func (p *Pointer) tick() uint32 {
	p.timeMS += 10
	return p.timeMS
}

// MoveTo moves the pointer to absolute output coordinates.
func (p *Pointer) MoveTo(x, y int) error {
	if err := p.ptr.MotionAbsolute(p.tick(), uint32(x), uint32(y), p.extentW, p.extentH); err != nil {
		return err
	}
	if err := p.ptr.Frame(); err != nil {
		return err
	}
	return p.roundtrip()
}

// Press presses the left mouse button.
func (p *Pointer) Press() error {
	if err := p.ptr.Button(p.tick(), btnLeft, statePressed); err != nil {
		return err
	}
	if err := p.ptr.Frame(); err != nil {
		return err
	}
	return p.roundtrip()
}

// Release releases the left mouse button.
func (p *Pointer) Release() error {
	if err := p.ptr.Button(p.tick(), btnLeft, stateReleased); err != nil {
		return err
	}
	if err := p.ptr.Frame(); err != nil {
		return err
	}
	return p.roundtrip()
}

// RightClick presses and releases the right mouse button where the pointer
// is: the gesture that opens a line to type at the ghost.
func (p *Pointer) RightClick() error {
	if err := p.ptr.Button(p.tick(), btnRight, statePressed); err != nil {
		return err
	}
	if err := p.ptr.Frame(); err != nil {
		return err
	}
	if err := p.ptr.Button(p.tick(), btnRight, stateReleased); err != nil {
		return err
	}
	if err := p.ptr.Frame(); err != nil {
		return err
	}
	return p.roundtrip()
}

// Drag presses at (x0, y0), moves to (x1, y1), and releases.
func (p *Pointer) Drag(x0, y0, x1, y1 int) error {
	if err := p.MoveTo(x0, y0); err != nil {
		return err
	}
	if err := p.Press(); err != nil {
		return err
	}
	if err := p.MoveTo(x1, y1); err != nil {
		return err
	}
	return p.Release()
}

// Close disconnects the input client.
func (p *Pointer) Close() {
	p.ptr.Destroy()
	p.display.Context().Close()
}
