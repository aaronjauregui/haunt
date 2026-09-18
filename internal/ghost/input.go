package ghost

import (
	"errors"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/textinput"
	"github.com/aaronjauregui/haunt/internal/wlclient"
)

// Talking to your ghost without a terminal. Right-clicking the sprite opens
// a line in its balloon; Enter sends it as a message event — the same event
// `haunt say` produces, so brains need no new handling — and Escape, or
// losing keyboard focus, abandons it.
//
// The ghost holds keyboard focus only while the line is open (layer-shell
// on-demand interactivity). A desktop companion that grabbed the keyboard
// whenever it felt like it would be a menace.

// caretBlink is the caret's on/off period.
const caretBlink = 500 * time.Millisecond

// inputState is the open line, if any.
type inputState struct {
	editor textinput.Editor
	open   bool
	caret  bool
	stop   chan struct{}
	// focused says the compositor has given *this* line the keyboard.
	// Losing focus is how a line is abandoned, but a leave event that
	// arrives before the matching enter belongs to the previous line, not
	// this one — see onKeyboardFocus.
	focused bool
	// reply, when set, is where what was typed goes instead of to the
	// brain. The same asymmetry g.ask has for menus: some questions are
	// the driver's own, and a ghost has no business overhearing the answer
	// to a question about what it has got wrong. Buffered by the caller, so
	// closing a line never blocks on a listener that gave up.
	reply chan string
}

// openInput starts a line of input, unless one is already open.
func (g *Ghost) openInput() { g.openInputWithReply(nil) }

// openInputWithReply is openInput with somewhere for the answer to go,
// reporting whether the line was opened at all.
func (g *Ghost) openInputWithReply(reply chan string) bool {
	g.inputMu.Lock()
	if g.input.open {
		g.inputMu.Unlock()
		return false
	}
	g.input.reply = reply
	g.input.open = true
	g.input.caret = true
	g.input.focused = false
	g.input.editor.Reset()
	g.input.stop = make(chan struct{})
	stop := g.input.stop
	g.inputMu.Unlock()

	g.takeKeyboard(true)
	g.log.Debug(hlog.Input, "input line opened")
	g.drawInput()

	// Blink on its own timer: a caret that only moves when you type looks
	// like a frozen ghost.
	go func() {
		defer g.recoverPanic(hlog.Input, "the caret")
		t := time.NewTicker(caretBlink)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-g.done:
				return
			case <-t.C:
				g.inputMu.Lock()
				g.input.caret = !g.input.caret
				open := g.input.open
				g.inputMu.Unlock()
				if !open {
					return
				}
				g.drawInput()
			}
		}
	}()
	return true
}

// closeInput ends the line, releasing the keyboard.
func (g *Ghost) closeInput() { g.closeInputWith("") }

// closeInputWith closes the line, handing answer to whoever is waiting for
// it. Abandoning a line closes it with nothing, which is how a person who
// turned away says no.
func (g *Ghost) closeInputWith(answer string) {
	g.inputMu.Lock()
	if !g.input.open {
		g.inputMu.Unlock()
		return
	}
	g.input.open = false
	g.input.focused = false
	close(g.input.stop)
	// Whoever is waiting is told, even when the line was abandoned: an
	// empty answer is an answer, and a caller blocked on a person who
	// walked away would wait for the session.
	if g.input.reply != nil {
		g.input.reply <- answer
		g.input.reply = nil
	}
	g.input.editor.Reset()
	g.inputMu.Unlock()
	g.log.Debug(hlog.Input, "input line closed")

	g.takeKeyboard(false)
	g.hideBalloon()
}

// onKeyboardFocus reacts to the compositor handing the keyboard over or
// taking it away. Losing it abandons the line: the user's attention has
// moved on, and a line nobody can type into is a lie.
//
// The subtlety is *whose* loss it is. Closing a line releases keyboard
// interactivity, which makes the compositor send a leave — and that leave
// can arrive after the user has already right-clicked to open the next
// line, because the round trip is not instant and a person with a mouse is
// quicker than one might think. Acting on it then would tear down a line
// opened a millisecond earlier, so a leave only counts once this line has
// actually been given focus. Observed in a nested sway: open, click away,
// open again, and the second line vanished the instant it appeared.
func (g *Ghost) onKeyboardFocus(focused bool) {
	// A menu is dismissed by attention moving elsewhere, like a line — and
	// it has the same stale-leave problem, for the same reason: taking the
	// keyboard makes the compositor release whoever held it before, and
	// that leave can arrive after this menu is already up.
	if g.choiceOpen() {
		if g.choiceFocus(focused) {
			g.dismissChoices()
		}
		return
	}
	g.inputMu.Lock()
	open := g.input.open
	if focused {
		g.input.focused = true
	}
	stale := !focused && !g.input.focused
	g.inputMu.Unlock()

	if !open || focused {
		return
	}
	if stale {
		g.log.Debug(hlog.Input, "ignoring a keyboard leave for the previous line")
		return
	}
	g.closeInput()
}

// takeKeyboard asks the compositor for (or gives up) keyboard focus. The
// nil check is for tests, which drive the input state machine without a
// compositor to hand focus over.
func (g *Ghost) takeKeyboard(want bool) {
	if g.app == nil || g.surface == nil {
		return
	}
	if err := g.app.TakeKeyboard(g.surface, want); err != nil {
		g.log.Warn(hlog.Input, "keyboard focus: %v", err)
	}
}

// onKey feeds a scancode to the open line. Enter publishes what was typed
// as a message — indistinguishable from `haunt say`, and prompted, so the
// reply is never billed to the ghost's curiosity budget.
func (g *Ghost) onKey(code uint32, down bool) {
	// A menu holds the keyboard while it is up: digits pick a row, arrows
	// move, Enter takes it, Escape dismisses.
	if g.onChoiceKey(code, down) {
		return
	}
	g.inputMu.Lock()
	if !g.input.open {
		g.inputMu.Unlock()
		return
	}
	action := g.input.editor.Key(code, down)
	text := g.input.editor.Text()
	empty := g.input.editor.Empty()
	g.inputMu.Unlock()

	switch action {
	case textinput.ActionCancel:
		g.closeInput()
		return
	case textinput.ActionSubmit:
		g.inputMu.Lock()
		driverOwned := g.input.reply != nil
		g.inputMu.Unlock()
		if driverOwned {
			g.closeInputWith(text)
			return
		}
		g.closeInput()
		if !empty {
			g.publish(proto.Event{Type: proto.EventMessage, Text: text})
		}
		return
	}
	g.drawInput()
}

// drawInput repaints the input balloon from current state.
func (g *Ghost) drawInput() {
	g.inputMu.Lock()
	open, text, caret := g.input.open, g.input.editor.Text(), g.input.caret
	g.inputMu.Unlock()
	rend := g.renderer()
	if !open || rend == nil {
		return
	}
	sp := rend.RenderInput(text, caret)

	// Repaint the existing panel rather than swapping surfaces: an input
	// line redraws on every keystroke and twice a second for the caret, and
	// destroying the surface each time makes it flicker. RenderInput keeps
	// a fixed width precisely so this stays possible.
	g.blnMu.Lock()
	bln := g.bln
	g.blnMu.Unlock()
	if bln != nil {
		if err := bln.Redraw(sp); err == nil {
			return
		} else if !errors.Is(err, wlclient.ErrSizeChanged) {
			g.log.Warn(hlog.Input, "redraw: %v", err)
		}
	}
	g.showBalloonSprite(sp)
}

// inputOpen reports whether a line is being typed, so balloon updates from
// the brain do not paint over it.
func (g *Ghost) inputOpen() bool {
	g.inputMu.Lock()
	defer g.inputMu.Unlock()
	return g.input.open
}
