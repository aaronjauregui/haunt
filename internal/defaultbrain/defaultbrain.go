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
		emit.Say("…oh, you're here.")
		emit.SetTimer("hush", 6000)

	case "timer":
		if e.Name == "hush" {
			emit.HideSay()
		}

	case "click":
		emit.Say(b.nextPoke())
		emit.SetTimer("hush", 4000)

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
			emit.Say("Painting…")
			emit.Imagine(b.lastPrompt)
			return
		}
		// The driver already streamed it to the balloon; linger a while.
		emit.SetTimer("hush", 12000)

	case "ai_image":
		emit.SetTimer("hush", 20000)

	case "ai_error":
		if e.Tag == rewriteTag && b.lastRequest != "" {
			// The rewrite is a nicety; paint the user's words instead.
			b.lastPrompt = b.lastRequest
			emit.Say("Painting…")
			emit.Imagine(b.lastPrompt)
			return
		}
		emit.Say("(that didn't work: " + e.Text + ")")
		emit.SetTimer("hush", 8000)
	}
}

// onMessage handles text from a person: a few spoken commands, and
// otherwise conversation.
func (b *Brain) onMessage(text string, emit *brainkit.Emitter) {
	switch strings.ToLower(text) {
	case "help", "?":
		emit.Say(helpText)
		emit.SetTimer("hush", 15000)
		return
	case "draw again", "again":
		if b.lastPrompt == "" {
			emit.Say("Draw what? Ask me for something first.")
			emit.SetTimer("hush", 5000)
			return
		}
		emit.Say("Again, differently…")
		emit.ImagineFresh(b.lastPrompt)
		return
	case "look":
		// The answer comes back as ai_look and goes in the journal, not the
		// balloon — but this time the user asked, so say that it happened.
		emit.Say("Having a look…")
		emit.Look(lookTag, "")
		emit.SetTimer("hush", 5000)
		return
	}
	if request, ok := strings.CutPrefix(text, "draw "); ok {
		b.lastRequest = request
		// Reuse the rewrite from last time, so the same request paints the
		// same picture from cache instead of paying for a fresh rewrite and
		// a fresh generation.
		if prompt, seen := b.prompts[request]; seen {
			b.lastPrompt = prompt
			emit.Say("Painting…")
			emit.Imagine(prompt)
			return
		}
		emit.Say("Composing…")
		emit.Ask(rewriteTag, rewriteInstruction+request)
		return
	}
	if fact, ok := strings.CutPrefix(text, "remember "); ok {
		emit.Note(fact)
		emit.Say("Noted, I'll keep that in mind.")
		emit.SetTimer("hush", 5000)
		return
	}
	emit.Chat(text)
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
