package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rajveermalviya/go-wayland/wayland/client"
	"golang.org/x/sys/unix"

	"github.com/aaronjauregui/haunt/internal/textinput"
	"github.com/aaronjauregui/haunt/internal/wayland/virtualkeyboard"
)

// Keyboard types at the headless compositor through
// zwp_virtual_keyboard_v1, from its own client connection.
//
// It exists because the harness runs sway with WLR_LIBINPUT_NO_DEVICES=1,
// so the seat otherwise advertises no keyboard capability at all — and a
// seat with no keyboard means the ghost never holds keyboard focus, which
// means neither typing *nor focus behaviour* could be tested. Both of the
// input line's real bugs so far lived in that gap and were found by hand.
// Creating a virtual keyboard gives the seat one, which is the point: the
// ghost sees an ordinary keyboard on an ordinary seat.
type Keyboard struct {
	display *client.Display
	kb      *virtualkeyboard.VirtualKeyboard
	timeMS  uint32
}

// keymapSource is the layout the virtual keyboard declares. The includes
// are resolved by the *compositor's* libxkbcommon against the system's xkb
// data, so this stays five lines instead of a few thousand. The driver
// itself never reads a keymap — it maps scancodes in pure Go — so this only
// has to be something sway will accept.
const keymapSource = `xkb_keymap {
	xkb_keycodes { include "evdev+aliases(qwerty)" };
	xkb_types    { include "complete" };
	xkb_compat   { include "complete" };
	xkb_symbols  { include "pc+us+inet(evdev)" };
};
`

// Modifier bit for shift, in the terms wl_keyboard uses. The compositor
// applies the keymap to the raw codes we send; shift has to be declared
// separately or every capital arrives lowercase.
const modShift = 1 << 0

// keyLeftShift is the evdev code of the left shift key, which is pressed
// as well as declared: the driver maps scancodes itself and reads shift
// from the key, as it arrives from a real keyboard.
const keyLeftShift = 42

// NewKeyboard connects to the compositor and creates a virtual keyboard on
// its seat.
func (c *Compositor) NewKeyboard() (*Keyboard, error) {
	display, err := client.Connect(filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), c.Display))
	if err != nil {
		return nil, fmt.Errorf("connecting keyboard client: %w", err)
	}
	k := &Keyboard{display: display}

	registry, err := display.GetRegistry()
	if err != nil {
		return nil, err
	}
	ctx := display.Context()
	var seat *client.Seat
	var mgr *virtualkeyboard.VirtualKeyboardManager
	registry.SetGlobalHandler(func(e client.RegistryGlobalEvent) {
		switch e.Interface {
		case "wl_seat":
			if seat == nil {
				seat = client.NewSeat(ctx)
				registry.Bind(e.Name, e.Interface, 5, seat)
			}
		case "zwp_virtual_keyboard_manager_v1":
			mgr = virtualkeyboard.NewVirtualKeyboardManager(ctx)
			registry.Bind(e.Name, e.Interface, e.Version, mgr)
		}
	})
	if err := k.roundtrip(); err != nil {
		return nil, err
	}
	if mgr == nil {
		return nil, fmt.Errorf("compositor does not support zwp_virtual_keyboard_manager_v1")
	}
	if k.kb, err = mgr.CreateVirtualKeyboard(seat); err != nil {
		return nil, err
	}
	if err := k.sendKeymap(); err != nil {
		return nil, err
	}
	return k, k.roundtrip()
}

// sendKeymap hands the layout over. Required before any key event: without
// it the compositor answers with a protocol error, which is a rude but
// effective way to find out.
func (k *Keyboard) sendKeymap() error {
	fd, err := unix.MemfdCreate("haunt-keymap", unix.MFD_CLOEXEC)
	if err != nil {
		return fmt.Errorf("memfd_create: %w", err)
	}
	defer unix.Close(fd)
	if _, err := unix.Write(fd, []byte(keymapSource)); err != nil {
		return fmt.Errorf("writing keymap: %w", err)
	}
	const xkbV1 = 1
	return k.kb.Keymap(xkbV1, fd, uint32(len(keymapSource)))
}

func (k *Keyboard) roundtrip() error {
	cb, err := k.display.Sync()
	if err != nil {
		return err
	}
	done := false
	cb.SetDoneHandler(func(client.CallbackDoneEvent) { done = true })
	for !done {
		if err := k.display.Context().Dispatch(); err != nil {
			return err
		}
	}
	return nil
}

func (k *Keyboard) tick() uint32 {
	k.timeMS += 10
	return k.timeMS
}

// Press and release one key by evdev scancode.
func (k *Keyboard) Key(code uint32) error {
	if err := k.kb.Key(k.tick(), code, statePressed); err != nil {
		return err
	}
	if err := k.kb.Key(k.tick(), code, stateReleased); err != nil {
		return err
	}
	return k.roundtrip()
}

// Chord presses code with mod held, as a person types Shift+Up: mod
// down, code pressed and released, mod up. The driver reads a modifier
// from its key events, so mod goes as a key of its own, not only as
// modifier state.
func (k *Keyboard) Chord(mod, code uint32) error {
	if err := k.kb.Key(k.tick(), mod, statePressed); err != nil {
		return err
	}
	if err := k.Key(code); err != nil {
		return err
	}
	if err := k.kb.Key(k.tick(), mod, stateReleased); err != nil {
		return err
	}
	return k.roundtrip()
}

// Hold presses a key, keeps it down for d, and releases it — long enough,
// past the compositor's repeat delay, for the client to repeat it.
func (k *Keyboard) Hold(code uint32, d time.Duration) error {
	if err := k.kb.Key(k.tick(), code, statePressed); err != nil {
		return err
	}
	if err := k.roundtrip(); err != nil {
		return err
	}
	time.Sleep(d)
	k.timeMS += uint32(d.Milliseconds())
	if err := k.kb.Key(k.tick(), code, stateReleased); err != nil {
		return err
	}
	return k.roundtrip()
}

// Type sends the keys that produce s on a US layout, shift included, as
// both the key and the modifier.
// Characters the layout cannot produce are an error rather than a silent
// omission: a test that thought it typed a password and typed "pssword"
// would fail in a way nobody could read.
func (k *Keyboard) Type(s string) error {
	for _, r := range s {
		code, shift, ok := textinput.Scancode(r)
		if !ok {
			return fmt.Errorf("no US-layout key produces %q", r)
		}
		if shift {
			if err := k.kb.Key(k.tick(), keyLeftShift, statePressed); err != nil {
				return err
			}
			if err := k.kb.Modifiers(modShift, 0, 0, 0); err != nil {
				return err
			}
		}
		if err := k.Key(code); err != nil {
			return err
		}
		if shift {
			if err := k.kb.Key(k.tick(), keyLeftShift, stateReleased); err != nil {
				return err
			}
			if err := k.kb.Modifiers(0, 0, 0, 0); err != nil {
				return err
			}
		}
	}
	return k.roundtrip()
}

// Close destroys the virtual keyboard, which takes the seat's keyboard
// capability with it.
func (k *Keyboard) Close() {
	if k.kb != nil {
		k.kb.Destroy()
		k.roundtrip()
	}
	k.display.Context().Close()
}
