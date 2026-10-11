package balloon

import (
	"image"
	"image/draw"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/aaronjauregui/haunt/internal/sprite"
)

// The conversation view: what was said lately, the person's lines and the
// ghost's, newest at the bottom, with the line being typed beneath them,
// in one panel. It is what the balloon shows while a conversation is open,
// so several exchanges can be read together rather than the last reply
// alone, and the wheel can go back through them a few lines at a time.

// Entry is one thing said in a conversation: a line of the person's
// (You), or of the ghost's, or a picture the ghost showed.
type Entry struct {
	You   bool
	Text  string
	Image image.Image
	At    time.Time
}

// convRow is one line's slot in the laid-out transcript. A picture's
// first slot carries the thumbnail and how many slots it covers; the
// slots after it are empty.
type convRow struct {
	prefix string
	text   string
	thumb  *image.NRGBA
}

// maxThumbs bounds the thumbnails a renderer keeps scaled: the caret
// redraws the view twice a second, and scaling every picture in it each
// time is work for nothing.
const maxThumbs = 16

// ConversationLines is how many lines of transcript the conversation view
// shows at most above the line being typed: the most an offset can uncover
// at once.
func (r *Renderer) ConversationLines() int {
	t := r.theme
	lh := r.lineHeight()
	if t.MaxHeight <= 0 {
		return 1 << 30
	}
	return max((t.MaxHeight-2*t.Padding-lh-lh/2)/lh, 1)
}

// RenderConversation paints the conversation view: entries laid out as
// wrapped lines under a dim one-word prefix (`you`, or the theme's
// Speaker), a picture as a thumbnail, a blank line between entries; the
// last lines that fit, moved up by offset lines (clamped to the top); and
// the line being typed last. The panel is always the full width, and
// grows with the transcript until it reaches the height cap, so once it
// is full a redraw is the same size and can be done in place. lines is the
// transcript's length in lines, for the caller to clamp its offset with.
func (r *Renderer) RenderConversation(entries []Entry, offset int, line string, caret bool) (sp *sprite.Sprite, lines int) {
	t := r.theme
	lh := r.lineHeight()
	gap := lh / 2
	rows, indent := r.layoutConversation(entries)

	total := len(rows)
	room := r.ConversationLines()
	offset = min(max(offset, 0), max(total-room, 0))
	end := total - offset
	first := max(end-room, 0)
	shown := end - first
	viewH := shown * lh
	if shown > 0 {
		viewH += gap
	}

	img := image.NewNRGBA(image.Rect(0, 0, t.MaxWidth, viewH+lh+2*t.Padding))
	paintPanel(img, t)
	view := img.SubImage(image.Rect(t.Padding, t.Padding, t.MaxWidth-t.Padding, t.Padding+shown*lh)).(*image.NRGBA)
	text := &font.Drawer{Dst: view, Src: image.NewUniform(t.Text), Face: r.face}
	dim := &font.Drawer{Dst: view, Src: image.NewUniform(r.dim()), Face: r.face}
	ascent := r.face.Metrics().Ascent.Ceil()
	for i, row := range rows {
		y := t.Padding + (i-first)*lh
		h := lh
		if row.thumb != nil {
			h = row.thumb.Bounds().Dy()
		}
		if y+h <= t.Padding || y >= t.Padding+shown*lh {
			continue
		}
		if row.prefix != "" {
			dim.Dot = fixed.P(t.Padding, y+ascent)
			dim.DrawString(row.prefix)
		}
		if row.thumb != nil {
			at := image.Pt(t.Padding+indent, y)
			draw.Draw(view, row.thumb.Bounds().Add(at), row.thumb, image.Point{}, draw.Over)
			continue
		}
		if row.text != "" {
			text.Dot = fixed.P(t.Padding+indent, y+ascent)
			text.DrawString(row.text)
		}
	}

	in := &font.Drawer{Dst: img, Src: image.NewUniform(t.Text), Face: r.face,
		Dot: fixed.P(t.Padding, t.Padding+viewH+ascent)}
	in.DrawString(r.inputLine(line, caret))
	return &sprite.Sprite{Img: img}, total
}

// layoutConversation lays the entries out as line slots, and says how far
// the words are indented past the prefixes.
func (r *Renderer) layoutConversation(entries []Entry) ([]convRow, int) {
	t := r.theme
	lh := r.lineHeight()
	speaker := strings.TrimSpace(t.Speaker)
	if speaker == "" {
		speaker = "ghost"
	}
	indent := max(font.MeasureString(r.face, "you").Ceil(), font.MeasureString(r.face, speaker).Ceil()) + lh/2
	width := t.MaxWidth - 2*t.Padding - indent
	maxThumb := lh * max(r.ConversationLines()/2, 1)

	var rows []convRow
	for _, e := range entries {
		text := strings.TrimSpace(e.Text)
		if text == "" && e.Image == nil {
			continue
		}
		if len(rows) > 0 {
			rows = append(rows, convRow{})
		}
		prefix := speaker
		if e.You {
			prefix = "you"
		}
		if e.Image != nil {
			thumb := r.thumbnail(e.Image, width, maxThumb)
			rows = append(rows, convRow{prefix: prefix, thumb: thumb})
			for range (thumb.Bounds().Dy()+lh-1)/lh - 1 {
				rows = append(rows, convRow{})
			}
			prefix = ""
		}
		if text == "" {
			continue
		}
		for _, l := range r.wrap(text, width) {
			rows = append(rows, convRow{prefix: prefix, text: l})
			prefix = ""
		}
	}
	return rows, indent
}

// thumbnail is src scaled down to fit w by h (nearest neighbour, as
// RenderImage scales), kept for the next redraw.
func (r *Renderer) thumbnail(src image.Image, w, h int) *image.NRGBA {
	if th, ok := r.thumbs[src]; ok {
		return th
	}
	b := src.Bounds()
	tw, th := b.Dx(), b.Dy()
	if tw > w {
		th, tw = th*w/tw, w
	}
	if th > h {
		tw, th = tw*h/th, h
	}
	tw, th = max(tw, 1), max(th, 1)
	out := image.NewNRGBA(image.Rect(0, 0, tw, th))
	for y := range th {
		for x := range tw {
			out.Set(x, y, src.At(b.Min.X+x*b.Dx()/tw, b.Min.Y+y*b.Dy()/th))
		}
	}
	if r.thumbs == nil || len(r.thumbs) >= maxThumbs {
		r.thumbs = map[image.Image]*image.NRGBA{}
	}
	r.thumbs[src] = out
	return out
}
