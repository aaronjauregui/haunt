package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// Pictures: painting one, showing one again, the gallery, and the house
// style every picture is painted in.

func init() {
	register(verb{
		id: "draw",
		desc: "Paint a picture in your balloon. prompt is for an image generator: a concrete visual " +
			"description (subject, setting, lighting). Your own style is added unless style=false. " +
			"like=<name of a picture you painted> keeps that picture's composition and changes only " +
			"what your prompt changes. see=true also tells you, in one sentence, what came out.",
		params: []string{"prompt:string", "style?:boolean", "see?:boolean", "like?:string"},
		run:    tool(toolDraw),
	})
	register(verb{
		id:     "show",
		desc:   "Put a picture you painted before back in your balloon, by the name draw gave it.",
		params: []string{"id:string"},
		run:    tool(toolShow),
	})
	register(verb{
		id:     "pictures",
		desc:   "List the pictures you have painted, newest first, with their names.",
		params: []string{"count?:integer"},
		run:    tool(toolPictures),
	})
	register(verb{
		id: "style",
		desc: "Your house style: the look added to every picture you paint. With no arguments it says what " +
			"it is. set changes it for good, reset goes back to your package's — both only when the person asks.",
		params: []string{"set?:string", "reset?:boolean"},
		run:    tool(toolStyle),
		// Only a set or a reset is one; saying what the style is, is not
		// (sideEffecting in web.go).
		sideEffect: true,
	})
}

// pictureLinger is how long a picture the model drew stays up. The turn's
// words wait for it (see modelBrain.run).
const pictureLinger = 20 * time.Second

func toolDraw(g *Ghost, ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Prompt string `json:"prompt"`
		Style  *bool  `json:"style"`
		See    bool   `json:"see"`
		Like   string `json:"like"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return "", fmt.Errorf("draw what?")
	}
	if !g.ensureCapability("imagine") {
		return "", refuse("the imagine capability is not granted")
	}
	// The style is on by default: a ghost looks like itself unless it is
	// asked for something else, and a model that forgets the argument gets
	// the house look rather than a picture out of character.
	styled := a.Style == nil || *a.Style
	// The model asks for a picture by the name it knows it by, and the
	// seed is looked up here. A seed is nineteen digits; a name is two,
	// and the gallery exists because short names are what a model can
	// repeat without getting one wrong.
	var seed int64
	if a.Like != "" {
		like, ok := g.picture(a.Like)
		if !ok {
			return "", fmt.Errorf("no picture %s; call pictures to see what you have", a.Like)
		}
		if like.Seed == 0 {
			return "", fmt.Errorf("%s was not painted from a seed, so there is nothing to keep; draw it fresh instead", a.Like)
		}
		seed = like.Seed
	}
	p, err := g.drawPicture(drawRequest{
		prompt:   a.Prompt,
		style:    styled,
		seed:     seed,
		wait:     "painting…",
		lingerMS: int(pictureLinger / time.Millisecond),
	})
	if err != nil {
		return "", err
	}
	g.execNote(fmt.Sprintf("drew %s: %s", p.ID, clip(p.Prompt, 120)))
	out := fmt.Sprintf("Drew %s: %q", p.ID, p.Prompt)
	if p.Style != "" {
		out += fmt.Sprintf(" (in your style: %s)", p.Style)
	}
	if a.Like != "" {
		out += fmt.Sprintf(" (from %s, so only what you changed has changed)", a.Like)
	}
	out += ". It is in your balloon."
	if a.See {
		out += " " + g.seePicture(ctx, p)
	}
	return out, nil
}

// seeQuestion is what the vision model is asked about a picture the ghost
// painted. Fixed, and narrow: the point is to catch a picture that did not
// come out as asked, not to have the model admire its own work.
const seeQuestion = "Describe this picture in one sentence: subject, composition, colours."

// seePicture shows the ghost what it painted.
//
// It is the vision backend, not the screen one: nothing of the person's is
// being looked at, so there is no screen grant in it — only the ration
// every look is charged against.
func (g *Ghost) seePicture(ctx context.Context, p Picture) string {
	if g.backend == nil || !g.backend.VisionEnabled() {
		return "(nothing to see it with: no vision backend)"
	}
	data, err := os.ReadFile(g.picturePath(p.ID))
	if err != nil {
		return "(it could not be looked at: " + err.Error() + ")"
	}
	seen, err := g.backend.DescribeImage(ctx, data, seeQuestion)
	if err != nil {
		g.log.Warn(hlog.Backend, "seeing %s: %v", p.ID, err)
		return "(it could not be looked at: " + err.Error() + ")"
	}
	p.Seen = seen
	if err := g.putPicture(p); err != nil {
		g.log.Warn(hlog.Memory, "keeping what %s shows: %v", p.ID, err)
	}
	return "It shows: " + seen
}

func toolShow(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if !g.ensureCapability("imagine") {
		return "", refuse("the imagine capability is not granted")
	}
	p, ok := g.picture(a.ID)
	if !ok {
		return "", fmt.Errorf("no picture %s; call pictures to see what you have", a.ID)
	}
	// Nothing is generated, so nothing is spent and the fullscreen rule
	// does not apply: showing a picture already on disk costs a balloon.
	if err := g.showPicture(p, int(pictureLinger/time.Millisecond)); err != nil {
		return "", err
	}
	g.execNote(fmt.Sprintf("showed %s again: %s", p.ID, clip(p.Prompt, 120)))
	return fmt.Sprintf("%s is in your balloon: %q.", p.ID, p.Prompt), nil
}

// galleryListDefault and galleryListMax bound what pictures answers with:
// enough to find last week's whale, not so much that the list is the turn.
const (
	galleryListDefault = 8
	galleryListMax     = 20
)

func toolPictures(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Count int `json:"count"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.Count <= 0 {
		a.Count = galleryListDefault
	}
	if a.Count > galleryListMax {
		a.Count = galleryListMax
	}
	return g.describeGallery(a.Count), nil
}

func toolStyle(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Set   string `json:"set"`
		Reset bool   `json:"reset"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	set := strings.TrimSpace(a.Set)
	if set != "" || a.Reset {
		// A standing change to how the ghost looks is the person's to
		// make. On a turn nobody started, the ghost would be redecorating
		// itself in an empty room — and a turn carrying somebody else's
		// words is exactly where a style it never chose would come from.
		if !g.respondingToUser() {
			return "", refuse("only when the person asks for a standing change")
		}
		if len([]rune(set)) > styleChars {
			return "", fmt.Errorf("that style is longer than %d characters", styleChars)
		}
		if strings.ContainsAny(set, "\n\r") {
			return "", fmt.Errorf("a style is one line")
		}
		var err error
		if a.Reset {
			err = g.clearStyle()
		} else {
			err = g.setStyle(set)
		}
		if err != nil {
			return "", err
		}
		if a.Reset {
			g.execNote("went back to its package's drawing style")
		} else {
			g.execNote("draws in " + clip(set, 200) + " from now on")
		}
	}
	return g.describeStyle(), nil
}

// describeStyle is the style as the model is told it: what it draws in,
// where that came from, and what its package would have it be.
func (g *Ghost) describeStyle() string {
	from := "from your package"
	if s, ok := g.styleChange(); ok {
		from = "changed on " + s.At.Format("2 January")
	}
	style := g.effectiveStyle()
	if style == "" {
		style = "(none)"
	}
	seed := g.styleSeed()
	if seed == "" {
		seed = "(none)"
	}
	return fmt.Sprintf("Your style: %q (%s). Your package's: %q.", style, from, seed)
}
