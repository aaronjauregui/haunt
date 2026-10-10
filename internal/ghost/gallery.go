package ghost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// The gallery: every picture the ghost has painted, kept under a short name
// it can say back.
//
// A picture is expensive and a ghost is asked for the same one twice, so
// something has to hold on to them. A hash of the prompt would do that
// invisibly — and invisibly is the problem. "Draw the whale again" and
// "draw another whale" are the same words to a cache and opposite requests
// to a person, and a model that cannot tell which it got cannot say which
// it is showing. So the pictures are named, the ghost is told the name it
// just made, and showing one again is a thing it decides to do rather than
// a thing that happens to it.
//
// The metadata lives in the ghost's own store under the internal prefix, so
// it never reaches a brain as ordinary memory; the files live beside it in
// the state directory. Neither is shown to the model as a path: a name is
// all it can use and all it needs.

const (
	// pictureKeyPrefix holds one picture's record per key.
	pictureKeyPrefix = internalKeyPrefix + "picture/"
	// pictureNextKey is the counter behind the ids.
	pictureNextKey = internalKeyPrefix + "picture-next"
	// galleryKeep is how many pictures a ghost keeps. Enough that a
	// picture from last week is still there to be asked for, and bounded
	// because a megabyte a time on somebody's disk should have an end: the
	// oldest goes when a new one arrives.
	galleryKeep = 100
)

// Picture is one painting and what is known about it.
type Picture struct {
	// ID is the short name, "p7", the model repeats to show it again.
	ID string `json:"id"`
	// Prompt is what the asker wanted, in their words. Sent is what the
	// image model was given: the same thing with the house style on it.
	// Both are kept because they answer different questions — what was
	// asked for, and what came back looking the way it does.
	Prompt string `json:"prompt"`
	Sent   string `json:"sent"`
	// Style is the house style this one was painted in, if any.
	Style string `json:"style,omitempty"`
	// Seed is the number the picture was painted from, when the backend
	// reports one — the ComfyUI graphs do, an image endpoint does not. It
	// is kept because it is the only way back to this exact picture, and
	// not offered to the model, which has the picture itself.
	Seed int64     `json:"seed,omitempty"`
	At   time.Time `json:"at"`
	// Caption is what the ghost said about it, filled in when the turn
	// that drew it has finished saying it (brain_model.go).
	Caption string `json:"caption,omitempty"`
	// Seen is the vision model's one sentence about it, when the ghost
	// asked to look at what it had painted.
	Seen string `json:"seen,omitempty"`
}

// picturePath is where a picture's PNG lives.
func (g *Ghost) picturePath(id string) string {
	return filepath.Join(g.picturesDir, id+".png")
}

// nextPictureID hands out the next short name, under picMu so that two
// draws in flight at once cannot be given the same one.
//
// A counter that cannot be read is an error rather than a fresh start:
// beginning again at p1 would hand out a name the gallery already holds,
// and saving under it would write over a picture the person still has.
func (g *Ghost) nextPictureID() (string, error) {
	g.picMu.Lock()
	defer g.picMu.Unlock()
	raw, ok, err := g.store.Get(pictureNextKey)
	if err != nil {
		return "", fmt.Errorf("numbering a picture: %w", err)
	}
	n := 0
	if ok {
		if n, err = strconv.Atoi(raw); err != nil {
			return "", fmt.Errorf("numbering a picture: %q is not a number", raw)
		}
	}
	n++
	if err := g.store.Set(pictureNextKey, strconv.Itoa(n)); err != nil {
		return "", fmt.Errorf("numbering a picture: %w", err)
	}
	return "p" + strconv.Itoa(n), nil
}

// savePicture writes the PNG and its record, and drops the oldest picture
// over the limit.
func (g *Ghost) savePicture(p Picture, data []byte) error {
	if err := os.MkdirAll(g.picturesDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(g.picturePath(p.ID), data, 0o644); err != nil {
		return err
	}
	if err := g.putPicture(p); err != nil {
		return err
	}
	g.pruneGallery()
	return nil
}

// putPicture writes one record.
func (g *Ghost) putPicture(p Picture) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return g.store.Set(pictureKeyPrefix+p.ID, string(b))
}

// picture reads one record.
func (g *Ghost) picture(id string) (Picture, bool) {
	raw, ok, err := g.store.Get(pictureKeyPrefix + id)
	if err != nil || !ok {
		return Picture{}, false
	}
	var p Picture
	if json.Unmarshal([]byte(raw), &p) != nil {
		return Picture{}, false
	}
	return p, true
}

// gallery is every picture the ghost still has, newest first.
func (g *Ghost) gallery() []Picture {
	all, err := g.store.All()
	if err != nil {
		g.log.Warn(hlog.Memory, "reading the gallery: %v", err)
		return nil
	}
	var out []Picture
	for k, v := range all {
		if !strings.HasPrefix(k, pictureKeyPrefix) {
			continue
		}
		var p Picture
		if json.Unmarshal([]byte(v), &p) != nil {
			continue
		}
		out = append(out, p)
	}
	// By id rather than by time: the ids are handed out in order, and a
	// clock that went backwards should not shuffle a ghost's history.
	sort.Slice(out, func(i, j int) bool { return pictureNumber(out[i].ID) > pictureNumber(out[j].ID) })
	return out
}

// pictureNumber is the counter inside an id, for ordering.
func pictureNumber(id string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(id, "p"))
	if err != nil {
		return 0
	}
	return n
}

// pruneGallery drops the oldest pictures past the limit, file and record
// together, so a forgotten picture is not a name that answers with nothing.
func (g *Ghost) pruneGallery() {
	list := g.gallery()
	for _, p := range list[min(len(list), galleryKeep):] {
		os.Remove(g.picturePath(p.ID))
		if err := g.store.Delete(pictureKeyPrefix + p.ID); err != nil {
			g.log.Warn(hlog.Memory, "forgetting %s: %v", p.ID, err)
		}
	}
}

// reusable finds the newest picture painted from exactly this prompt whose
// file is still there. It is what a scripted brain gets when it asks for
// the same picture twice without asking for a fresh one.
func (g *Ghost) reusable(sent string) (Picture, bool) {
	for _, p := range g.gallery() {
		if p.Sent != sent {
			continue
		}
		if _, err := os.Stat(g.picturePath(p.ID)); err == nil {
			return p, true
		}
	}
	return Picture{}, false
}

// notePicture records that this turn put a picture in the balloon, so the
// turn's last words can become its caption.
func (g *Ghost) notePicture(id string) {
	g.picMu.Lock()
	defer g.picMu.Unlock()
	g.turnPics = append(g.turnPics, id)
}

// turnPicture is the one picture this turn showed, if it showed exactly
// one. Two pictures in a turn have no single caption, and the words that
// end such a turn belong to neither.
func (g *Ghost) turnPicture() (string, bool) {
	g.picMu.Lock()
	defer g.picMu.Unlock()
	if len(g.turnPics) != 1 {
		return "", false
	}
	return g.turnPics[0], true
}

// forgetTurnPictures starts a turn with nothing shown yet. What the turn
// before painted is not this turn's to caption.
func (g *Ghost) forgetTurnPictures() {
	g.picMu.Lock()
	defer g.picMu.Unlock()
	g.turnPics = nil
}

// captionPicture keeps what the ghost said about a picture, which is the
// line a person reading the gallery recognizes it by — far better than the
// prompt, which was written for an image model.
func (g *Ghost) captionPicture(id, caption string) {
	p, ok := g.picture(id)
	if !ok {
		return
	}
	p.Caption = caption
	if err := g.putPicture(p); err != nil {
		g.log.Warn(hlog.Memory, "captioning %s: %v", id, err)
	}
}

// describeGallery lists the pictures for the model: names, ages and the
// words that went with them.
func (g *Ghost) describeGallery(count int) string {
	list := g.gallery()
	if len(list) == 0 {
		return "(none yet)"
	}
	if len(list) > count {
		list = list[:count]
	}
	now := time.Now()
	var b strings.Builder
	for _, p := range list {
		fmt.Fprintf(&b, "%s · %s · %q", p.ID, shortAge(p.At, now), p.Prompt)
		if p.Caption != "" {
			fmt.Fprintf(&b, " · caption: %q", p.Caption)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// shortAge is how long ago, in the fewest characters that still say it.
func shortAge(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}
