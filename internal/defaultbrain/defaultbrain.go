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

	"github.com/aaronjauregui/haunt/brainkit"
)

// KeyPrefix namespaces this brain's persisted state. The built-in brain
// shares a keyspace with whatever the ghost's author writes by hand, so it
// keeps to one prefix and leaves the rest alone.
const KeyPrefix = "default/"

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
// pokesPerMenu is how many interactions pass before the ghost offers to
// explain itself again.
const pokesPerMenu = 3

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
	// pokes counts interactions since the last menu, so a person who keeps
	// clicking is offered one rather than another quip.
	pokes int
}

// offerMenu asks what the person wants, in a balloon they can click or
// answer with the number keys.
func (b *Brain) offerMenu(emit *brainkit.Emitter) {
	// Counted from here, so the next menu is three interactions away
	// whatever happened before this one.
	b.pokes = 0
	options := []string{menuHelp, menuLook}
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
		for k, v := range e.KV {
			if request, ok := strings.CutPrefix(k, promptKeyPrefix); ok {
				b.prompts[request] = v
			}
		}
		// Boot opens only a couple of seconds of free speech, so the
		// greeting is said rather than generated: a model that took ten
		// seconds to answer would arrive after the ghost had gone quiet.
		emit.SayFor("…oh, you're here.", lingerGreeting)

	case "click":
		// Every third interaction offers the menu instead of a quip:
		// clicking is the only gesture a new user is sure to try, and
		// "type the word help" is a secret.
		b.pokes++
		if b.pokes >= pokesPerMenu {
			b.offerMenu(emit)
			return
		}
		emit.SayFor(b.nextPoke(), lingerPoke)

	case "choice":
		// Dismissing a menu counts as an interaction too. The click that
		// waves a menu away never reaches this brain — the driver spends it
		// on the dismissal — so without this the rhythm drifts and a person
		// clicking three times gets nothing.
		b.pokes++
		b.onChoice(e.Name, emit)

	case "window":
		// Titles carry more than they look like they do — file names,
		// branch names, error text. The brain just remembers what it saw;
		// what it does with that is the model's business later, through
		// recall.
		if e.Name != "" {
			b.lastWindow = e.Name + ": " + e.Text
		}

	case "ai_look":
		// Looking is deliberate and rationed, so what came back is worth
		// writing down rather than announcing. Narrating "I see you have a
		// terminal open" is the tiresome version of this feature.
		emit.Note("Glanced at their screen: " + e.Text)

	case "idle":
		// Initiative: nobody asked. The driver decides whether this may
		// spend one of the ghost's unprompted actions — the brain just
		// tries. Initiate rather than Chat, so the direction below is not
		// remembered as something the user said.
		direction := "Nobody has spoken to you in a while. Say one short thing " +
			"to them, unprompted — draw on what you remember about them."
		if b.lastWindow != "" {
			// What they were last doing is usually the most interesting
			// thing the ghost knows.
			direction += " They were last looking at " + b.lastWindow + "."
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
