package ghost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aaronjauregui/haunt/internal/backend"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// internalKeyPrefix marks driver-owned store keys hidden from brains.
const internalKeyPrefix = "_haunt/"

// visibleKV strips driver-internal keys from the boot snapshot.
func visibleKV(all map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range all {
		if !strings.HasPrefix(k, internalKeyPrefix) {
			out[k] = v
		}
	}
	return out
}

// execImagine handles an imagine intent: paint a picture and show it as a
// balloon. A scripted brain that asks twice for the same thing is shown the
// picture it already has, since that is the cheap behaviour those brains
// are written against; a fresh request paints regardless.
func (g *Ghost) execImagine(in proto.Intent, userDriven bool) {
	p, err := g.drawPicture(drawRequest{
		prompt:   in.Text,
		style:    true,
		reuse:    !in.Fresh,
		wait:     in.Wait,
		lingerMS: in.LingerMS,
	})
	if err != nil {
		g.publishAIError(err.Error(), in.Tag)
		return
	}
	g.publishAIResult(proto.Event{Type: proto.EventAIImage, Name: g.picturePath(p.ID), Tag: in.Tag}, userDriven)
}

// drawRequest is one picture, as the thing that wants it describes it.
type drawRequest struct {
	// prompt is what was asked for, in the asker's words.
	prompt string
	// style applies the ghost's house style on top of the prompt.
	style bool
	// reuse accepts a picture already painted from exactly this prompt.
	// The hermes brain never asks for it: it has the gallery and says
	// which picture it wants by name.
	reuse bool
	// seed paints from the number an earlier picture came out of, so that
	// what changes between the two is what the prompt changed and nothing
	// else. Zero picks a fresh one.
	seed     int64
	wait     string
	lingerMS int
}

// drawPicture paints, keeps and shows one picture, and hands back its
// gallery record — or says why there is not one.
func (g *Ghost) drawPicture(req drawRequest) (Picture, error) {
	// The capability was settled before this was called (execIntent's
	// startAI, or the draw tool).
	if g.backend == nil || !g.backend.ImageEnabled() {
		return Picture{}, fmt.Errorf("no image backend configured in the haunt config")
	}
	// A local diffusion run takes the whole GPU for seconds at a time,
	// which is a visible stutter in whatever is fullscreen. The refusal is
	// reported rather than silent: the user asked for a picture, so they
	// should hear why there isn't one.
	if g.desktop != nil && g.desktop.Fullscreen() {
		g.log.Info(hlog.Backend, "imagine refused: something is fullscreen")
		return Picture{}, fmt.Errorf("not while something is fullscreen — it would stutter")
	}

	style := ""
	if req.style {
		style = g.effectiveStyle()
	}
	sent := req.prompt
	if style != "" {
		sent += ", " + style
	}

	if req.reuse {
		if p, ok := g.reusable(sent); ok {
			// Nothing is being painted, so there is nothing to wait for and
			// no indicator.
			return p, g.showPicture(p, req.lingerMS)
		}
	}

	// A generation on a local backend is tens of seconds of nothing. The
	// indicator is the driver's job rather than the brain's precisely so a
	// brain that never thought about it still shows one.
	//
	// The negative goes with every picture, styled or not: it is what the
	// author will not have in their ghost's pictures, which is not a thing
	// one prompt gets to opt out of.
	wait := g.startWaiting(req.wait)
	img, err := g.backend.Draw(g.sessionContext(), backend.DrawRequest{
		Prompt:   sent,
		Negative: g.manifest.Imagine.Negative,
		Seed:     req.seed,
	})
	wait.Stop()
	if err != nil {
		g.log.Warn(hlog.Backend, "imagine: %v", err)
		return Picture{}, err
	}
	if _, err := png.Decode(bytes.NewReader(img.PNG)); err != nil {
		return Picture{}, fmt.Errorf("image backend returned invalid PNG: %w", err)
	}
	id, err := g.nextPictureID()
	if err != nil {
		return Picture{}, err
	}
	p := Picture{
		ID:     id,
		Prompt: req.prompt,
		Sent:   sent,
		Style:  style,
		Seed:   img.Seed,
		At:     time.Now(),
	}
	if err := g.savePicture(p, img.PNG); err != nil {
		return Picture{}, fmt.Errorf("keeping the picture: %w", err)
	}
	return p, g.showPicture(p, req.lingerMS)
}

// loadPicture reads a picture back from the gallery.
func (g *Ghost) loadPicture(id string) (image.Image, error) {
	data, err := os.ReadFile(g.picturePath(id))
	if err != nil {
		return nil, fmt.Errorf("that picture is gone")
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("that picture will not open: %w", err)
	}
	return img, nil
}

// showPicture hangs a picture in the balloon, whether it was just painted
// or asked for by name.
func (g *Ghost) showPicture(p Picture, lingerMS int) error {
	img, err := g.loadPicture(p.ID)
	if err != nil {
		return err
	}
	if g.showImageBalloon(img) {
		g.remember(recalled{picture: p.ID, gen: g.balloonGen()})
		// What the picture is of, to the ghosts who can see it hanging
		// there. The prompt is the honest description: a picture of a cat
		// visibly contains a cat, and the rewrite that produced it is not
		// something the driver has any better summary of.
		g.announce(proto.ActedDrew, p.Prompt)
	}
	g.notePicture(p.ID)
	if lingerMS > 0 {
		g.lingerBalloon(g.balloonGen(), time.Duration(lingerMS)*time.Millisecond)
	}
	return nil
}

// StyleStoreKey holds the house style the ghost was asked to adopt, when
// the person has asked it to adopt one. Exported for `haunt journal
// -style`, which is how a person sees what their ghost is drawing in
// without asking it.
const StyleStoreKey = internalKeyPrefix + "style"

// StyleChange is a standing change to the ghost's look.
//
// The package author seeds a style and the person may let the ghost keep a
// different one, so Seed records which seed the change was made against: a
// package that updates its taste should not silently overwrite what the
// person asked for, and a person comparing the two wants to see both.
type StyleChange struct {
	Style string    `json:"style"`
	Seed  string    `json:"seed"`
	At    time.Time `json:"at"`
}

// DecodeStyle parses a stored style change; used by the journal CLI.
func DecodeStyle(raw string) (StyleChange, error) {
	var s StyleChange
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return StyleChange{}, err
	}
	return s, nil
}

// styleSeed is the package's own style, the one the author shipped.
func (g *Ghost) styleSeed() string {
	if g.manifest == nil {
		return ""
	}
	return g.manifest.Imagine.Style
}

// styleChange is the standing change, if there is one.
func (g *Ghost) styleChange() (StyleChange, bool) {
	raw, ok, err := g.store.Get(StyleStoreKey)
	if err != nil || !ok {
		return StyleChange{}, false
	}
	s, err := DecodeStyle(raw)
	if err != nil || s.Style == "" {
		return StyleChange{}, false
	}
	return s, true
}

// effectiveStyle is the look every picture gets: what the ghost was asked
// to draw in, or failing that its package's taste.
func (g *Ghost) effectiveStyle() string {
	if s, ok := g.styleChange(); ok {
		return s.Style
	}
	return g.styleSeed()
}

// setStyle records a standing change to the ghost's look.
func (g *Ghost) setStyle(style string) error {
	b, err := json.Marshal(StyleChange{Style: style, Seed: g.styleSeed(), At: time.Now()})
	if err != nil {
		return err
	}
	return g.store.Set(StyleStoreKey, string(b))
}

// clearStyle puts the ghost back to its package's taste.
func (g *Ghost) clearStyle() error { return g.store.Delete(StyleStoreKey) }

// publishAIResult delivers an AI result, extending the response window when
// a person is waiting on it, however long the backend took to answer.
func (g *Ghost) publishAIResult(e proto.Event, userDriven bool) {
	if userDriven {
		g.markPrompted()
	}
	g.publish(e)
}

func (g *Ghost) publishAIError(msg, tag string) {
	g.publish(proto.Event{Type: proto.EventAIError, Text: msg, Tag: tag})
}

// toolResultChars is how much of one tool's answer goes back to the model.
//
// A tool's answer is the one thing in a turn the driver does not generate
// and cannot predict the size of: an MCP server is somebody else's program,
// answering with whatever it has, and a file or a query dump goes in whole.
// One tool call should not be able to crowd out everything the ghost knows.
//
// Generous for what a companion's tools actually answer with (a day's
// appointments, a search result, a note that was written) and much smaller
// than a document. A tool that genuinely needs to hand over more than this
// wants to be asked a narrower question, which the model can do: it still
// has the tool.
const toolResultChars = 2000

// readable trims a tool's answer to what the model is shown, and says so
// when it has.
//
// Saying so is the point rather than a courtesy. A silently truncated
// answer is worse than a refused one: the model reads half a result,
// cannot tell that it is half, and answers the person as though it had the
// whole thing. The note goes at the end, where it is the last thing read
// before the model decides how much to trust what it just saw.
//
// Unlike clip() this keeps the newlines: a tool's answer is often a list,
// and flattening it to one line to save characters would cost the model
// the structure it needs to read it. clip() is for the journal, where the
// entry is one line by design.
func (g *Ghost) readable(ref toolRef, text string) string {
	if len(text) <= toolResultChars {
		return text
	}
	// On a rune boundary: a tool may answer in any script, and half a
	// character is not a saving.
	cut := toolResultChars
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	dropped := len(text) - cut
	g.log.Info(hlog.Tool, "%s answered with %d characters; %d of them were not read",
		ref, len(text), dropped)
	return text[:cut] + fmt.Sprintf(
		"\n\n[This answer was too long to read in full: %d more characters were not seen. "+
			"Ask a narrower question if you need them.]", dropped)
}
