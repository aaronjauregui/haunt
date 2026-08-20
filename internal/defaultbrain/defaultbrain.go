// Package defaultbrain is the brain a ghost gets when its manifest declares
// no brain of its own: chat in the ghost's persona, pictures on request,
// self-notes, and speaking first when left alone.
//
// It exists to take the compiler out of the authoring path. A ghost that
// ships art and a `[persona]` block is a whole ghost with this brain behind
// it — which matters because the people most likely to make ghosts are
// artists, and the wall they will tolerate is drawing, not `GOOS=wasip1`.
//
// It is a brain, not a shortcut into the runtime. It is built on brainkit
// like any third-party brain, speaks the same event/intent schema over a
// pipe (see internal/brain.StartBuiltin), and can do nothing a downloaded
// brain could not — the moment it could, the sandbox would stop meaning
// anything for everybody else.
package defaultbrain

import (
	"io"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/brainkit"
)

// KeyPrefix namespaces this brain's persisted state. The built-in brain
// shares a keyspace with whatever the ghost's author writes by hand, so it
// keeps to one prefix and leaves the rest alone.
const KeyPrefix = "default/"

// metKey records that this ghost has introduced itself, so it only explains
// the double-click to someone who has not seen it before.
const metKey = KeyPrefix + "met"

// promptKeyPrefix remembers request → rewritten image prompt.
const promptKeyPrefix = KeyPrefix + "prompt/"

// rewriteTag labels the silent prompt-rewriting request so its reply is not
// mistaken for ordinary conversation.
const rewriteTag = "imgprompt"

// lookTag labels a look the user asked for out loud.
const lookTag = "asked"

// What this ghost says while it waits. The driver draws the indicator; the
// words beside it are the character's, and these are deliberately plain —
// a default brain has no idea whose voice it is speaking in.
const (
	thinkingText = "thinking…"
	paintingText = "painting…"
)

const rewriteInstruction = `Rewrite this picture request as a prompt for an image generator: ` +
	`a comma-separated list of concrete visual tags covering subject, setting, ` +
	`lighting, composition and art style. Answer with the tags only — no ` +
	`sentences, no preamble, no quotes.

Request: `

// helpText is the ghost explaining itself. A hand-written brain's author
// knows what it responds to; a built-in one has to say so, because nobody
// wrote it and there is no source for the user to read.
const helpText = "Talk to me and I'll answer. `draw <something>` paints a picture, " +
	"`draw again` repaints it differently, `remember <fact>` keeps something " +
	"in mind, `look` glances at your screen."

// How long each kind of line stays up. The driver times these from the
// moment the line appears and drops them if something newer has arrived —
// which a timer set by this brain could not do, since it cannot know how
// long a model will take. That mattered: a `hush` scheduled beside a quip
// used to hide the reply that landed thirty seconds later.
// doubleClick is how close two pokes must be to mean "show me the menu".
//
// A counter was the first attempt — every third poke offered one — and it
// was unpredictable in the hand: the click that waves a menu away never
// reaches this brain, so the rhythm drifted and the menu arrived at moments
// nobody had asked for. A double-click is a gesture a person performs on
// purpose and can repeat at will, which is the whole difference.
//
// Timed from the events' own timestamps, which the bus stamps on the way
// in: the brain needs no clock, and a recorded session replays identically.
const doubleClick = 700 * time.Millisecond

const (
	lingerGreeting = 6000
	lingerPoke     = 4000
	lingerHelp     = 15000
	lingerNote     = 5000
	lingerReply    = 12000
	lingerImage    = 20000
	lingerError    = 8000
)

// The menu, for the same reason: a person who has just met a ghost should
// be able to find out what it does by clicking it, not by knowing to type
// the word "help". This is also the one path through the built-in brain
// that needs no language model at all.
const (
	menuTag      = "menu"
	menuHelp     = "What can you do?"
	menuLook     = "Have a look at my screen"
	menuWatch    = "Keep an eye on what I'm working on"
	menuDrawMore = "Draw that again"
	menuNothing  = "Nothing, thanks"
)

// pokes are what the ghost says when poked. Answered locally and instantly:
// a click is a small gesture, and billing a language model for every one of
// them would be both slow and expensive.
var pokes = []string{
	"Mm?",
	"I'm here.",
	"That tickles.",
	"Yes, yes, I'm listening.",
	"Poke received.",
}

// Brain is the built-in brain's state. One per ghost; not safe for
// concurrent use, which brainkit's single-threaded dispatch guarantees.
type Brain struct {
	// lastWindow is the application and title the user last had focused,
	// when the window capability is granted.
	lastWindow string
	// lastRequest is the user's own words, kept as the fallback prompt if
	// the rewrite fails.
	lastRequest string
	// lastPrompt is the rewritten prompt actually painted, replayed by
	// "draw again".
	lastPrompt string
	// prompts maps a request to the prompt it was rewritten into. The
	// rewrite is a sampled LLM answer and so differs every time it runs;
	// remembering it is what lets asking twice for the same picture hit the
	// driver's generation cache. It persists via the store, so the cache
	// survives restarts too.
	prompts map[string]string
	// pokeN rotates the poke replies, so a second click is not the first
	// one again.
	pokeN int
	// lastNoticed is the most recent thing the session model made of the
	// day, kept for when the ghost speaks first later on.
	lastNoticed string
	// lastPoke is when the ghost was last clicked, for spotting the second
	// click of a double.
	lastPoke time.Time
	// lastSeen is the most recent thing another resident ghost did in
	// public, kept for the same reason lastNoticed is: when this ghost
	// speaks first later on, the room is part of what it can talk about.
	lastSeen string
	// lastReacted is when it last remarked on a neighbour, which is the
	// whole of its self-rationing. See company.go.
	lastReacted time.Time
	// poses are the faces this ghost's shell declares, learned at boot.
	// Empty on a single-image shell, which is why choosing one is always
	// conditional rather than assumed.
	poses []string
	// worn is the last face chosen for an occasion rather than for a line,
	// so the same shape twice running does not re-send the same pose.
	// Dialogue moves the face too and this does not track that; the cost
	// of being wrong is one redundant intent the driver drops.
	worn string
	// watched says window events have actually been arriving, which is the
	// only way a brain can tell whether it is being allowed to watch — the
	// grant itself is the driver's business and deliberately not visible
	// here. Used for one thing: not offering to start something already
	// running.
	watched bool
}

// offerMenu asks what the person wants, in a balloon they can click or
// answer with the number keys.
func (b *Brain) offerMenu(emit *brainkit.Emitter) {
	options := []string{menuHelp, menuLook}
	// Offering to watch is how window awareness gets asked for at all: it
	// is the one capability whose consumer is a background poller, so there
	// is no moment of use to ask at, and the person choosing it from a menu
	// is a better moment than either the install sheet or a popup at login.
	// Dropped once events are arriving, because a menu entry that starts
	// something already running is a menu entry that does nothing.
	if !b.watched {
		options = append(options, menuWatch)
	}
	if b.lastPrompt != "" {
		options = append(options, menuDrawMore)
	}
	options = append(options, menuNothing)
	emit.Offer(menuTag, "What would you like?", options...)
}

// onChoice acts on a menu pick. An empty name is a dismissal — the user
// turned away, which is an answer and not an error.
func (b *Brain) onChoice(picked string, emit *brainkit.Emitter) {
	switch picked {
	case "":
		return
	case menuHelp:
		emit.SayFor(helpText, lingerHelp)
	case menuLook:
		emit.Look(lookTag, "")
	case menuWatch:
		// No answer comes back. Either window events start arriving or they
		// do not, which is exactly what the brain sees when the capability
		// was never granted — so there is nothing here to handle.
		emit.Watch()
	case menuDrawMore:
		if b.lastPrompt != "" {
			emit.Send(brainkit.Intent{Type: "imagine", Text: b.lastPrompt, Fresh: true, Wait: "Again, differently…", LingerMS: lingerImage})
		}
	case menuNothing:
		emit.HideSay()
	}
}

// New returns a fresh built-in brain.
func New() *Brain { return &Brain{prompts: map[string]string{}} }

// Run drives a built-in brain over the given event/intent streams. This is
// the entry point internal/brain.StartBuiltin calls; it is the same loop a
// standalone brain's main would run.
func Run(events io.Reader, intents io.Writer) {
	brainkit.RunIO(New(), events, intents)
}

// OnEvent implements brainkit.Brain.
func (b *Brain) OnEvent(e brainkit.Event, emit *brainkit.Emitter) {
	switch e.Type {
	case "boot":
		// Which faces this shell has. A brain that means to choose one is
		// otherwise guessing names at a driver that quietly ignores the
		// ones that do not exist.
		b.poses = e.Poses
		for k, v := range e.KV {
			if request, ok := strings.CutPrefix(k, promptKeyPrefix); ok {
				b.prompts[request] = v
			}
		}
		// Boot opens only a couple of seconds of free speech, so the
		// greeting is said rather than generated: a model that took ten
		// seconds to answer would arrive after the ghost had gone quiet.
		//
		// The first time, it says how to find the menu. A gesture nobody
		// mentions is a gesture nobody performs, and this is the only place
		// a ghost gets to introduce itself.
		if _, met := e.KV[metKey]; met {
			emit.SayFor("…oh, you're here.", lingerGreeting)
		} else {
			emit.SayFor("…oh, hello. Double-click me if you want the menu.", lingerGreeting)
			emit.Remember(metKey, "yes")
		}

	case "click":
		// Two pokes in quick succession mean "show me what you can do".
		// Clicking is the only gesture a new user is sure to try, and
		// "type the word help" is a secret.
		if !b.lastPoke.IsZero() && e.Time.Sub(b.lastPoke) <= doubleClick {
			// Cleared, so a third click starts a fresh pair rather than
			// re-opening the menu that is already up.
			b.lastPoke = time.Time{}
			b.offerMenu(emit)
			return
		}
		b.lastPoke = e.Time
		emit.SayFor(b.nextPoke(), lingerPoke)

	case "choice":
		b.onChoice(e.Name, emit)

	case "window":
		// Titles carry more than they look like they do — file names,
		// branch names, error text. The brain just remembers what it saw;
		// what it does with that is the model's business later, through
		// recall.
		b.watched = true
		if e.Name != "" {
			b.lastWindow = e.Name + ": " + e.Text
		}

	case "ai_look":
		// Looking is deliberate and rationed, so what came back is worth
		// writing down rather than announcing. Narrating "I see you have a
		// terminal open" is the tiresome version of this feature.
		emit.Note("Glanced at their screen: " + e.Text)

	case "session":
		// Something about the shape of the day rather than a single
		// window: a long stretch, a flip-flop, a title that keeps coming
		// back. This is what initiative should be spent on — a remark that
		// lands because it is *about* something, rather than one fired by a
		// timer against idleness. The driver decides whether the ghost may
		// spend an unprompted action on it; the brain only tries.
		if e.Session == nil {
			return
		}
		b.watched = true
		b.lastNoticed = e.Session.Summary
		// The face first, because it is free and certain: the remark below
		// is an unprompted action the driver may well refuse, and a ghost
		// that only looks sleepy on the nights it has an interruption to
		// spare is a ghost whose face is rationed. See face.go.
		b.wearForOccasion(e, emit)
		emit.Initiate("You have noticed this about them just now: " + e.Session.Summary +
			". Say one short thing to them about it, unprompted — or nothing much, if it " +
			"would be tiresome to remark on.")

	case "observed":
		// Another ghost in the room did something in public. Nobody
		// addressed this one, so anything it does about it is unprompted
		// and charged — see company.go for why the driver's charge is not
		// the only brake here.
		b.onObserved(e, emit)

	case "idle":
		// Initiative: nobody asked. The driver decides whether this may
		// spend one of the ghost's unprompted actions — the brain just
		// tries. Initiate rather than Chat, so the direction below is not
		// remembered as something the user said.
		direction := "Nobody has spoken to you in a while. Say one short thing " +
			"to them, unprompted — draw on what you remember about them."
		if b.lastNoticed != "" {
			// The shape of the day beats the last window title: "forty
			// minutes on one file" is a thing to say something about, where
			// "nvim" is not.
			direction += " You noticed earlier: " + b.lastNoticed + "."
		} else if b.lastWindow != "" {
			// What they were last doing is usually the most interesting
			// thing the ghost knows.
			direction += " They were last looking at " + b.lastWindow + "."
		} else if b.lastSeen != "" {
			// And failing both, there may be somebody else in the room.
			// Last, because the person is the point: what their afternoon
			// looked like beats what the ghost next door was up to.
			direction += " " + b.lastSeen + "."
		}
		emit.Initiate(direction)

	case "cron":
		// A manifest trigger fired — a morning greeting, an anniversary.
		// The manifest names the occasion; the model finds the words, so an
		// author gets scheduled dialogue without writing any.
		emit.Initiate("The occasion is: " + e.Name +
			". Say one short thing to them about it, unprompted.")

	case "message":
		b.onMessage(strings.TrimSpace(e.Text), emit)

	case "ai_reply":
		if e.Tag == rewriteTag {
			b.lastPrompt = cleanPrompt(e.Text, b.lastRequest)
			b.prompts[b.lastRequest] = b.lastPrompt
			emit.Remember(promptKeyPrefix+b.lastRequest, b.lastPrompt)
			// Out of the way first: the driver's waiting indicator gives
			// precedence to a balloon already on screen, so leaving
			// "Composing…" up would leave it up for the whole generation.
			emit.HideSay()
			emit.Send(brainkit.Intent{Type: "imagine", Text: b.lastPrompt, Wait: paintingText, LingerMS: lingerImage})
			return
		}
		// The driver streamed it to the balloon and is timing how long it
		// stays; nothing left to do.

	case "ai_image":
		// Likewise: the linger rode along with the request.

	case "ai_error":
		if e.Tag == rewriteTag && b.lastRequest != "" {
			// The rewrite is a nicety; paint the user's words instead.
			b.lastPrompt = b.lastRequest
			emit.HideSay()
			emit.Send(brainkit.Intent{Type: "imagine", Text: b.lastPrompt, Wait: paintingText, LingerMS: lingerImage})
			return
		}
		emit.SayFor("(that didn't work: "+e.Text+")", lingerError)
	}
}

// onMessage handles text from a person: a few spoken commands, and
// otherwise conversation.
func (b *Brain) onMessage(text string, emit *brainkit.Emitter) {
	switch strings.ToLower(text) {
	case "help", "?":
		b.offerMenu(emit)
		return
	case "draw again", "again":
		if b.lastPrompt == "" {
			emit.SayFor("Draw what? Ask me for something first.", lingerNote)
			return
		}
		emit.Send(brainkit.Intent{Type: "imagine", Text: b.lastPrompt, Fresh: true, Wait: "Again, differently…", LingerMS: lingerImage})
		return
	case "look":
		// The answer comes back as ai_look and goes in the journal, not the
		// balloon. The driver shows that a look is happening — looking is
		// required to be visible — so there is nothing to say here.
		emit.Look(lookTag, "")
		return
	}
	if request, ok := strings.CutPrefix(text, "draw "); ok {
		b.lastRequest = request
		// Reuse the rewrite from last time, so the same request paints the
		// same picture from cache instead of paying for a fresh rewrite and
		// a fresh generation.
		if prompt, seen := b.prompts[request]; seen {
			b.lastPrompt = prompt
			emit.Send(brainkit.Intent{Type: "imagine", Text: prompt, Wait: paintingText, LingerMS: lingerImage})
			return
		}
		emit.Say("Composing…")
		emit.Ask(rewriteTag, rewriteInstruction+request)
		return
	}
	if fact, ok := strings.CutPrefix(text, "remember "); ok {
		emit.Note(fact)
		emit.SayFor("Noted, I'll keep that in mind.", lingerNote)
		return
	}
	// The reply's lifetime travels with the request, so the driver can time
	// it from when the answer arrives rather than from when it was asked
	// for.
	emit.Send(brainkit.Intent{
		Type:     "chat",
		Text:     text,
		Wait:     thinkingText,
		LingerMS: lingerReply,
	})
}

// nextPoke returns the next poke reply in rotation.
func (b *Brain) nextPoke() string {
	s := pokes[b.pokeN%len(pokes)]
	b.pokeN++
	return s
}

// cleanPrompt strips the wrapping a chatty model adds around its answer,
// falling back to the user's own request if nothing usable is left.
func cleanPrompt(reply, fallback string) string {
	p := strings.TrimSpace(reply)
	p = strings.Trim(p, "\"'`")
	p = strings.TrimSpace(p)
	if p == "" {
		return fallback
	}
	return p
}
