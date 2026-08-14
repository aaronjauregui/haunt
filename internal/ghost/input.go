package ghost

import (
	"fmt"
	"time"

	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/textinput"
)

// Talking to your ghost without a terminal. Clicking the sprite opens a
// line in its balloon; Enter sends it as a message event — the same event
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
}

// openInput starts a line of input, unless one is already open.
func (g *Ghost) openInput() {
	g.inputMu.Lock()
	if g.input.open {
		g.inputMu.Unlock()
		return
	}
	g.input.open = true
	g.input.caret = true
	g.input.editor.Reset()
	g.input.stop = make(chan struct{})
	stop := g.input.stop
	g.inputMu.Unlock()

	g.takeKeyboard(true)
	g.drawInput()

	// Blink on its own timer: a caret that only moves when you type looks
	// like a frozen ghost.
	go func() {
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
}

// closeInput ends the line, releasing the keyboard.
func (g *Ghost) closeInput() {
	g.inputMu.Lock()
	if !g.input.open {
		g.inputMu.Unlock()
		return
	}
	g.input.open = false
	close(g.input.stop)
	g.input.editor.Reset()
	g.inputMu.Unlock()

	g.takeKeyboard(false)
	g.hideBalloon()
}

// takeKeyboard asks the compositor for (or gives up) keyboard focus. The
// nil check is for tests, which drive the input state machine without a
// compositor to hand focus over.
func (g *Ghost) takeKeyboard(want bool) {
	if g.app == nil || g.surface == nil {
		return
	}
	if err := g.app.TakeKeyboard(g.surface, want); err != nil {
		g.opts.Log(fmt.Sprintf("haunt: input: keyboard focus: %v", err))
	}
}

// onKey feeds a scancode to the open line. Enter publishes what was typed
// as a message — indistinguishable from `haunt say`, and prompted, so the
// reply is never billed to the ghost's curiosity budget.
func (g *Ghost) onKey(code uint32, down bool) {
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
	if !open || g.blnRend == nil {
		return
	}
	g.showBalloonSprite(g.blnRend.RenderInput(text, caret))
}

// inputOpen reports whether a line is being typed, so balloon updates from
// the brain do not paint over it.
func (g *Ghost) inputOpen() bool {
	g.inputMu.Lock()
	defer g.inputMu.Unlock()
	return g.input.open
}
