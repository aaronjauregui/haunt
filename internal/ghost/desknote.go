package ghost

import (
	"fmt"
	"image"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/sprite"
	"github.com/aaronjauregui/haunt/internal/wlclient"
)

// Notes left on the desktop.
//
// A line in the balloon is gone in twelve seconds, and a person who was
// out of the room never sees it. A note is the other thing a companion
// can leave: a few words, maybe with a picture it painted, on a slip of
// paper beside it, there when the person comes back.
//
// A note is an intention with a surface, not a mechanism of its own. It
// is stored in the intention list under the same key and the same cap,
// named model/note-<n>, so everything that already reads that list sees
// it: `intentions` lists it, `resolve` discharges it, `haunt journal
// -intentions` prints it, and the turn context tells the model what it
// has left lying about. What a note adds is a Note record (where the
// paper is, what is pinned to it) and a surface the driver owns.
//
// The paper is the person's to move and to take down. It is draggable,
// and where it is dropped is remembered; a click takes it down and
// resolves it, silently: the model learns it is gone because the turn
// context stops listing it. The ghost may take one down too, by resolving
// it. A ghost may have at most maxDeskNotes up at once, because a desktop
// papered with notes is a desktop the person has lost.

const (
	// maxDeskNotes is how many notes a ghost may have up at once.
	maxDeskNotes = 3
	// maxNoteChars is the most a note may say. The schema's, so a scripted
	// brain is held to the same.
	maxNoteChars = proto.MaxNoteChars
	// noteNamePrefix names a note in the intention list.
	noteNamePrefix = modelIntentionPrefix + "note-"
	// noteGap is the space between notes stacked above the ghost.
	noteGap = 8
)

// DeskNote is what makes an intention a note: where its paper is and what
// is pinned to it. Its words are the intention's Text.
type DeskNote struct {
	// Picture is the id of a picture from the gallery drawn above the
	// words, or empty.
	Picture string `json:"p,omitempty"`
	// Output and X, Y are where the paper was last put, relative to that
	// screen, as the ghost's own place is kept (place.go). All three empty
	// means it has not been shown yet.
	Output string `json:"o,omitempty"`
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
}

// placed reports whether the note has a position to go back to.
func (n DeskNote) placed() bool { return n.Output != "" || n.X != 0 || n.Y != 0 }

// deskNotes is the notes on screen, by intention name. A note shown with
// no compositor has an entry with a nil surface, so the bookkeeping is the
// same with a screen and without one.
type deskNotes struct {
	mu sync.Mutex
	up map[string]*wlclient.SpriteSurface
}

// put records a note's surface, handing back any it replaces.
func (d *deskNotes) put(name string, s *wlclient.SpriteSurface) *wlclient.SpriteSurface {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.up == nil {
		d.up = map[string]*wlclient.SpriteSurface{}
	}
	old := d.up[name]
	d.up[name] = s
	return old
}

// take removes one note, or every note when name is empty, and returns
// the surfaces that were up.
func (d *deskNotes) take(name string) []*wlclient.SpriteSurface {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []*wlclient.SpriteSurface
	for n, s := range d.up {
		if name != "" && n != name {
			continue
		}
		delete(d.up, n)
		if s != nil {
			out = append(out, s)
		}
	}
	return out
}

// rects is where the notes up are, for stacking a new one clear of them.
// The surfaces are read out under the lock and asked for their geometry
// after it, which takes the compositor connection's own.
func (d *deskNotes) rects() []image.Rectangle {
	d.mu.Lock()
	var surfs []*wlclient.SpriteSurface
	for _, s := range d.up {
		if s != nil {
			surfs = append(surfs, s)
		}
	}
	d.mu.Unlock()
	out := make([]image.Rectangle, 0, len(surfs))
	for _, s := range surfs {
		x, y, w, h := s.Geometry()
		out = append(out, image.Rect(int(x), int(y), int(x+w), int(y+h)))
	}
	return out
}

// count is how many notes are up.
func (d *deskNotes) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.up)
}

// execLeaveNote puts a note on the desktop: in.Text is its words and
// in.Name the id of a picture to pin to it, or empty. It answers with the
// note's name, or with why not: too many up already (listing them, so the
// model can resolve one in the next call), or a picture that is not in the
// gallery. Both the model's leave_note and a scripted brain's intent come
// here.
func (g *Ghost) execLeaveNote(in proto.Intent) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	text := strings.TrimSpace(in.Text)
	pic := strings.TrimSpace(in.Name)
	if pic != "" {
		if _, ok := g.picture(pic); !ok {
			return "", fmt.Errorf("no picture %s; call pictures to see what you have", pic)
		}
	}
	g.intentionsMu.Lock()
	list := g.loadIntentions()
	last := 0
	var up []string
	for _, it := range list {
		if it.Note == nil {
			continue
		}
		last = max(last, noteNumber(it.Name))
		if !it.Aside {
			up = append(up, fmt.Sprintf("%s (%q)", it.Name, it.Text))
		}
	}
	if len(up) >= maxDeskNotes {
		g.intentionsMu.Unlock()
		return "", refuse("%d notes are up already: %s; resolve one first", len(up), strings.Join(up, ", "))
	}
	note := Intention{
		Name: noteNamePrefix + strconv.Itoa(last+1),
		Text: text,
		Made: time.Now(),
		// As execIntend decides it, and for the same reason: driver-side,
		// from who was there, never from anything the brain says.
		Asked: g.respondingToUser() || g.obliged(),
		Note:  &DeskNote{Picture: pic},
	}
	list = append(list, note)
	sort.SliceStable(list, func(a, b int) bool { return list[a].Made.Before(list[b].Made) })
	list, gone := g.capIntentions(list)
	g.saveIntentions(list)
	g.intentionsMu.Unlock()
	g.takeDownNotes(gone)
	g.showNote(note)
	g.log.Info(hlog.Memory, "left a note: %s", text)
	g.announce(proto.ActedNoted, text)
	g.execNote("left a note: " + text)
	return note.Name, nil
}

// noteNumber is n in model/note-<n>, or 0.
func noteNumber(name string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(name, noteNamePrefix))
	return n
}

// showStoredNotes puts back every note the ghost had up when it last
// stopped. Called once at boot, after the ghost is on screen.
func (g *Ghost) showStoredNotes() {
	for _, it := range g.loadIntentions() {
		if it.Note != nil && !it.Aside {
			g.showNote(it)
		}
	}
}

// destroyNotes takes every note's surface down, at shutdown. The notes
// stay in the store, to be shown again at the next boot.
func (g *Ghost) destroyNotes() {
	for _, s := range g.notes.take("") {
		s.Destroy()
	}
}

// takeDownNote removes one note's surface, if it is up. Called wherever a
// note leaves the list, so the paper and the record go together.
func (g *Ghost) takeDownNote(name string) {
	for _, s := range g.notes.take(name) {
		s.Destroy()
	}
}

// notesUp is how many notes are on screen.
func (g *Ghost) notesUp() int { return g.notes.count() }

// showNote puts one note's paper on screen: where it was left if that
// screen is still there, otherwise beside the ghost, and remembers where.
//
// Nothing goes up once the session is stopping: destroyNotes has taken
// the paper down, or is about to, and a note put up after it would stay
// up, its click resolving into a closed store.
func (g *Ghost) showNote(it Intention) {
	if g.isStopping() {
		g.log.Debug(hlog.Memory, "note %s not shown: the ghost is stopping", it.Name)
		return
	}
	if g.app == nil || g.surface == nil {
		g.notes.put(it.Name, nil) // no compositor: tests keep the books only
		return
	}
	r := g.renderer()
	sp := r.RenderNote(it.Text, g.deskNotePicture(it.Note.Picture), g.notePaper())
	lw, lh := r.LogicalSize(sp.Size())
	out, x, y, placed := g.notePlace(*it.Note, lw, lh)
	s, err := g.app.CreateSpriteOpts(sp, int32(x), int32(y), wlclient.SurfaceOptions{
		Output:   out,
		LogicalW: int32(lw),
		LogicalH: int32(lh),
	})
	if err != nil {
		g.log.Warn(hlog.Balloon, "note %s failed: %v", it.Name, err)
		return
	}
	name := it.Name
	// A click takes it down, and says nothing to the model: the turn
	// context stops listing it, which is all it needs to know.
	s.OnClick = func(int, int) {
		g.log.Info(hlog.Memory, "note %s taken down by a click", name)
		g.execResolve(name)
	}
	// Dragged is where the person wants it.
	s.OnDragEnd = func(int, int) {
		sx, sy, _, _ := s.Geometry()
		g.moveNote(name, out, int(sx), int(sy))
	}
	if old := g.notes.put(name, s); old != nil {
		old.Destroy()
	}
	// Stopping began while the paper was being made, and destroyNotes may
	// have run before it was recorded.
	if g.isStopping() {
		g.takeDownNote(name)
		return
	}
	if !placed {
		g.moveNote(name, out, x, y)
	}
}

// deskNotePicture loads a note's picture, or nil when it has none or the
// gallery has since let it go: the words are still worth showing.
func (g *Ghost) deskNotePicture(id string) image.Image {
	if id == "" {
		return nil
	}
	img, err := g.loadPicture(id)
	if err != nil {
		g.log.Debug(hlog.Memory, "a note's picture %s: %v", id, err)
		return nil
	}
	return img
}

// notePaper is the shell's note art, or nil for the default sheet.
func (g *Ghost) notePaper() *sprite.Sprite {
	if g.shell == nil {
		return nil
	}
	g.shellMu.Lock()
	defer g.shellMu.Unlock()
	return g.shell.Note()
}

// notePlace decides where a note of logical size w by h goes: back where
// it was left if it has been shown before and its screen is still there,
// otherwise on the side the ghost speaks on, above its head, clear of the
// notes already up and of the other ghosts in the room. placed says the
// position is the remembered one.
func (g *Ghost) notePlace(n DeskNote, w, h int) (out string, x, y int, placed bool) {
	if n.placed() {
		if n.Output == "" {
			return "", n.X, n.Y, true
		}
		if o, ok := g.app.OutputNamed(n.Output); ok {
			x, y := clampTo(o, n.X, n.Y)
			return n.Output, x, y, true
		}
		g.log.Info(hlog.Place, "a note's screen %q is not connected; placing it again", n.Output)
	}
	gx, gy, gw, _ := g.surface.Geometry()
	edge, _, known := g.outputSize()
	x = int(balloonSide(gx, gw, int32(w), edge, known))
	y = stackNote(x, int(gy), w, h, g.notes.rects())
	if sp := g.opts.Spots; sp != nil {
		// The room's view is this ghost's own reservation, so claiming the
		// note's spot moves it; it is put back to where the ghost stands
		// at once. The note keeps clear of the others; they are not kept
		// clear of it, which is right for paper.
		x, y = sp.Claim(g.outputName, x, y, w, h, 0)
		sx, sy, sw, sh := g.surface.Geometry()
		sp.Moved(g.outputName, int(sx), int(sy), int(sw), int(sh))
	}
	return g.outputName, x, y, false
}

// stackNote is where a note w by h goes above a ghost whose top is at top,
// at x: just above the ghost, or above the lowest note it would otherwise
// cover. It stops at the top of the screen rather than leaving it.
func stackNote(x, top, w, h int, others []image.Rectangle) int {
	y := top - h - noteGap
	for range len(others) + 1 {
		cand := image.Rect(x, y, x+w, y+h)
		moved := false
		for _, o := range others {
			if cand.Overlaps(o) && o.Min.Y-h-noteGap < y {
				y, moved = o.Min.Y-h-noteGap, true
			}
		}
		if !moved {
			break
		}
	}
	return max(y, 0)
}

// moveNote remembers where a note's paper is, if the note is still held:
// a drag ending as the note is resolved must not bring it back.
func (g *Ghost) moveNote(name, out string, x, y int) {
	g.intentionsMu.Lock()
	defer g.intentionsMu.Unlock()
	list := g.loadIntentions()
	for i := range list {
		if list[i].Name == name && list[i].Note != nil {
			list[i].Note.Output, list[i].Note.X, list[i].Note.Y = out, x, y
			g.saveIntentions(list)
			return
		}
	}
}

// notesContext is the turn context's line about the notes the ghost has
// up, with a newline before it, or "" when there are none. Without it a
// ghost would leave the same note twice, or talk about one the person took
// down an hour ago.
func (g *Ghost) notesContext() string {
	var up []string
	for _, it := range g.loadIntentions() {
		if it.Note != nil && !it.Aside {
			up = append(up, fmt.Sprintf("%q (%s)", it.Text, it.Name))
		}
	}
	if len(up) == 0 {
		return ""
	}
	return "\nNotes you have left: " + strings.Join(up, "; ") + "."
}
