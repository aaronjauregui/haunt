package ghost

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

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

// execImagine handles an imagine intent: generate (or reuse from cache) an
// image and show it as a balloon. A fresh request skips the cache lookup and
// lands under its own key, so "draw that again" yields a new picture instead
// of the one already on disk.
func (g *Ghost) execImagine(in proto.Intent, userDriven bool) {
	path, err := g.imagine(in.Text, in.Fresh, in.Wait, in.LingerMS)
	if err != nil {
		g.publishAIError(err.Error(), in.Tag)
		return
	}
	g.publishAIResult(proto.Event{Type: proto.EventAIImage, Name: path, Tag: in.Tag}, userDriven)
}

// imagine is execImagine for a caller that wants the answer back rather
// than as an event: the picture's path, or why there is not one. The hermes
// brain's draw tool is that caller (hermestools.go).
func (g *Ghost) imagine(prompt string, fresh bool, waitText string, lingerMS int) (string, error) {
	// The capability was settled before this was called (execIntent's
	// startAI, or the draw tool).
	if g.aiSvc == nil || !g.aiSvc.ImageEnabled() {
		return "", fmt.Errorf("no image backend configured in the haunt config")
	}
	// A local diffusion run takes the whole GPU for seconds at a time,
	// which is a visible stutter in whatever is fullscreen. The refusal is
	// reported rather than silent: the user asked for a picture, so they
	// should hear why there isn't one.
	if g.desktop != nil && g.desktop.Fullscreen() {
		g.log.Info(hlog.AI, "imagine refused: something is fullscreen")
		return "", fmt.Errorf("not while something is fullscreen — it would stutter")
	}

	// Content-addressed cache: same backend/model/prompt = same file.
	salt := ""
	if fresh {
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", fmt.Errorf("reroll: %w", err)
		}
		salt = "\x00" + hex.EncodeToString(nonce[:])
	}
	key := sha256.Sum256([]byte(g.aiSvc.ImageCacheKey() + "\x00" + prompt + salt))
	cachePath := filepath.Join(g.cacheDir, hex.EncodeToString(key[:8])+".png")
	data, err := os.ReadFile(cachePath)
	if err != nil {
		// A cache miss means a real generation, which on a local backend is
		// tens of seconds of nothing. The indicator is the driver's job
		// rather than the brain's precisely so a brain that never thought
		// about it still shows one; a cache hit skips it, because there is
		// nothing to wait for.
		wait := g.startWaiting(waitText)
		data, err = g.aiSvc.GenerateImage(g.sessionContext(), prompt)
		wait.Stop()
		if err != nil {
			g.log.Warn(hlog.AI, "imagine: %v", err)
			return "", err
		}
		if err := os.MkdirAll(g.cacheDir, 0o755); err == nil {
			os.WriteFile(cachePath, data, 0o644)
		}
	}

	img, err := png.Decode(bytesReader(data))
	if err != nil {
		return "", fmt.Errorf("image backend returned invalid PNG: %w", err)
	}
	if g.showImageBalloon(img) {
		// What the picture is of, to the ghosts who can see it hanging
		// there. The prompt is the honest description: a picture of a cat
		// visibly contains a cat, and the rewrite that produced it is not
		// something the driver has any better summary of.
		g.announce(proto.ActedDrew, prompt)
	}
	if lingerMS > 0 {
		g.lingerBalloon(g.balloonGen(), time.Duration(lingerMS)*time.Millisecond)
	}
	return cachePath, nil
}

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

// storeGet reads one key.
func storeGet(s Store, key string) (string, bool, error) {
	return s.Get(key)
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
