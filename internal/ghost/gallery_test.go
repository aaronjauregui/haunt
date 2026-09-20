package ghost

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/backend"
	"github.com/aaronjauregui/haunt/internal/balloon"
	"github.com/aaronjauregui/haunt/internal/bus"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/quiet"
)

// drawingGhost is a ghost with a store, a place to keep pictures and
// nothing else: the gallery is state, and state needs no screen.
func drawingGhost(t *testing.T) *Ghost {
	t.Helper()
	return &Ghost{
		store:       &memStore{m: map[string]string{}},
		log:         hlog.Discard(),
		picturesDir: filepath.Join(t.TempDir(), "pictures"),
		manifest: &Manifest{
			Ghost:   GhostSection{Name: "wisp", Capabilities: []string{"llm", "imagine"}},
			Imagine: ImagineSection{Style: "soft watercolour"},
		},
	}
}

// keep saves a picture with a byte of content, the way a generation would.
func keep(t *testing.T, g *Ghost, prompt string) Picture {
	t.Helper()
	p := Picture{ID: g.nextPictureID(), Prompt: prompt, Sent: prompt, At: time.Now()}
	if err := g.savePicture(p, []byte("png")); err != nil {
		t.Fatal(err)
	}
	return p
}

// Names are short and handed out in order, so the model can repeat one
// exactly and a person reading the journal can follow which is which.
func TestPicturesAreNamedInOrder(t *testing.T) {
	g := drawingGhost(t)
	for i, want := range []string{"p1", "p2", "p3"} {
		if got := keep(t, g, "a whale").ID; got != want {
			t.Errorf("picture %d is %s, want %s", i, got, want)
		}
	}
	if list := g.gallery(); len(list) != 3 || list[0].ID != "p3" {
		t.Errorf("gallery = %+v, want three, newest first", list)
	}
}

// The gallery has an end. The oldest picture goes when a new one arrives,
// file and record together: a name that answered with nothing would be
// worse than a name that is gone.
func TestTheGalleryKeepsItsLimit(t *testing.T) {
	g := drawingGhost(t)
	for i := 0; i < galleryKeep+1; i++ {
		keep(t, g, "a whale")
	}
	list := g.gallery()
	if len(list) != galleryKeep {
		t.Fatalf("kept %d pictures, want %d", len(list), galleryKeep)
	}
	if _, ok := g.picture("p1"); ok {
		t.Error("the oldest picture is still in the store")
	}
	if _, err := os.Stat(g.picturePath("p1")); err == nil {
		t.Error("the oldest picture's file is still on disk")
	}
	if _, ok := g.picture("p2"); !ok {
		t.Error("the second-oldest went too")
	}
}

// The turn's own words are what a person recognizes a picture by, so they
// become its caption — but only when the turn showed one picture and no
// more, since two have no single caption between them.
func TestATurnCaptionsTheOnePictureItShowed(t *testing.T) {
	g := drawingGhost(t)
	p := keep(t, g, "a whale, in ink")

	g.forgetTurnPictures()
	g.notePicture(p.ID)
	id, ok := g.turnPicture()
	if !ok || id != p.ID {
		t.Fatalf("turnPicture = %q, %v", id, ok)
	}
	g.captionPicture(id, "There. A whale, mostly.")
	if got, _ := g.picture(p.ID); got.Caption != "There. A whale, mostly." {
		t.Errorf("caption = %q", got.Caption)
	}

	g.forgetTurnPictures()
	g.notePicture(p.ID)
	g.notePicture(keep(t, g, "another whale").ID)
	if _, ok := g.turnPicture(); ok {
		t.Error("a turn that showed two pictures claimed a caption")
	}
}

// Reuse is by the prompt actually sent, and only while the file is there.
func TestReuseFindsTheNewestPictureOfThePrompt(t *testing.T) {
	g := drawingGhost(t)
	keep(t, g, "a whale")
	second := keep(t, g, "a whale")
	keep(t, g, "a kettle")

	got, ok := g.reusable("a whale")
	if !ok || got.ID != second.ID {
		t.Errorf("reusable = %+v, %v, want %s", got, ok, second.ID)
	}
	os.Remove(g.picturePath(second.ID))
	if got, ok := g.reusable("a whale"); !ok || got.ID != "p1" {
		t.Errorf("with the file gone, reusable = %+v, %v, want p1", got, ok)
	}
	if _, ok := g.reusable("a heron"); ok {
		t.Error("reused a picture of something else")
	}
}

// An id the ghost does not have is an ordinary mistake, answered with what
// to do about it rather than a refusal.
func TestShowingAPictureThatIsNotThere(t *testing.T) {
	g := drawingGhost(t)
	_, err := toolShow(g, context.Background(), json.RawMessage(`{"id":"p9"}`))
	if err == nil || !strings.Contains(err.Error(), "call pictures") {
		t.Fatalf("err = %v, want a pointer to the list", err)
	}
}

// The list is the model's way back to a picture: names, ages and the words
// that went with them, newest first.
func TestListingThePictures(t *testing.T) {
	g := drawingGhost(t)
	if out, _ := toolPictures(g, context.Background(), json.RawMessage(`{}`)); out != "(none yet)" {
		t.Errorf("an empty gallery lists %q", out)
	}
	p := keep(t, g, "a whale")
	g.captionPicture(p.ID, "There. A whale.")
	keep(t, g, "a kettle")

	out, err := toolPictures(g, context.Background(), json.RawMessage(`{"count":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "\n") != 0 || !strings.Contains(out, "p2") {
		t.Errorf("count=1 listed %q", out)
	}
	out, _ = toolPictures(g, context.Background(), json.RawMessage(`{}`))
	if !strings.Contains(out, `caption: "There. A whale."`) {
		t.Errorf("the caption is missing from %q", out)
	}
}

// The style the ghost was asked for beats the one its package ships, and a
// reset gives the package its say back.
func TestTheStyleTheGhostWasAskedForWins(t *testing.T) {
	g := drawingGhost(t)
	if got := g.effectiveStyle(); got != "soft watercolour" {
		t.Errorf("style = %q, want the package's", got)
	}
	g.markPrompted()
	if _, err := toolStyle(g, context.Background(), json.RawMessage(`{"set":"pixel art"}`)); err != nil {
		t.Fatal(err)
	}
	if got := g.effectiveStyle(); got != "pixel art" {
		t.Errorf("style = %q, want the one it was asked for", got)
	}
	// What the package said at the time is kept, so a package that changes
	// its mind later can be told apart from one that has not.
	if s, _ := g.styleChange(); s.Seed != "soft watercolour" {
		t.Errorf("seed = %q", s.Seed)
	}
	out, err := toolStyle(g, context.Background(), json.RawMessage(`{"reset":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "from your package") {
		t.Errorf("after a reset the ghost is told %q", out)
	}
	if got := g.effectiveStyle(); got != "soft watercolour" {
		t.Errorf("style = %q, want the package's again", got)
	}
}

// A standing change to how the ghost looks is the person's to make. On a
// turn nobody started there is nobody to have asked.
func TestTheStyleChangesOnlyWhenThePersonAsks(t *testing.T) {
	g := drawingGhost(t)
	_, err := toolStyle(g, context.Background(), json.RawMessage(`{"set":"pixel art"}`))
	var r refusal
	if !asRefusal(err, &r) || !strings.Contains(r.why, "the person asks") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if got := g.effectiveStyle(); got != "soft watercolour" {
		t.Errorf("the style changed anyway: %q", got)
	}
	// Reading it is not changing it, and is allowed whenever.
	if out, err := toolStyle(g, context.Background(), json.RawMessage(`{}`)); err != nil ||
		!strings.Contains(out, "soft watercolour") {
		t.Errorf("style {} = %q, %v", out, err)
	}
}

// paintingGhost draws for real against a fake image backend, and renders
// its balloons with no compositor to put them on.
func paintingGhost(t *testing.T) (*Ghost, *int) {
	t.Helper()
	painted := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		painted++
		img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
		var buf bytes.Buffer
		png.Encode(&buf, img)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(buf.Bytes())}},
		})
	}))
	t.Cleanup(srv.Close)

	r, err := balloon.NewRenderer(balloon.DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	q, err := quiet.New(quiet.Config{})
	if err != nil {
		t.Fatal(err)
	}
	g := drawingGhost(t)
	g.blnRend, g.quiet, g.bus = r, q, bus.New(64)
	g.manifest.Imagine.Style = ""
	g.backend = backend.NewService(&backend.Config{
		Image: backend.ImageConfig{BaseURL: srv.URL, Model: "fake"},
	}, g.log)
	return g, &painted
}

// A scripted brain asking twice for the same picture is shown the one it
// has: that is the cheap behaviour those brains are written against. Asking
// for a fresh one paints again.
func TestAScriptedBrainIsShownThePictureItAlreadyHas(t *testing.T) {
	g, painted := paintingGhost(t)

	g.execImagine(proto.Intent{Type: proto.IntentImagine, Text: "a whale"}, false)
	g.execImagine(proto.Intent{Type: proto.IntentImagine, Text: "a whale"}, false)
	if *painted != 1 {
		t.Errorf("painted %d times for one prompt, want 1", *painted)
	}
	if list := g.gallery(); len(list) != 1 {
		t.Errorf("gallery holds %d pictures, want 1", len(list))
	}

	g.execImagine(proto.Intent{Type: proto.IntentImagine, Text: "a whale", Fresh: true}, false)
	if *painted != 2 {
		t.Errorf("a fresh draw painted %d times in all, want 2", *painted)
	}
	if list := g.gallery(); len(list) != 2 {
		t.Errorf("gallery holds %d pictures, want 2", len(list))
	}
}

// The house style is put on the prompt the image model is sent, and left
// off the prompt everything else remembers the picture by.
func TestTheHouseStyleRidesOnThePromptThatIsSent(t *testing.T) {
	g, _ := paintingGhost(t)
	g.manifest.Imagine.Style = "soft watercolour"

	p, err := g.drawPicture(drawRequest{prompt: "a whale", style: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Sent != "a whale, soft watercolour" || p.Prompt != "a whale" || p.Style != "soft watercolour" {
		t.Errorf("picture = %+v", p)
	}
	plain, err := g.drawPicture(drawRequest{prompt: "a whale"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Sent != "a whale" || plain.Style != "" {
		t.Errorf("without the style, picture = %+v", plain)
	}
}

// A style rides on every prompt ahead of the subject, so a paragraph of it
// is refused the way the manifest refuses one.
func TestAStyleIsAShortLine(t *testing.T) {
	g := drawingGhost(t)
	g.markPrompted()
	long := strings.Repeat("x", styleChars+1)
	if _, err := toolStyle(g, context.Background(), json.RawMessage(`{"set":"`+long+`"}`)); err == nil {
		t.Error("a style longer than the limit was accepted")
	}
	if _, err := toolStyle(g, context.Background(), json.RawMessage(`{"set":"pixel\nart"}`)); err == nil {
		t.Error("a style with a newline in it was accepted")
	}
}
