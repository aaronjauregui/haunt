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
// the ghost's words streaming in at the bottom, and the wheel over the
// ghost goes back through it; nothing on the balloon expires while it
// lasts. A click on the ghost is still the ghost's, and leaves the
// conversation be. It ends on
// Escape, on the keyboard going elsewhere (a click away), on a quiet spell
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
	// words are the last words the ghost said or is saying, which stay
	// up when the conversation ends.
	words string
	// streaming is the ghost's words still arriving, shown after what has
	// been said; empty once they are said.
	streaming string
	// offset is how many lines the view is scrolled back, and lines how
	// many there were at the last draw, to clamp it with.
	offset, lines int
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
	g.conv.words, g.conv.streaming, g.conv.ending = "", "", false
	g.conv.offset, g.conv.lines, g.conv.pics = 0, 0, nil
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
	words, rend := g.conv.words, g.conv.rend
	g.conv.words, g.conv.streaming, g.conv.pics = "", "", nil
	g.convMu.Unlock()
	g.log.Debug(hlog.Input, "conversation ended: %s", why)

	g.closeInputWith("")
	if words == "" || rend == nil {
		return
	}
	if muted, _ := g.quiet.Active(); muted {
		return
	}
	g.drawMu.Lock()
	sp := rend.Render(words)
	g.drawMu.Unlock()
	was := g.balloonGen()
	g.showBalloonSprite(sp)
	g.recallMoved(was, g.balloonGen())
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

// conversationScroll is how many lines one step of the wheel moves the
// conversation view.
const conversationScroll = 3

// streamInConversation puts words arriving mid-turn at the bottom of the
// view, reporting whether there was a conversation to put them in. Empty
// takes back what had streamed, for a turn that ended up saying nothing.
func (g *Ghost) streamInConversation(text string) bool {
	g.convMu.Lock()
	if !g.conv.open {
		g.convMu.Unlock()
		return false
	}
	g.conv.streaming, g.conv.last = text, time.Now()
	if text != "" {
		g.conv.words, g.conv.offset = text, 0
	}
	g.convMu.Unlock()
	g.drawInput()
	return true
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
	g.conv.words, g.conv.streaming, g.conv.last = text, "", time.Now()
	g.convMu.Unlock()
	g.announce(proto.ActedSpoke, text)
	g.remember(recalled{text: text, gen: g.balloonGen()})
	return true
}

// conversationSaid is something new in the conversation, the person's or
// the ghost's: the view goes back to the bottom to show it. It reports
// whether a conversation is open.
func (g *Ghost) conversationSaid() bool {
	g.convMu.Lock()
	defer g.convMu.Unlock()
	if g.conv.open {
		g.conv.offset, g.conv.last = 0, time.Now()
	}
	return g.conv.open
}

// scrollConversation moves the view: back for a negative count of wheel
// steps, forward for a positive one, clamped to what there is.
func (g *Ghost) scrollConversation(steps int) {
	g.convMu.Lock()
	rend := g.conv.rend
	top := g.conv.lines
	if rend != nil {
		top -= rend.ConversationLines()
	}
	g.conv.offset = min(max(g.conv.offset-steps*conversationScroll, 0), max(top, 0))
	offset := g.conv.offset
	g.convMu.Unlock()
	g.log.Debug(hlog.Balloon, "conversation: %d lines back", offset)
	g.drawInput()
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

// conversationView paints the view with the line being typed beneath it:
// what has been said, from the recall, and the words still streaming.
// Called from drawInput, with drawMu held.
func (g *Ghost) conversationView(rend *balloon.Renderer, line string, caret bool) *sprite.Sprite {
	g.recallMu.Lock()
	said := append([]recalled(nil), g.recall.lines...)
	g.recallMu.Unlock()

	g.convMu.Lock()
	streaming, offset := g.conv.streaming, g.conv.offset
	if g.conv.pics == nil {
		g.conv.pics = map[string]image.Image{}
	}
	pics := g.conv.pics
	g.convMu.Unlock()

	entries := make([]balloon.Entry, 0, len(said)+1)
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
	sp, lines := rend.RenderConversation(entries, offset, line, caret)

	g.convMu.Lock()
	g.conv.lines = lines
	g.conv.offset = min(g.conv.offset, max(lines-rend.ConversationLines(), 0))
	g.convMu.Unlock()
	return sp
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
