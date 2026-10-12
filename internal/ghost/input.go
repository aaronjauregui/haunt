package ghost

import (
	"errors"
	"time"

	"github.com/aaronjauregui/haunt/internal/balloon"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/sprite"
	"github.com/aaronjauregui/haunt/internal/textinput"
	"github.com/aaronjauregui/haunt/internal/wlclient"
)

// Talking to your ghost without a terminal. Right-clicking the sprite opens
// a line in its balloon; Enter sends it as a message event — the same event
// `haunt say` produces, so brains need no new handling — and Escape, or
// losing keyboard focus, abandons it. A line opened this way is a
// conversation (conversation.go): Enter sends and the line opens again,
// until the person or the ghost ends it. A line the driver opens for an
// answer of its own closes on Enter.
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
	// held is the last line the ghost finished while this one was open,
	// shown when it closes: a reply that lands while the person is typing
	// their next thought is still an answer to their last one. heldShown,
	// if set, is told when it is.
	held      string
	heldShown func()
	// top is the first row of the box shown when the text has more rows
	// than it grows to, kept between draws so the box scrolls only as
	// far as the caret needs (balloon.InputBox.Top).
	top int
}

// openInput starts a conversation, unless a line is already open.
func (g *Ghost) openInput() { g.openInputWithReply(nil) }

// openInputWithReply is openInput with somewhere for the answer to go,
// reporting whether the line was opened at all. With nowhere, the line is
// the person's and opens a conversation. A question put while a
// conversation holds the line takes the line's next Enter instead of
// opening one of its own: the person is already typing there.
func (g *Ghost) openInputWithReply(reply chan string) bool {
	conv := g.inConversation()
	g.inputMu.Lock()
	if g.input.open {
		adopted := conv && reply != nil && g.input.reply == nil
		if adopted {
			g.input.reply = reply
		}
		g.inputMu.Unlock()
		return adopted
	}
	if reply == nil {
		g.startConversation()
	}
	g.input.reply = reply
	g.input.open = true
	g.input.caret = true
	g.input.focused = false
	g.input.editor.Reset()
	g.input.top = 0
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
// turned away says no. Closing a conversation's line ends the
// conversation, whoever closes it: a click on the ghost, a menu. It
// reports whether it put up a line held while the line was open.
func (g *Ghost) closeInputWith(answer string) bool {
	if g.inConversation() {
		g.endConversation("the line was closed")
		return false
	}
	g.inputMu.Lock()
	if !g.input.open {
		g.inputMu.Unlock()
		return false
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
	held, shown := g.input.held, g.input.heldShown
	g.input.held, g.input.heldShown = "", nil
	g.inputMu.Unlock()
	g.log.Debug(hlog.Input, "input line closed")

	g.takeKeyboard(false)
	// After any draw already under way: one that read the line as open
	// before it closed would otherwise put the panel back up once it has
	// been taken down.
	g.drawMu.Lock()
	g.hideBalloon()
	g.drawMu.Unlock()
	if held == "" {
		return false
	}
	g.showPaged(held)
	g.lingerBalloon(g.balloonGen(), heldLinger)
	if shown != nil && g.blnShowing() {
		shown()
	}
	return true
}

// heldLinger is how long a line held back by the input line stays once it
// is shown: as long as a reply.
const heldLinger = 12 * time.Second

// holdLine keeps text for when the open line closes, reporting whether
// there was a line to wait for. A newer line replaces an older one, whose
// shown is never called: only the latest word is worth the wait.
//
// In a conversation nothing is held: the line is shown at once above the
// open line, and shown is told so now.
func (g *Ghost) holdLine(text string, shown func()) bool {
	if g.sayInConversation(text) {
		if shown != nil {
			shown()
		}
		return true
	}
	g.inputMu.Lock()
	defer g.inputMu.Unlock()
	if !g.input.open {
		return false
	}
	g.input.held, g.input.heldShown = text, shown
	g.log.Debug(hlog.Balloon, "line held until the input line closes")
	return true
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
	g.abandonLine("the keyboard went elsewhere")
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
// reply is never billed to the ghost's curiosity budget. In a
// conversation the line then opens again empty; a message sent while the
// ghost is still answering the last queues behind it, as `haunt say`
// does. A backslash before Enter is a new line instead (textinput), and
// what is sent keeps its newlines. In a conversation PageUp and PageDown
// scroll the view a page, and Shift+Up and Shift+Down as far as a step of
// the wheel; on a line of the driver's they do nothing.
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
	if action != textinput.ActionSubmit && action != textinput.ActionCancel {
		g.touchConversation()
	}

	switch action {
	case textinput.ActionPageBack, textinput.ActionPageForward:
		if g.inConversation() {
			page := g.conversationPage()
			if action == textinput.ActionPageBack {
				page = -page
			}
			g.scrollConversationLines(page)
		}
		return
	case textinput.ActionScrollBack:
		if g.inConversation() {
			g.scrollConversationLines(-conversationScroll)
		}
		return
	case textinput.ActionScrollForward:
		if g.inConversation() {
			g.scrollConversationLines(conversationScroll)
		}
		return
	case textinput.ActionCancel:
		g.abandonLine("escape")
		return
	case textinput.ActionSubmit:
		if g.inConversation() {
			g.submitInConversation(text, empty)
			return
		}
		g.inputMu.Lock()
		driverOwned := g.input.reply != nil
		g.inputMu.Unlock()
		if driverOwned {
			g.closeInputWith(text)
			return
		}
		g.closeInput()
		if !empty {
			g.remember(recalled{you: true, text: text})
			g.publish(proto.Event{Type: proto.EventMessage, Text: text})
		}
		return
	}
	g.drawInput()
}

// submitInConversation is Enter in a conversation: what was typed goes
// where it is waited for, and the line opens again empty with the
// keyboard still held. A question of the driver's that took the line has
// its answer, as it would from a line of its own; anything else is a
// message. Enter on an empty line does nothing.
func (g *Ghost) submitInConversation(text string, empty bool) {
	g.inputMu.Lock()
	reply := g.input.reply
	g.input.reply = nil
	g.input.editor.Reset()
	g.input.top = 0
	g.inputMu.Unlock()
	g.touchConversation()
	switch {
	case reply != nil:
		reply <- text
		// The question was the status line, and it has its answer.
		g.statusInConversation("")
		return
	case !empty:
		// Into the view at once, before the ghost has answered: remember
		// draws it.
		g.remember(recalled{you: true, text: text})
		g.publish(proto.Event{Type: proto.EventMessage, Text: text})
		return
	}
	g.drawInput()
}

// keyRepeats says which held keys repeat. In a menu only the arrows do: a
// held digit picks its row once, and must not go on to type into whatever
// the answer opens next. In a line, the editing and moving keys and
// anything that types (textinput.Repeats).
func (g *Ghost) keyRepeats(code uint32) bool {
	if g.choiceOpen() {
		return code == textinput.KeyUp || code == textinput.KeyDown
	}
	return g.inputOpen() && textinput.Repeats(code)
}

// drawInput repaints the input balloon from current state.
func (g *Ghost) drawInput() {
	// One at a time: the keys, the caret and the ghost's words all draw
	// this panel from goroutines of their own, and a font face is not
	// safe for two at once.
	g.drawMu.Lock()
	defer g.drawMu.Unlock()
	g.inputMu.Lock()
	open := g.input.open
	in := balloon.InputBox{Text: g.input.editor.Text(), Cursor: g.input.editor.Cursor(),
		Caret: g.input.caret, Top: g.input.top}
	g.inputMu.Unlock()
	rend := g.renderer()
	if !open || rend == nil {
		return
	}
	var sp *sprite.Sprite
	var top int
	conv := g.inConversation()
	if conv {
		sp, top = g.conversationView(rend, in)
	} else {
		sp, top = rend.RenderInput(in)
	}
	g.inputMu.Lock()
	g.input.top = top
	g.inputMu.Unlock()

	// Repaint the existing panel rather than swapping surfaces: an input
	// line redraws on every keystroke and twice a second for the caret, and
	// destroying the surface each time makes it flicker. RenderInput keeps
	// a fixed width precisely so this stays possible, growing only a row
	// at a time as the text does, and so does RenderConversation, which
	// only grows taller until it reaches its cap: past that, streaming
	// words and the wheel redraw in place too.
	//
	// Only a panel of the right kind is repainted: the conversation's takes
	// the wheel and every other balloon lets the pointer through, so a
	// balloon from before the conversation that happens to be its size is
	// replaced, not reused.
	g.blnMu.Lock()
	bln := g.bln
	g.blnMu.Unlock()
	if bln != nil && (bln.OnScroll != nil) == conv {
		if err := bln.Redraw(sp); err == nil {
			return
		} else if !errors.Is(err, wlclient.ErrSizeChanged) {
			g.log.Warn(hlog.Input, "redraw: %v", err)
		}
	}
	if conv {
		// The wheel over the conversation scrolls it, as over the ghost.
		g.showBalloonSpriteScroll(sp, g.scrollConversationPanel)
		return
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
