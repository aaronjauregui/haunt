package ghost

import (
	"image"
	"time"

	"github.com/aaronjauregui/haunt/internal/balloon"
	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/sprite"
)

// Conversation. Typing at the ghost opens a conversation: a mode of the
// balloon, not a window. Enter sends the line and it opens again empty,
// with the keyboard still held; above it, in a wider panel, is what was
// said lately, the person's lines and the ghost's and its pictures, with
// the ghost's words streaming in at the bottom. The wheel over the ghost
// or over the panel goes back through it, as do PageUp, PageDown and
// Shift with Up or Down; scrolled back, the view holds its place as new
// words arrive, with a mark beneath it until it is brought back to the
// bottom, and the person's own next line brings it back. Nothing on the
// balloon expires while it lasts. A click on the ghost is still the
// ghost's, and leaves the conversation be. It ends on Escape, on the
// keyboard going elsewhere (a click away), on a quiet spell
// with nothing typed and nothing said, or when the ghost says goodbye
// with end_conversation, which the person can switch off
// ([conversation] ghost_may_end). Once it ends, the last words linger as
// a reply would.
//
// A line the driver opens for its own question (ask_text, a consent
// question) is not a conversation: it closes on Enter, as it always has.
//
// The model is told twice, both in the prompt: rule 8 is rendered for the
// mode (the persona's length, up to a short paragraph when something
// needs it, instead of under 25 words) and the turn context
// says "In conversation: yes." Rule 8 sits in part 1, so the cached
// prefix changes once when a conversation opens and once when it ends.

// conversationIdle is how long a conversation stays open with nothing
// typed and nothing said: long enough to think, short enough that a line
// left open when the person walked away does not hold the keyboard all
// afternoon.
const conversationIdle = 3 * time.Minute

// The conversation panel: wider than a remark's, for a few sentences, and
// a little taller.
const (
	conversationWidth  = 480
	conversationHeight = 520
)

// conversation is the mode's state, under convMu.
type conversation struct {
	open  bool
	since time.Time
	// last is when something was last typed or said, which the idle
	// timer measures from.
	last time.Time
	idle *time.Timer
	// idleAfter replaces conversationIdle when set, for tests.
	idleAfter time.Duration
	// rend is the conversation's renderer, kept for the scale it was
	// built at.
	rend *balloon.Renderer
	// said is the last line the ghost finished saying, which stays up
	// when the conversation ends. Only sayInConversation sets it: words
	// still streaming may yet be taken back, and are not the ghost's last
	// words until they are said.
	said string
	// streaming is the ghost's words still arriving, shown after what has
	// been said; empty once they are said, or taken back.
	streaming string
	// status is a line of the driver's own (statusInConversation), drawn
	// beneath the rest until the ghost next says something and never
	// remembered: it is the driver reporting, not the ghost speaking.
	status string
	// waitStatus is the status line the waiting indicator last put up
	// (waitInConversation), which it may tick and take down; any other
	// status line is not its to touch.
	waitStatus string
	// offset is how many lines the view is scrolled back from the bottom;
	// lines is how many there were at the last draw, and room how many
	// the view had room for, to clamp it with.
	offset, lines, room int
	// pics are the pictures in the view, decoded once.
	pics map[string]image.Image
	// ending says the ghost called end_conversation this turn: the mode
	// closes once the turn's words have been shown.
	ending bool
	// noGhostEnd is [conversation] ghost_may_end = false.
	noGhostEnd bool
}

// applyConversation takes the [conversation] section.
func (g *Ghost) applyConversation(c config.ConversationConfig) {
	g.convMu.Lock()
	g.conv.noGhostEnd = !c.MayEnd()
	g.convMu.Unlock()
}

// ghostMayEnd is whether end_conversation is offered.
func (g *Ghost) ghostMayEnd() bool {
	g.convMu.Lock()
	defer g.convMu.Unlock()
	return !g.conv.noGhostEnd
}

// inConversation is whether a conversation is open.
func (g *Ghost) inConversation() bool {
	g.convMu.Lock()
	defer g.convMu.Unlock()
	return g.conv.open
}

// startConversation opens the mode, if it is not open already.
func (g *Ghost) startConversation() {
	// A page of a reply from before is not the conversation's to turn.
	g.clearPaging()
	g.convMu.Lock()
	defer g.convMu.Unlock()
	if g.conv.open {
		return
	}
	now := time.Now()
	g.conv.open, g.conv.since, g.conv.last = true, now, now
	g.conv.said, g.conv.streaming, g.conv.status, g.conv.ending = "", "", "", false
	g.conv.offset, g.conv.lines, g.conv.room, g.conv.pics = 0, 0, 0, nil
	d := g.conv.idleAfter
	if d <= 0 {
		d = conversationIdle
	}
	g.conv.idle = time.AfterFunc(d, g.conversationIdled)
	g.log.Debug(hlog.Input, "conversation opened")
}

// touchConversation notes that something was typed or said, which keeps
// the conversation from going quiet.
func (g *Ghost) touchConversation() {
	g.convMu.Lock()
	if g.conv.open {
		g.conv.last = time.Now()
	}
	g.convMu.Unlock()
}

// conversationIdled is the idle timer: the conversation ends if nothing
// has happened for the whole of the idle time, and the timer is set again
// for the rest of it otherwise.
func (g *Ghost) conversationIdled() {
	g.guard(hlog.Input, "the conversation's idle timer", func() {
		select {
		case <-g.done:
			return
		default:
		}
		g.convMu.Lock()
		if !g.conv.open {
			g.convMu.Unlock()
			return
		}
		d := g.conv.idleAfter
		if d <= 0 {
			d = conversationIdle
		}
		if left := d - time.Since(g.conv.last); left > 0 {
			g.conv.idle.Reset(left)
			g.convMu.Unlock()
			return
		}
		g.convMu.Unlock()
		g.endConversation("nothing was said for a while")
	})
}

// endConversation closes the mode, why being for the log: the line closes
// and the keyboard is let go, and the ghost's last words stay up for as
// long as a reply would, from now.
func (g *Ghost) endConversation(why string) {
	g.convMu.Lock()
	if !g.conv.open {
		g.convMu.Unlock()
		return
	}
	g.conv.open, g.conv.ending = false, false
	if g.conv.idle != nil {
		g.conv.idle.Stop()
		g.conv.idle = nil
	}
	words, rend := g.conv.said, g.conv.rend
	g.conv.said, g.conv.streaming, g.conv.status, g.conv.pics = "", "", "", nil
	g.convMu.Unlock()
	g.log.Debug(hlog.Input, "conversation ended: %s", why)

	// A reply that landed between the mode closing and the line closing
	// was held, and has just been shown: it is newer than words.
	if g.closeInputWith("") || words == "" || rend == nil {
		return
	}
	if muted, _ := g.quiet.Active(); muted {
		return
	}
	g.drawMu.Lock()
	sp := rend.Render(words)
	g.drawMu.Unlock()
	g.showBalloonSprite(sp)
	g.recallUp(words, g.balloonGen())
	g.lingerBalloon(g.balloonGen(), lingerReply)
}

// abandonLine is the person turning away from the line: the conversation
// ends if there is one, and otherwise the line closes.
func (g *Ghost) abandonLine(why string) {
	if g.inConversation() {
		g.endConversation(why)
		return
	}
	g.closeInput()
}

// endConversationIfAsked ends the conversation at the end of a turn that
// called end_conversation, once its words have been shown: they were the
// goodbye.
func (g *Ghost) endConversationIfAsked() {
	g.convMu.Lock()
	ending := g.conv.open && g.conv.ending
	g.convMu.Unlock()
	if ending {
		g.endConversation("the ghost said goodbye")
	}
}

// conversationRenderer is the renderer for the conversation panel while
// one is open, and base otherwise. It is the balloon's theme made wider,
// built at base's scale and built again when that changes, so a ghost
// dragged to a 200% screen mid-conversation is drawn sharp there too.
func (g *Ghost) conversationRenderer(base *balloon.Renderer) *balloon.Renderer {
	if base == nil {
		return nil
	}
	g.convMu.Lock()
	defer g.convMu.Unlock()
	if !g.conv.open {
		return base
	}
	if r := g.conv.rend; r != nil && r.Scale() == base.Scale() {
		return r
	}
	theme := g.blnTheme
	if theme.MaxWidth == 0 {
		theme = balloon.DefaultTheme()
	}
	theme.MaxWidth, theme.MaxHeight = conversationWidth, conversationHeight
	if g.manifest != nil {
		theme.Speaker = g.manifest.Ghost.Name
	}
	r, err := balloon.NewRendererScaled(theme, base.Scale())
	if err != nil {
		g.log.Warn(hlog.Balloon, "the conversation's balloon: %v", err)
		return base
	}
	g.conv.rend = r
	return r
}

// conversationScroll is how many lines one step of the wheel, or Shift
// with Up or Down, moves the conversation view.
const conversationScroll = 3

// streamInConversation puts words arriving mid-turn at the bottom of the
// view, reporting whether there was a conversation to put them in. Empty
// takes back what had streamed, for a turn that ended up saying nothing,
// which leaves the last words said as the last words. A view scrolled
// back stays where it is (conversationView).
func (g *Ghost) streamInConversation(text string) bool {
	g.convMu.Lock()
	if !g.conv.open {
		g.convMu.Unlock()
		return false
	}
	g.conv.streaming, g.conv.last = text, time.Now()
	if text != "" {
		g.conv.status = ""
	}
	g.convMu.Unlock()
	g.drawInput()
	return true
}

// statusInConversation puts a line of the driver's own ("(looking…)", a
// question's prompt, a tool's report) beneath the view, reporting whether
// there was a conversation to put it in. It is shown, since the person
// should see the ghost looking or what is being asked, but it is not one
// of the ghost's lines: it is not remembered, does not linger when the
// conversation ends, and goes when the ghost next speaks or the person
// answers. Empty takes it down.
func (g *Ghost) statusInConversation(text string) bool {
	g.convMu.Lock()
	if !g.conv.open {
		g.convMu.Unlock()
		return false
	}
	g.conv.status = text
	g.convMu.Unlock()
	g.drawInput()
	return true
}

// dropStatus takes text down if it is still the status line, leaving any
// other that replaced it.
func (g *Ghost) dropStatus(text string) {
	g.convMu.Lock()
	mine := g.conv.open && g.conv.status == text
	g.convMu.Unlock()
	if mine {
		g.statusInConversation("")
	}
}

// sayInConversation is a finished line said during a conversation: shown
// at once in the view rather than held until the line closes, and
// announced and remembered as showPaged would. Remembering it is what puts
// it in the view (remember). It reports whether there was a conversation
// to say it in.
func (g *Ghost) sayInConversation(text string) bool {
	if !g.inputOpen() {
		return false
	}
	g.convMu.Lock()
	if !g.conv.open {
		g.convMu.Unlock()
		return false
	}
	g.conv.said, g.conv.streaming, g.conv.status, g.conv.last = text, "", "", time.Now()
	g.convMu.Unlock()
	g.announce(proto.ActedSpoke, text)
	// Remembering draws the view, and a taller panel is a new balloon:
	// the line is in the one that is up after the draw, which is the one
	// endConversation follows it out of.
	was := g.balloonGen()
	g.remember(recalled{text: text, gen: was})
	g.recallMoved(was, g.balloonGen())
	return true
}

// conversationSaid is something new in the conversation, the person's
// (you) or the ghost's. The person's brings the view back to the bottom:
// they have just spoken, and what comes next is the answer. The ghost's
// leaves a view scrolled back where it is, since the person is reading
// something earlier. It reports whether a conversation is open.
func (g *Ghost) conversationSaid(you bool) bool {
	g.convMu.Lock()
	defer g.convMu.Unlock()
	if g.conv.open {
		g.conv.last = time.Now()
		if you {
			g.conv.offset = 0
		}
		// Something said answers whatever the status line was waiting on.
		g.conv.status = ""
	}
	return g.conv.open
}

// scrollConversation moves the view: back for a negative count of wheel
// steps, forward for a positive one.
func (g *Ghost) scrollConversation(steps int) {
	g.scrollConversationLines(steps * conversationScroll)
}

// scrollConversationPanel is the wheel over the conversation panel
// itself, which scrolls it as the wheel over the ghost does; a panel the
// wheel reaches after the conversation has ended is left alone.
func (g *Ghost) scrollConversationPanel(steps int) {
	if g.inConversation() {
		g.scrollConversation(steps)
	}
}

// scrollConversationLines moves the view n lines, back for a negative n
// and forward for a positive one, clamped so it never goes above the
// first line or below the last.
func (g *Ghost) scrollConversationLines(n int) {
	g.convMu.Lock()
	g.conv.offset = min(max(g.conv.offset-n, 0), g.conversationTop())
	offset := g.conv.offset
	g.convMu.Unlock()
	g.log.Debug(hlog.Balloon, "conversation: %d lines back", offset)
	g.drawInput()
}

// conversationTop is the furthest back the view goes: the offset that
// shows the first line at the top. Called with convMu held.
func (g *Ghost) conversationTop() int {
	return max(g.conv.lines-g.conversationRoom(), 0)
}

// conversationRoom is how many lines the view shows: as many as it had
// room for at the last draw, or as a one-row box leaves before the
// first. Called with convMu held.
func (g *Ghost) conversationRoom() int {
	if g.conv.room <= 0 && g.conv.rend != nil {
		return g.conv.rend.ConversationLines()
	}
	return g.conv.room
}

// conversationPage is how far PageUp and PageDown move the view: what it
// shows, less a line kept from the last page to read on from.
func (g *Ghost) conversationPage() int {
	g.convMu.Lock()
	defer g.convMu.Unlock()
	return max(g.conversationRoom()-1, 1)
}

// clickInConversation is whether a click on the ghost belongs to an open
// conversation's line, which it leaves alone: clicking away is the
// keyboard leaving the ghost, not poking it. A line the driver opened for
// a question of its own is closed by a click as ever.
func (g *Ghost) clickInConversation() bool {
	if !g.inConversation() {
		return false
	}
	g.inputMu.Lock()
	defer g.inputMu.Unlock()
	return g.input.open && g.input.reply == nil
}

// conversationView paints the view with the input box beneath it: what
// has been said, from the recall, and the words still streaming. It
// reports the first row of the box shown. Called from drawInput, with
// drawMu held.
//
// A view scrolled back holds its place: the offset counts lines from the
// bottom, so when lines arrive below (or a status line goes) it moves by
// as many, and the lines on screen stay on screen. At the bottom it
// follows what arrives.
func (g *Ghost) conversationView(rend *balloon.Renderer, in balloon.InputBox) (*sprite.Sprite, int) {
	g.recallMu.Lock()
	said := append([]recalled(nil), g.recall.lines...)
	g.recallMu.Unlock()

	g.convMu.Lock()
	streaming, status, offset, before := g.conv.streaming, g.conv.status, g.conv.offset, g.conv.lines
	if g.conv.pics == nil {
		g.conv.pics = map[string]image.Image{}
	}
	pics := g.conv.pics
	g.convMu.Unlock()

	entries := make([]balloon.Entry, 0, len(said)+2)
	for _, r := range said {
		e := balloon.Entry{You: r.you, Text: r.text, At: r.at}
		if r.picture != "" {
			g.convMu.Lock()
			img, ok := pics[r.picture]
			g.convMu.Unlock()
			if !ok {
				var err error
				if img, err = g.loadPicture(r.picture); err != nil {
					img = nil
				}
				g.convMu.Lock()
				pics[r.picture] = img
				g.convMu.Unlock()
			}
			if img == nil {
				e.Text = "(that picture is gone)"
			} else {
				e.Image = img
			}
		}
		entries = append(entries, e)
	}
	if streaming != "" {
		entries = append(entries, balloon.Entry{Text: streaming, At: time.Now()})
	}
	if status != "" {
		entries = append(entries, balloon.Entry{Text: status, At: time.Now()})
	}
	start := offset
	sp, view := rend.RenderConversation(entries, offset, in)
	if offset > 0 && before > 0 && view.Lines != before {
		held := min(max(offset+view.Lines-before, 0), max(view.Lines-view.Room, 0))
		if held != offset {
			offset = held
			sp, view = rend.RenderConversation(entries, offset, in)
			g.log.Debug(hlog.Balloon, "conversation: held %d lines back", offset)
		}
	}

	g.convMu.Lock()
	g.conv.lines, g.conv.room = view.Lines, view.Room
	// Only if nothing moved it meanwhile: a scroll that landed during the
	// draw is the newer word, and the next draw shows it.
	if g.conv.offset == start {
		g.conv.offset = offset
	}
	g.conv.offset = min(g.conv.offset, g.conversationTop())
	g.convMu.Unlock()
	return sp, view.InputTop
}

// askToEnd is end_conversation: the turn's words become the goodbye.
func (g *Ghost) askToEnd() (string, error) {
	g.convMu.Lock()
	defer g.convMu.Unlock()
	if !g.conv.open {
		return "", refuse("you are not in a conversation")
	}
	g.conv.ending = true
	return "The conversation closes once your words this turn are shown: say goodbye.", nil
}
