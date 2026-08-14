// Package brainkit is the SDK for writing Haunt brains in Go. A brain built
// on brainkit compiles to a WASI module (GOOS=wasip1 GOARCH=wasm) for
// sandboxed in-driver execution, and to a native binary for socket brains —
// the transport is stdin/stdout JSON lines either way.
package brainkit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// Re-exported schema types so brains import only brainkit.
type (
	Event  = proto.Event
	Intent = proto.Intent
)

// Brain handles events. Implementations receive each event in stdin order.
type Brain interface {
	OnEvent(e Event, emit *Emitter)
}

// Func adapts a function to the Brain interface.
type Func func(e Event, emit *Emitter)

func (f Func) OnEvent(e Event, emit *Emitter) { f(e, emit) }

// Emitter sends intents back to the driver.
type Emitter struct {
	mu  sync.Mutex
	enc *json.Encoder
}

func (em *Emitter) emit(i Intent) {
	em.mu.Lock()
	defer em.mu.Unlock()
	if err := em.enc.Encode(i); err != nil {
		fmt.Fprintf(os.Stderr, "brainkit: emit failed: %v\n", err)
	}
}

// Say shows text in the ghost's balloon.
func (em *Emitter) Say(text string) { em.emit(Intent{Type: proto.IntentSay, Text: text}) }

// HideSay dismisses the balloon.
func (em *Emitter) HideSay() { em.emit(Intent{Type: proto.IntentHideSay}) }

// Move repositions the sprite to absolute output coordinates.
func (em *Emitter) Move(x, y int) { em.emit(Intent{Type: proto.IntentMove, X: x, Y: y}) }

// SetTimer schedules a named EventTimer delayMS milliseconds from now.
func (em *Emitter) SetTimer(name string, delayMS int) {
	em.emit(Intent{Type: proto.IntentSetTimer, Name: name, DelayMS: delayMS})
}

// CancelTimer cancels a pending named timer.
func (em *Emitter) CancelTimer(name string) {
	em.emit(Intent{Type: proto.IntentCancelTimer, Name: name})
}

// Remember persists key=value in the ghost's store; it comes back in the
// boot event's KV map next session.
func (em *Emitter) Remember(key, value string) {
	em.emit(Intent{Type: proto.IntentKVSet, Name: key, Value: value})
}

// Log writes a debug message via the driver.
func (em *Emitter) Log(text string) { em.emit(Intent{Type: proto.IntentLog, Text: text}) }

// Tell sends text to another running ghost (requires the tell capability).
// The receiving ghost sees a message event with From set to this ghost.
func (em *Emitter) Tell(ghostName, text string) {
	em.emit(Intent{Type: proto.IntentTell, Name: ghostName, Text: text})
}

// Chat asks the user's LLM to reply to text in this ghost's persona
// (requires the llm capability). The reply streams into the balloon and
// arrives back as an ai_reply event; failures arrive as ai_error.
func (em *Emitter) Chat(text string) {
	em.emit(Intent{Type: proto.IntentChat, Text: text})
}

// Ask is Chat without the balloon: the reply never reaches the user and is
// kept out of conversation history, arriving only as an ai_reply event whose
// Tag is the tag given here. Use it to put the LLM to work inside the brain —
// rewriting a scribbled request into a real image prompt, classifying an
// utterance — and act on the answer yourself.
func (em *Emitter) Ask(tag, text string) {
	em.emit(Intent{Type: proto.IntentChat, Text: text, Tag: tag, Silent: true})
}

// Initiate speaks first. direction is a stage direction for the model —
// "nobody has spoken in a while; say something short, drawing on what you
// remember" — not a line the user typed, and the driver records only the
// reply, as the ghost's own remark. Use this rather than Chat for anything
// the ghost says unbidden, or its memory fills with words it credits to a
// user who never said them.
//
// Unprompted actions come out of the ghost's daily curiosity allowance;
// the driver may refuse silently.
func (em *Emitter) Initiate(direction string) {
	em.emit(Intent{Type: proto.IntentChat, Text: direction, SelfPrompt: true})
}

// Imagine generates an image (requires the imagine capability), shown as an
// image balloon and reported by an ai_image event. Identical prompts are
// served from the generation cache.
func (em *Emitter) Imagine(prompt string) {
	em.emit(Intent{Type: proto.IntentImagine, Text: prompt})
}

// ImagineFresh is Imagine that ignores the cache, so a repeated prompt
// produces a new image — the "draw that again" gesture.
func (em *Emitter) ImagineFresh(prompt string) {
	em.emit(Intent{Type: proto.IntentImagine, Text: prompt, Fresh: true})
}

// SetExpression wears one of the poses the ghost's shell declares — the
// driver ignores a pose the shell does not have, so a brain written for a
// richer shell degrades quietly on a simpler one.
func (em *Emitter) SetExpression(pose string) {
	em.emit(Intent{Type: proto.IntentSetExpression, Name: pose})
}

// PlayClip runs one of the shell's declared animations. Clips the shell
// does not have are ignored, so a brain may ask for a flourish a simpler
// shell cannot perform.
func (em *Emitter) PlayClip(name string) {
	em.emit(Intent{Type: proto.IntentPlayClip, Name: name})
}

// Note writes a private journal entry. Recent notes are injected into LLM
// context on every Chat, so what the ghost chooses to remember shapes its
// future behavior. The user can read the journal with `haunt journal`.
func (em *Emitter) Note(text string) {
	em.emit(Intent{Type: proto.IntentNote, Text: text})
}

// Run reads events from stdin until EOF, dispatching each to b. Call it from
// main; it returns when the driver closes the event stream.
func Run(b Brain) {
	em := &Emitter{enc: json.NewEncoder(os.Stdout)}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			fmt.Fprintf(os.Stderr, "brainkit: bad event: %v\n", err)
			continue
		}
		b.OnEvent(e, em)
	}
}
