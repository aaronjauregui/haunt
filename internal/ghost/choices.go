package ghost

import (
	"image"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/sprite"
	"github.com/aaronjauregui/haunt/internal/textinput"
	"github.com/aaronjauregui/haunt/internal/wlclient"
)

// Choices: a balloon you can answer.
//
// Menus are how classic ukagaka ghosts are mostly *operated*. Without them
// the only ways to address a ghost are clicking it and typing free text at
// it — the first is barely a language and the second assumes an LLM behind
// the balloon. A brain that offers three options is having a conversation
// with anyone, model or no model.
//
// Two ways to answer, deliberately:
//
//   - The pointer. The menu balloon takes input rather than passing it
//     through (the only surface here that does besides the ghost), and a
//     click lands on whichever row it is over.
//   - The keyboard. Digits pick a row, arrows move, Enter takes the
//     highlighted one, Escape dismisses. This is the first interaction in
//     the project that does not require a mouse, which the accessibility
//     hole in DESIGN.md has been waiting for.
//
// Dismissal is an answer too. A brain that asked a question and heard
// nothing back would wait forever, so clicking the ghost, pressing Escape
// or losing the keyboard all send an EventChoice with an empty Name.

// choiceState is an open menu.
type choiceState struct {
	open    bool
	text    string
	options []string
	tag     string
	// rows are the hit rectangles, in the panel's own pixels.
	rows []image.Rectangle
	// scale is the renderer scale the rows were measured at, since clicks
	// arrive in logical coordinates.
	scale float64
	// highlight is the row the keyboard is on, -1 for none. The pointer
	// does not move it: a menu that reshuffled itself under the cursor
	// would be a menu you cannot trust.
	highlight int
	// focused says the compositor has given *this* menu the keyboard. A
	// leave that arrives before the matching enter belongs to whatever held
	// it before — see onKeyboardFocus.
	focused bool
	surface *wlclient.SpriteSurface
	// expiry takes an unanswered question down after a while.
	expiry *time.Timer
}

// showChoices puts a question on screen. It replaces whatever balloon was
// there, because a question is what the ghost is saying now.
func (g *Ghost) showChoices(in proto.Intent) {
	if muted, why := g.quiet.Active(); muted {
		g.log.Debug(hlog.Balloon, "choices suppressed (%s)", why)
		return
	}
	// A menu is a question, and a question over a half-typed line is two
	// conversations at once.
	g.closeInput()
	g.clearPaging()

	options := make([]string, 0, len(in.Choices))
	for _, c := range in.Choices {
		options = append(options, strings.TrimSpace(c))
	}

	g.choiceMu.Lock()
	g.choice = choiceState{
		open:      true,
		text:      in.Text,
		options:   options,
		tag:       in.Tag,
		highlight: -1,
		focused:   false,
	}
	g.choiceMu.Unlock()

	// A question that stayed up forever would be a modal dialog, which is
	// the opposite of what a companion should be. The brain hears a
	// dismissal and can ask again if it still cares.
	expiry := time.AfterFunc(choiceTimeout, func() {
		g.guard(hlog.Balloon, "the choice timeout", func() {
			g.log.Debug(hlog.Balloon, "choices expired unanswered")
			g.dismissChoices()
		})
	})
	g.choiceMu.Lock()
	g.choice.expiry = expiry
	g.choiceMu.Unlock()

	g.drawChoices()
	// The keyboard, so the digits work. Taken the same way the input line
	// takes it: on demand, and given back the moment the menu closes.
	g.takeKeyboard(true)
	g.log.Debug(hlog.Balloon, "choices offered: %s", strings.Join(options, " | "))
}

// drawChoices paints the menu and remembers where its rows are.
func (g *Ghost) drawChoices() {
	g.choiceMu.Lock()
	if !g.choice.open {
		g.choiceMu.Unlock()
		return
	}
	text, options, highlight := g.choice.text, g.choice.options, g.choice.highlight
	g.choiceMu.Unlock()

	rend := g.renderer()
	if rend == nil {
		return
	}
	sp, rows := rend.RenderChoices(text, options, highlight)

	g.choiceMu.Lock()
	g.choice.rows = rows
	g.choice.scale = rend.Scale()
	g.choiceMu.Unlock()

	g.showChoiceSprite(sp)
}

// showChoiceSprite puts the menu panel on screen. Unlike every other
// balloon it takes pointer input, and unlike the ghost it does not move
// when dragged: a menu that slid away as you reached for an option would be
// a cruel joke.
func (g *Ghost) showChoiceSprite(sp *sprite.Sprite) {
	g.hideBalloon()
	// A question is a line like any other as far as the balloon's
	// bookkeeping goes: an older line's linger must not take it away.
	g.blnMu.Lock()
	g.blnGen++
	g.blnUp = true
	g.blnMu.Unlock()
	if g.surface == nil || g.app == nil {
		return // tests drive the state machine without a screen
	}
	x, y := g.balloonPos()
	w, h := sp.Size()
	lw, lh := g.renderer().LogicalSize(w, h)
	bln, err := g.app.CreateSpriteOpts(sp, x, y, wlclient.SurfaceOptions{
		Fixed:    true,
		LogicalW: int32(lw),
		LogicalH: int32(lh),
	})
	if err != nil {
		g.log.Warn(hlog.Balloon, "choice balloon failed: %v", err)
		return
	}
	bln.OnClick = g.onChoiceClick
	g.choiceMu.Lock()
	g.choice.surface = bln
	g.choiceMu.Unlock()

	g.blnMu.Lock()
	g.bln = bln
	g.blnWaiting = false
	g.blnMu.Unlock()
}

// onChoiceClick answers with whichever row was under the pointer. A click
// on the panel but not on a row is ignored rather than treated as a
// dismissal: the user was aiming at something.
func (g *Ghost) onChoiceClick(x, y int) {
	g.choiceMu.Lock()
	if !g.choice.open {
		g.choiceMu.Unlock()
		return
	}
	scale := g.choice.scale
	if scale <= 0 {
		scale = 1
	}
	// Clicks arrive in logical pixels; the rows were measured in the
	// panel's own, which on a HiDPI screen are larger.
	px, py := int(float64(x)*scale), int(float64(y)*scale)
	picked := -1
	for i, r := range g.choice.rows {
		if (image.Point{px, py}).In(r) {
			picked = i
			break
		}
	}
	g.choiceMu.Unlock()
	if picked < 0 {
		return
	}
	g.answerChoice(picked)
}

// onChoiceKey handles the keyboard while a menu is open, reporting whether
// it consumed the key.
func (g *Ghost) onChoiceKey(code uint32, down bool) bool {
	if !g.choiceOpen() {
		return false
	}
	if !down {
		return true // the menu owns the keyboard while it is up
	}
	switch code {
	case textinput.KeyEsc:
		g.dismissChoices()
	case textinput.KeyEnter, textinput.KeyKPEnter:
		g.choiceMu.Lock()
		at := g.choice.highlight
		g.choiceMu.Unlock()
		if at >= 0 {
			g.answerChoice(at)
		}
	case keyUp:
		g.moveHighlight(-1)
	case keyDown:
		g.moveHighlight(1)
	default:
		if n, ok := digit(code); ok {
			g.choiceMu.Lock()
			count := len(g.choice.options)
			g.choiceMu.Unlock()
			if n >= 1 && n <= count {
				g.answerChoice(n - 1)
			}
		}
	}
	return true
}

// Arrow scancodes, which internal/textinput has no reason to name: they
// produce no character.
const (
	keyUp   = 103
	keyDown = 108
)

// digit maps a number-row scancode to the digit on it.
func digit(code uint32) (int, bool) {
	if code >= 2 && code <= 10 { // 1..9
		return int(code) - 1, true
	}
	return 0, false
}

// moveHighlight walks the selection, wrapping, and starts at the top when
// nothing is selected yet.
func (g *Ghost) moveHighlight(by int) {
	g.choiceMu.Lock()
	n := len(g.choice.options)
	if n == 0 {
		g.choiceMu.Unlock()
		return
	}
	switch {
	case g.choice.highlight < 0 && by > 0:
		g.choice.highlight = 0
	case g.choice.highlight < 0:
		g.choice.highlight = n - 1
	default:
		g.choice.highlight = ((g.choice.highlight+by)%n + n) % n
	}
	g.choiceMu.Unlock()
	g.drawChoices()
}

// answerChoice publishes the pick and takes the menu down.
func (g *Ghost) answerChoice(i int) {
	g.choiceMu.Lock()
	if !g.choice.open || i < 0 || i >= len(g.choice.options) {
		g.choiceMu.Unlock()
		return
	}
	picked, tag := g.choice.options[i], g.choice.tag
	g.choiceMu.Unlock()

	g.closeChoices()
	g.log.Debug(hlog.Balloon, "choice taken: %q", picked)
	// Through publishExternal: choosing is a person acting, so what the
	// ghost does next is a response rather than an interruption.
	g.publishExternal(proto.Event{Type: proto.EventChoice, Name: picked, Tag: tag})
}

// dismissChoices closes the menu without an answer — and says so, because a
// brain waiting on a question has to hear that nobody answered it.
func (g *Ghost) dismissChoices() {
	if !g.choiceOpen() {
		return
	}
	g.choiceMu.Lock()
	tag := g.choice.tag
	g.choiceMu.Unlock()

	g.closeChoices()
	g.log.Debug(hlog.Balloon, "choices dismissed")
	g.publishExternal(proto.Event{Type: proto.EventChoice, Tag: tag})
}

// choiceFocus records the compositor handing the keyboard to a menu or
// taking it away, reporting whether a loss should end the menu.
//
// The subtlety is the same one the input line hit, and the comment that
// used to be here claimed a menu could not hit it — wrongly. Opening a menu
// asks for the keyboard, and the compositor answers a *previous* holder's
// release with a leave that can arrive after the new menu is already up:
// offered, leave, dismissed, enter, all inside a few milliseconds, which
// from the outside is a menu that never appears at all. A leave only counts
// once this menu has actually been given focus.
func (g *Ghost) choiceFocus(focused bool) (endsIt bool) {
	g.choiceMu.Lock()
	defer g.choiceMu.Unlock()
	if !g.choice.open {
		return false
	}
	if focused {
		g.choice.focused = true
		return false
	}
	return g.choice.focused
}

// closeChoices takes the menu off screen and gives the keyboard back.
func (g *Ghost) closeChoices() {
	g.choiceMu.Lock()
	if !g.choice.open {
		g.choiceMu.Unlock()
		return
	}
	if g.choice.expiry != nil {
		g.choice.expiry.Stop()
	}
	g.choice = choiceState{highlight: -1}
	g.choiceMu.Unlock()

	g.takeKeyboard(false)
	g.hideBalloon()
}

// choiceOpen reports whether a menu is waiting for an answer.
func (g *Ghost) choiceOpen() bool {
	g.choiceMu.Lock()
	defer g.choiceMu.Unlock()
	return g.choice.open
}

// choiceHighlight is the selected row, for tests and for the log.
func (g *Ghost) choiceHighlight() int {
	g.choiceMu.Lock()
	defer g.choiceMu.Unlock()
	return g.choice.highlight
}

// choiceTimeout is how long a question stays up with no answer. A menu that
// hung around forever would be a modal dialog, which is the opposite of
// what a desktop companion should be; the brain hears a dismissal and can
// ask again if it still cares.
const choiceTimeout = 2 * time.Minute
