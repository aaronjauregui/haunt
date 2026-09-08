// Package brainkit is the SDK for writing Haunt brains in Go. A brain built
// on brainkit compiles to a WASI module (GOOS=wasip1 GOARCH=wasm) for
// sandboxed in-driver execution, and to a native binary for socket brains —
// the transport is stdin/stdout JSON lines either way.
package brainkit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
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

// Offer puts a question in the balloon: the text, then the options, which
// the user can click or answer with the number keys. The pick comes back as
// a `choice` event whose Name is the option and whose Tag is the one given
// here; an empty Name means the question was dismissed, which a brain
// waiting on an answer has to handle or it waits forever.
//
// Menus are how classic ukagaka ghosts are mostly operated. They are also
// the only way to ask a question that needs no language model behind it,
// and the only interaction in the driver that works without a mouse.
// (Not to be confused with Ask, which puts the *model* to work silently.)
func (em *Emitter) Offer(tag, text string, options ...string) {
	em.emit(Intent{Type: proto.IntentSay, Text: text, Tag: tag, Choices: options})
}

// SayFor shows text and puts it away after ms milliseconds — but only if it
// is still the line on screen. Prefer this to Say plus a timer: a timer
// scheduled to hide *this* line fires later and hides whatever has arrived
// meanwhile, which on a slow model is the reply the person was waiting for.
// The driver can time it correctly because it knows what is on screen.
func (em *Emitter) SayFor(text string, ms int) {
	em.emit(Intent{Type: proto.IntentSay, Text: text, LingerMS: ms})
}

// Send emits an intent directly, for combinations these helpers do not
// cover — a chat that carries both waiting words and a linger, say. The
// driver validates whatever arrives, so this is an escape hatch rather than
// a back door.
func (em *Emitter) Send(i Intent) { em.emit(i) }

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

// ChatWaiting is Chat with something to say while the model thinks. The
// driver shows a waiting indicator of its own after half a second — every
// ghost gets that, whether or not its author considered slow backends —
// and puts these words beside it. "Thinking…" in one ghost's voice is
// "hold on, hold on" in another's, which is why the words come from here
// and the dots do not.
func (em *Emitter) ChatWaiting(text, waiting string) {
	em.emit(Intent{Type: proto.IntentChat, Text: text, Wait: waiting})
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

// InitiateAbout is Initiate for something the ghost is speaking about a
// named subject — most usefully another resident ghost it has just watched
// do something. What it says is kept as one of the things it knows about
// that subject, so a neighbour stops being a series of unconnected moments
// and starts being somebody the ghost has opinions about. Those opinions
// come back into context whenever the subject is mentioned again, by the
// person or by the ghost itself.
//
// The subject is whatever the brain calls it; a ghost's name as it arrived
// on an `observed` event is the obvious one. Nothing validates it, because
// this is the ghost's own private sense of who is who.
func (em *Emitter) InitiateAbout(subject, direction string) {
	em.emit(Intent{
		Type:       proto.IntentChat,
		Text:       direction,
		SelfPrompt: true,
		About:      subject,
	})
}

// Imagine generates an image (requires the imagine capability), shown as an
// image balloon and reported by an ai_image event. Identical prompts are
// served from the generation cache.
func (em *Emitter) Imagine(prompt string) {
	em.emit(Intent{Type: proto.IntentImagine, Text: prompt})
}

// ImagineWaiting is Imagine with something to say while the picture is
// being made — usually a longer wait than a reply, and on a local backend
// a wait with the whole GPU busy behind it.
func (em *Emitter) ImagineWaiting(prompt, waiting string) {
	em.emit(Intent{Type: proto.IntentImagine, Text: prompt, Wait: waiting})
}

// ImagineFresh is Imagine that ignores the cache, so a repeated prompt
// produces a new image — the "draw that again" gesture.
func (em *Emitter) ImagineFresh(prompt string) {
	em.emit(Intent{Type: proto.IntentImagine, Text: prompt, Fresh: true})
}

// ImagineFreshWaiting is ImagineFresh with words for the wait.
func (em *Emitter) ImagineFreshWaiting(prompt, waiting string) {
	em.emit(Intent{Type: proto.IntentImagine, Text: prompt, Fresh: true, Wait: waiting})
}

// SetExpression wears one of the poses the ghost's shell declares — the
// driver ignores a pose the shell does not have, so a brain written for a
// richer shell degrades quietly on a simpler one. Which poses exist
// arrives with the boot event, as Event.Poses.
//
// Faces are not only for lines being said. An event marked Event.MayPose
// is the driver offering an occasion to change expression with no words
// attached — its own rules about that (nothing over a fullscreen window,
// nothing under reduced motion) are already satisfied, and what to wear,
// or whether to bother, is the brain's to decide.
func (em *Emitter) SetExpression(pose string) {
	em.emit(Intent{Type: proto.IntentSetExpression, Name: pose})
}

// PlayClip runs one of the shell's declared animations. Clips the shell
// does not have are ignored, so a brain may ask for a flourish a simpler
// shell cannot perform.
func (em *Emitter) PlayClip(name string) {
	em.emit(Intent{Type: proto.IntentPlayClip, Name: name})
}

// Look shows the focused window to the user's vision model and returns
// what it says as an ai_look event (requires the screen capability, which
// is granted separately from everything else and for good reason).
// question may be empty, which asks the driver's own: what is this person
// working on, in one sentence.
//
// The answer comes to the brain, not to the balloon. What the ghost does
// with having looked is its own business — narrating it is the tiresome
// version of this.
func (em *Emitter) Look(tag, question string) {
	em.emit(Intent{Type: proto.IntentLook, Text: question, Tag: tag})
}

// Watch asks the driver to start noticing which window has focus, putting
// the permission question to the user if they have not answered it yet.
//
// Unlike every other capability, this one is not asked for at the moment it
// is needed: the thing that would need it is a background poller, and a
// permission popup about nothing in particular is exactly what asking in
// the moment was meant to replace. So the honest trigger is the user's own
// curiosity — offer it as a menu entry and emit this when they pick it —
// and the driver enforces that by ignoring a Watch that does not follow
// something the person just did.
//
// There is no answer event. If they say yes, window and session events
// start arriving; if they do not, nothing happens, which is the same thing
// a brain sees when the capability was never granted.
func (em *Emitter) Watch() {
	em.emit(Intent{Type: proto.IntentWatch})
}

// Correct asks the driver to let the person fix something the ghost
// believes about a subject. Offer it as a menu entry; emit this when they
// pick it.
//
// It carries nothing, and cannot be made to carry anything. Everything
// about the correction — which subject, which belief, what is true instead
// — is read from the driver's own entity index, asked in the driver's own
// balloon, and answered where this brain cannot hear it. That asymmetry is
// deliberate: a brain may *propose* that the person be asked, and may not
// perform a retirement, because filing a fact adds something they can see
// and object to while retiring one removes the evidence.
//
// There is no answer event, for the same reason Watch has none.
func (em *Emitter) Correct() {
	em.emit(Intent{Type: proto.IntentCorrect})
}

// Act runs one tool on one of the tool servers the ghost's manifest
// declares (requires the tools capability, and the user's yes for that
// individual tool the first time it is used). tool is in "server.tool"
// form — the names arrive at boot, as Event.Tools — and args is the
// arguments object, which may be nil for a tool that takes none.
//
// What happened comes back as an `acted` event carrying the tag given
// here: Text is what the tool said, whether or not it worked, because "the
// file is not there" is an answer and not a failure. Only a refusal — no
// capability, no consent, no allowance left — arrives as `ai_error`.
//
// Acting is not a quiet operation and cannot be made into one. The driver
// puts a working face on the ghost, a line in its balloon about what it
// did, and an entry in the journal, every time, for every tool call,
// whether a brain asked for it or the model did. That is the difference
// between a companion and a process that does things behind your back, and
// it is enforced in the driver so that no brain can decide otherwise.
//
// A brain does not have to use this at all: when the ghost has tools, the
// model is offered them during ordinary Chat, and a ghost that is art plus
// a persona can act without a line of code anywhere. Reach for Act when the
// ghost should do a *particular* thing at a *particular* moment — check the
// build when the timer fires — rather than whatever the conversation
// suggests.
func (em *Emitter) Act(tag, tool string, args map[string]any) {
	i := Intent{Type: proto.IntentAct, Name: tool, Tag: tag}
	if len(args) > 0 {
		b, err := json.Marshal(args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "brainkit: act %s: %v\n", tool, err)
			return
		}
		i.Args = b
	}
	em.emit(i)
}

// Intend takes on a standing purpose: something the ghost means to do and
// has not done. Unlike SetTimer this survives a restart, and unlike Remember
// it is not a fact but an outstanding obligation — the ghost carries it in
// its own context, so it knows what it is holding and can say so if asked.
//
// name identifies it, and taking on the same name again replaces it rather
// than adding a second, which is how a purpose is re-armed ("check again in
// ten minutes") without the list filling with copies. delayMS is when it
// should come due; zero means there is no moment and the ghost simply holds
// it until something discharges it.
//
// When the moment arrives an `intention` event carries the name and the
// text — possibly days later, possibly moments after a boot for something
// that fell due while the ghost was switched off, because being switched
// off is not a reason to forget what you were asked to do.
//
// Coming due does not discharge it. Call Resolve when the thing is actually
// dealt with; until then the ghost goes on carrying it, which is what stops
// a reminder raised into a quiet hour from being silently lost.
func (em *Emitter) Intend(name, text string, delayMS int) {
	em.emit(Intent{Type: proto.IntentIntend, Name: name, Text: text, DelayMS: delayMS})
}

// Resolve discharges a standing intention: the ghost stops carrying it and
// stops mentioning it. Resolving something that is not there is not an
// error.
func (em *Emitter) Resolve(name string) {
	em.emit(Intent{Type: proto.IntentResolve, Name: name})
}

// SetAside stops a goal being watched and brought up, without pretending it
// was finished. It is the other exit, and the two are not the same one:
// Resolve says the thing was done, and this says the person has stopped
// caring whether it is. A goal that is over and still generating "you have
// not touched this in eleven days" is the most irritating thing an ambient
// companion can do, so both exits have to exist.
//
// What is set aside stays on the books — it is still visible in
// `haunt journal -intentions`, marked — because deleting it would put a
// discharge in the record that never happened.
func (em *Emitter) SetAside(name string) {
	em.emit(Intent{Type: proto.IntentSetAside, Name: name})
}

// Note writes a private journal entry. Recent notes are injected into LLM
// context on every Chat, so what the ghost chooses to remember shapes its
// future behavior. The user can read the journal with `haunt journal`.
func (em *Emitter) Note(text string) {
	em.emit(Intent{Type: proto.IntentNote, Text: text})
}

// Run reads events from stdin until EOF, dispatching each to b. Call it from
// main; it returns when the driver closes the event stream.
func Run(b Brain) { RunIO(b, os.Stdin, os.Stdout) }

// RunIO is Run over an arbitrary event stream and intent sink. The driver
// uses it to host its own built-in brain in-process: the transport is a
// pipe rather than a WASI module's stdio, but the brain still speaks JSON
// lines through the same schema and the same validation, so the built-in
// brain can do nothing a downloaded one cannot.
func RunIO(b Brain, r io.Reader, w io.Writer) {
	em := &Emitter{enc: json.NewEncoder(w)}
	sc := bufio.NewScanner(r)
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
