package ghost

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aaronjauregui/haunt/internal/ai"
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

const historyKey = "_haunt/chat_history"

// execChat handles a chat intent: persona + history + the new utterance go
// to the LLM; the reply streams into the balloon and lands as an ai_reply
// event. Runs on its own goroutine so a slow backend never blocks intents.
//
// A silent chat is a brain-internal call — no balloon, no history — used to
// chain the LLM into other work (rewriting a request into an image prompt,
// say) without the user seeing the scaffolding. Silent calls are also
// self-contained: persona prompt plus the given text, with neither the
// conversation nor the journal in context, so the same question tends to
// get the same answer no matter what has been said around it.
func (g *Ghost) execChat(in proto.Intent, userDriven bool) {
	text, tag, silent := in.Text, in.Tag, in.Silent
	// The capability was settled before this goroutine took the AI lock
	// (see execIntent and consent.go). Asking again here would re-read a
	// grants file that may have been written a millisecond ago, and refuse
	// a request the person just allowed.
	if g.aiSvc == nil || !g.aiSvc.LLMEnabled() {
		g.publishAIError("no LLM backend configured in the haunt config", tag)
		return
	}

	var history []ai.Message
	if !silent {
		history = g.loadHistory()
	}
	msgs := make([]ai.Message, 0, len(history)+2)
	system := g.manifest.Persona.System
	if system == "" {
		system = "You are " + g.manifest.Ghost.Name + ", a small desktop companion. Reply in one or two short sentences."
	}
	// The journal makes the persona self-modifying: notes the ghost wrote
	// about past interactions ride along with every request. Recall then
	// adds whatever the utterance is actually about, however long ago it
	// happened — the part the history window and the recent-notes window
	// both drop.
	if !silent {
		notes, recentNotes := g.notesContext()
		system += notes + g.recallContext(g.recall(text, recentNotes))
	}
	msgs = append(msgs, ai.Message{Role: "system", Content: system})
	msgs = append(msgs, history...)
	msgs = append(msgs, ai.Message{Role: "user", Content: text})

	// The mouth moves for exactly as long as words are arriving — the whole
	// reason the trigger is called `talk` and not `animate`. It starts on
	// the first token, not on the request: a mouth moving through a wait
	// with nothing being said is how a slow backend comes to look like a
	// broken ghost.
	defer g.stopTalking()

	// Until that first token there is nothing on screen to say the ghost is
	// working, so the driver says it (waiting.go). Not for silent calls:
	// those are the brain talking to itself, and the user is owed no
	// account of them.
	var wait *waiter
	if !silent {
		wait = g.startWaiting(in.Wait)
	}
	defer wait.Stop()

	// A backend that accepts the connection and then never answers would
	// otherwise hold this ghost's single AI slot until the HTTP client's
	// own timeout, minutes later, with every later request queued behind
	// it. The deadline is on the *first* token rather than the whole reply,
	// because a long answer streaming steadily is working and a long
	// silence is not.
	ctx, cancel := context.WithCancel(g.sessionContext())
	defer cancel()
	var timedOut atomic.Bool
	deadline := time.AfterFunc(g.firstTokenTimeout(), func() {
		timedOut.Store(true)
		cancel()
	})
	defer deadline.Stop()

	// Stream into the balloon, re-rendering at a gentle pace.
	var acc string
	var started sync.Once
	last := time.Now()
	onDelta := func(delta string) {
		started.Do(func() {
			deadline.Stop()
			// Take the indicator down before the first words land, or they
			// arrive under a balloon full of dots.
			wait.Stop()
			g.startTalking()
		})
		acc += delta
		if time.Since(last) >= 120*time.Millisecond {
			last = time.Now()
			g.showBalloon(acc)
		}
	}
	if silent {
		onDelta = func(string) { started.Do(func() { deadline.Stop() }) }
	}
	full, err := g.aiSvc.Chat(ctx, msgs, onDelta,
		ai.Options{Silent: silent, Tag: tag})
	if err != nil {
		if timedOut.Load() {
			err = fmt.Errorf("the model said nothing for %s — is the backend awake?",
				g.firstTokenTimeout())
		}
		g.log.Warn(hlog.AI, "chat: %v", err)
		g.publishAIError(err.Error(), tag)
		return
	}
	if silent {
		g.publishAIResult(proto.Event{Type: proto.EventAIReply, Text: full, Tag: tag}, userDriven)
		return
	}
	// The face before the words: inference runs ahead of the balloon so the
	// expression is already right when the line appears, and ahead of the
	// ai_reply event so a brain that sets its own expression lands last.
	g.inferExpression(full, true)
	// Streaming followed the tail so the newest words were visible as they
	// arrived; now that it is all here, show it from the beginning and let
	// the reader page through the rest.
	g.showPaged(full)
	// Timed from when the reply *landed*, not from when it was asked for.
	// A brain scheduling this itself cannot know how long the model took,
	// which is how a slow answer used to vanish the instant it appeared.
	if in.LingerMS > 0 {
		g.lingerBalloon(g.balloonGen(), time.Duration(in.LingerMS)*time.Millisecond)
	}

	turns := g.manifest.Persona.HistoryTurns
	if turns <= 0 {
		turns = 12
	}
	// A self-prompt is the ghost's own stage direction: the model saw it,
	// but the record must not claim the user said it. Only the reply is
	// kept, and kept as what it was — the ghost speaking first.
	if in.SelfPrompt {
		history = append(history, ai.Message{Role: "assistant", Content: full})
	} else {
		history = append(history,
			ai.Message{Role: "user", Content: text},
			ai.Message{Role: "assistant", Content: full})
	}
	if max := turns * 2; len(history) > max {
		history = history[len(history)-max:]
	}
	g.saveHistory(history)

	// The brain hears the reply before memory is written. Remembering means
	// embedding, which is a call to a backend that can be slow or wedged,
	// and nothing about a *past* exchange should stand between a person and
	// the answer they are waiting for. (It did: a live Ollama whose
	// embeddings endpoint had stopped answering swallowed every reply.)
	g.publishAIResult(proto.Event{Type: proto.EventAIReply, Text: full, Tag: tag}, userDriven)

	// The exchange leaves the history window eventually; this is what
	// outlives it. Labelled, because recall pastes it into a prompt months
	// later where two bare lines would be ambiguous about who said what.
	//
	// On its own goroutine, and deliberately outside the lock this chat
	// holds: writing down what was just said must not delay saying the next
	// thing. The ghost held one AI slot for everything, so a slow
	// embeddings endpoint used to make it mute for as long as it took.
	note := "The user said: " + text + "\nYou answered: " + full
	if in.SelfPrompt {
		note = "Unprompted, you said: " + full
	}
	g.go_(hlog.Memory, "remembering an exchange", func() { g.remember("exchange", note) })
}

// execImagine handles an imagine intent: generate (or reuse from cache) an
// image and show it as a balloon. A fresh request skips the cache lookup and
// lands under its own key, so "draw that again" yields a new picture instead
// of the one already on disk.
func (g *Ghost) execImagine(in proto.Intent, userDriven bool) {
	prompt, tag := in.Text, in.Tag
	// Settled before the lock, as with chat.
	if g.aiSvc == nil || !g.aiSvc.ImageEnabled() {
		g.publishAIError("no image backend configured in the haunt config", tag)
		return
	}
	// A local diffusion run takes the whole GPU for seconds at a time,
	// which is a visible stutter in whatever is fullscreen. The refusal is
	// reported rather than silent: the user asked for a picture, so they
	// should hear why there isn't one.
	if g.desktop != nil && g.desktop.Fullscreen() {
		g.log.Info(hlog.AI, "imagine refused: something is fullscreen")
		g.publishAIError("not while something is fullscreen — it would stutter", tag)
		return
	}

	// Content-addressed cache: same backend/model/prompt = same file.
	salt := ""
	if in.Fresh {
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			g.publishAIError("reroll: "+err.Error(), tag)
			return
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
		wait := g.startWaiting(in.Wait)
		data, err = g.aiSvc.GenerateImage(g.sessionContext(), prompt)
		wait.Stop()
		if err != nil {
			g.log.Warn(hlog.AI, "imagine: %v", err)
			g.publishAIError(err.Error(), tag)
			return
		}
		if err := os.MkdirAll(g.cacheDir, 0o755); err == nil {
			os.WriteFile(cachePath, data, 0o644)
		}
	}

	img, err := png.Decode(bytesReader(data))
	if err != nil {
		g.publishAIError("image backend returned invalid PNG: "+err.Error(), tag)
		return
	}
	g.showImageBalloon(img)
	if in.LingerMS > 0 {
		g.lingerBalloon(g.balloonGen(), time.Duration(in.LingerMS)*time.Millisecond)
	}
	g.publishAIResult(proto.Event{Type: proto.EventAIImage, Name: cachePath, Tag: tag}, userDriven)
}

// publishAIResult delivers an AI result, extending the response window when
// a person is waiting on it. That is what keeps a chained request — the
// prompt rewrite that becomes an imagine — free of the curiosity budget
// however long the model took to answer.
func (g *Ghost) publishAIResult(e proto.Event, userDriven bool) {
	if userDriven {
		g.markPrompted()
	}
	g.publish(e)
}

func (g *Ghost) publishAIError(msg, tag string) {
	g.publish(proto.Event{Type: proto.EventAIError, Text: msg, Tag: tag})
}

func (g *Ghost) loadHistory() []ai.Message {
	raw, ok, err := storeGet(g.store, historyKey)
	if err != nil || !ok {
		return nil
	}
	var h []ai.Message
	if json.Unmarshal([]byte(raw), &h) != nil {
		return nil
	}
	return h
}

func (g *Ghost) saveHistory(h []ai.Message) {
	b, err := json.Marshal(h)
	if err != nil {
		return
	}
	if err := g.store.Set(historyKey, string(b)); err != nil {
		g.log.Warn(hlog.Store, "saving chat history: %v", err)
	}
}

// HistoryStoreKey exposes the conversation's store key to the CLI.
const HistoryStoreKey = historyKey

// Exchange is one turn of recorded conversation, for `haunt history`.
type Exchange struct {
	Role string
	Text string
}

// DecodeHistory parses a stored conversation. The balloon is transient and
// a long reply pages away; this is how a person reads back what was
// actually said.
func DecodeHistory(raw string) ([]Exchange, error) {
	var msgs []ai.Message
	if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
		return nil, err
	}
	out := make([]Exchange, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, Exchange{Role: m.Role, Text: m.Content})
	}
	return out, nil
}

// storeGet adapts the narrow Store interface (which lacks Get) by scanning
// All; history is one key so this stays cheap.
func storeGet(s Store, key string) (string, bool, error) {
	all, err := s.All()
	if err != nil {
		return "", false, err
	}
	v, ok := all[key]
	return v, ok, nil
}
