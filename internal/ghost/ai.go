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
	// Then the entity index: what the ghost has concluded about the people
	// and projects this utterance actually names. Recall answers "what does
	// this remind you of"; that question cannot answer "who is Sam", which
	// is about a subject rather than a resemblance (see entity.go).
	var recalled []scored
	if !silent {
		notes, recentNotes := g.notesContext()
		recalled = g.recallScored(text, recentNotes)
		picked := make([]memory, 0, len(recalled))
		for _, r := range recalled {
			picked = append(picked, r.m)
		}
		// And what it is still meaning to do. Last, because it is the
		// shortest and the most immediate: the journal is who the ghost has
		// become, recall is what this reminds it of, and this is what it
		// has not got round to yet.
		system += notes + g.entityContext(text) + g.recallContext(picked) +
			g.intentionsContext(text)
	}
	msgs = append(msgs, ai.Message{Role: "system", Content: system})
	msgs = append(msgs, history...)
	msgs = append(msgs, ai.Message{Role: "user", Content: text})

	// What this ghost may do, offered to the model alongside what it may
	// say. Left out of silent calls: those are the brain talking to itself
	// — rewriting a request into an image prompt — and a machine that
	// answered a rewrite by sending an email would be a machine nobody
	// should have built.
	var tools []ai.Tool
	var back map[string]toolRef
	if !silent && g.capabilityActive("tools") {
		g.startToolServers()
		tools, back = g.offerable()
	}

	// The mouth moves for exactly as long as words are arriving — the whole
	// reason the trigger is called `talk` and not `animate`. It starts on
	// the first token, not on the request: a mouth moving through a wait
	// with nothing being said is how a slow backend comes to look like a
	// broken ghost.
	defer g.stopTalking()

	// One utterance can take several requests. A model that has been given
	// tools answers either with words or with something it wants done
	// first, and the second kind is not an answer yet: the driver runs what
	// it asked for, hands back what happened, and asks again. Every one of
	// those actions is consented to and visible in its own right (act.go),
	// so this loop cannot become a way to do things quietly by doing them
	// inside somebody else's sentence.
	var full string
	var actions []string
	spent := 0
	for {
		reply, err := g.chatRound(msgs, ai.Options{Silent: silent, Tag: tag, Tools: tools}, in.Wait, silent)
		if err != nil {
			g.log.Warn(hlog.AI, "chat: %v", err)
			g.publishAIError(err.Error(), tag)
			return
		}
		if len(reply.Calls) == 0 {
			full = reply.Text
			break
		}
		// What it said on the way, if anything, is kept: a model that
		// explains itself before acting has said something worth hearing,
		// and the balloon has been showing it.
		msgs = append(msgs, ai.Message{
			Role:      "assistant",
			Content:   reply.Text,
			ToolCalls: reply.Calls,
		})
		for _, call := range reply.Calls {
			result := g.runToolCall(call, back)
			spent++
			if result.ran {
				actions = append(actions, result.ref)
			}
			msgs = append(msgs, ai.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Name:       call.Function.Name,
				Content:    result.text,
			})
		}
		// The stop. Without one, a model that keeps asking for the same
		// tool is an unbounded spend nobody asked for; with it, the next
		// round is offered no tools at all and so has to answer with what
		// it has, which is a better ending than a truncated exchange.
		if spent >= g.aiSvc.MaxToolCalls() {
			g.log.Info(hlog.Tool, "%d tool calls in one exchange; asking for an answer now", spent)
			tools = nil
		}
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

	// Why it said that. Written before the reply is published so a person
	// who asks the moment they see the balloon finds the answer already
	// there. Silent calls are excluded above: there was no balloon, so
	// there is no "that" (see why.go).
	g.noteWhy(Why{
		Time:         time.Now(),
		User:         text,
		SelfPrompt:   in.SelfPrompt,
		Reply:        full,
		Acted:        actions,
		Recalled:     recalledTrace(recalled),
		Window:       g.currentWindow(),
		System:       system,
		HistoryTurns: len(history) / 2,
	})

	// The brain hears the reply before memory is written. Remembering means
	// embedding, which is a call to a backend that can be slow or wedged,
	// and nothing about a *past* exchange should stand between a person and
	// the answer they are waiting for. (It did: a live Ollama whose
	// embeddings endpoint had stopped answering swallowed every reply.)
	g.publishAIResult(proto.Event{Type: proto.EventAIReply, Text: full, Tag: tag}, userDriven)

	// And the second retrieval path: what the ghost just concluded about the
	// subject, filed under its name. Recall would find this only by
	// resemblance; the entity index answers "who is moth" completely, which
	// is the question a ghost with a neighbour actually has.
	//
	// The reply itself is the fact, because the reply is the ghost's own
	// judgement and the driver has no business inventing a better one. Six
	// of them is a character sketch, which is exactly what entity.go is for.
	if subject := strings.TrimSpace(in.About); subject != "" {
		kind := g.subjectKind(subject)
		g.go_(hlog.Memory, "filing what it thinks of "+subject, func() {
			g.memMu.Lock()
			defer g.memMu.Unlock()
			g.recordEntity(subject, kind, []string{full})
		})
	}

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
	// Who it was about, when the brain said. Without this a remark about a
	// neighbour dissolves into the pool as an unattributed thing the ghost
	// once said, and consolidation has no name to cluster it under — which
	// is the whole of why a ghost could react to the ghost next door for
	// months and keep no sense of who it was.
	if subject := strings.TrimSpace(in.About); subject != "" {
		note = "About " + subject + ": " + note
	}
	// What it did in the course of answering is part of what happened, and
	// the journal already has an entry per action; this is what makes the
	// exchange itself findable by it months later ("when did you last
	// touch my notes?").
	if len(actions) > 0 {
		note += "\nAlong the way you used: " + strings.Join(actions, ", ")
	}
	g.go_(hlog.Memory, "remembering an exchange", func() { g.remember(kindExchange, note) })
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
	if g.showImageBalloon(img) {
		// What the picture is of, to the ghosts who can see it hanging
		// there. The prompt is the honest description: a picture of a cat
		// visibly contains a cat, and the rewrite that produced it is not
		// something the driver has any better summary of.
		g.announce(proto.ActedDrew, prompt)
	}
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

// chatRound is one request to the model: the waiting indicator in front of
// it, the first-token deadline over it, and the words streaming into the
// balloon as they arrive. An exchange is one of these when nothing is
// offered, and several when the model asks for something to be done.
func (g *Ghost) chatRound(msgs []ai.Message, opts ai.Options, wait string, silent bool) (ai.Reply, error) {
	// Until the first token there is nothing on screen to say the ghost is
	// working, so the driver says it (waiting.go). Not for silent calls:
	// those are the brain talking to itself, and the user is owed no
	// account of them.
	var waiting *waiter
	if !silent {
		waiting = g.startWaiting(wait)
	}
	defer waiting.Stop()

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
			waiting.Stop()
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
	reply, err := g.aiSvc.ChatWithTools(ctx, msgs, onDelta, opts)
	// The mouth stops when this round's words stop, not when the whole
	// exchange does: a ghost that goes on mouthing through a two-minute
	// tool call is a ghost that looks broken.
	g.stopTalking()
	if err != nil {
		if timedOut.Load() {
			err = fmt.Errorf("the model said nothing for %s — is the backend awake?",
				g.firstTokenTimeout())
		}
		return reply, err
	}
	return reply, nil
}

// toolResult is what one of the model's tool calls came to, in the terms
// this file needs: what to hand back to the model, and whether anything
// actually happened.
type toolResult struct {
	text string
	ref  string
	ran  bool
}

// runToolCall turns one call from the model into an action, or into an
// explanation of why there was not one. Every outcome comes back as
// content for the model rather than as an error: "you may not do that" and
// "that did not work" are both things it needs to hear in order to say
// something sensible next, and a refusal that surfaced as a failed exchange
// would leave the person who asked with nothing at all.
func (g *Ghost) runToolCall(call ai.ToolCall, back map[string]toolRef) toolResult {
	ref, known := back[call.Function.Name]
	if !known {
		// The model asked for something that was never offered. It happens,
		// and the honest answer is the one that stops it happening twice.
		g.log.Info(hlog.Tool, "the model asked for %q, which is not on offer", call.Function.Name)
		return toolResult{text: "There is no tool called " + call.Function.Name + "."}
	}
	args := json.RawMessage(strings.TrimSpace(call.Function.Arguments))
	if len(args) == 0 || !json.Valid(args) {
		// Small models produce this often enough to be worth answering
		// rather than failing on: an empty string is what a model sends
		// when it means no arguments, and anything else is a mistake it
		// can be told about and correct.
		if len(args) > 0 {
			return toolResult{text: "Those arguments are not valid JSON."}
		}
		args = nil
	}
	// No Wait words: inside an exchange the character has already said
	// whatever it had to say about waiting, and the driver names the tool.
	out := g.runTool(ref, args, "")
	if out.Refused {
		return toolResult{text: "That was not allowed: " + out.Text + "."}
	}
	return toolResult{text: out.Text, ref: ref.String(), ran: true}
}
