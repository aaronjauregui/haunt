// Muse is the AI example ghost: chat messages go to the user's configured
// LLM in Muse's persona, and "draw ..." messages become images.
//
// Drawing is a two-step chain. Raw user text makes a poor image prompt, so
// Muse first asks the LLM (silently, via Ask) to rewrite the request into
// tag-style prompt text, then paints that. "draw again" re-runs the last
// prompt with the cache bypassed, so the picture comes out different.
//
// Build: GOOS=wasip1 GOARCH=wasm go build -o brain.wasm ./examples/muse
package main

import (
	"strings"

	"github.com/aaronjauregui/haunt/brainkit"
)

// rewriteTag labels the silent prompt-rewriting request so its reply is not
// mistaken for ordinary conversation.
const rewriteTag = "imgprompt"

const rewriteInstruction = `Rewrite this picture request as a prompt for an image generator: ` +
	`a comma-separated list of concrete visual tags covering subject, setting, ` +
	`lighting, composition and art style. Answer with the tags only — no ` +
	`sentences, no preamble, no quotes.

Request: `

// promptKeyPrefix namespaces the remembered rewrites in the ghost's store.
const promptKeyPrefix = "prompt/"

var (
	// lastRequest is the user's own words, kept as the fallback prompt if
	// the rewrite fails.
	lastRequest string
	// lastPrompt is the rewritten prompt actually painted, replayed by
	// "draw again".
	lastPrompt string
	// prompts maps a request to the prompt it was rewritten into. The
	// rewrite is a sampled LLM answer and so differs every time it runs;
	// remembering it is what lets asking twice for the same picture hit
	// the driver's generation cache. It persists via the store, so the
	// cache survives restarts too.
	prompts = map[string]string{}
)

func main() {
	brainkit.Run(brainkit.Func(func(e brainkit.Event, emit *brainkit.Emitter) {
		switch e.Type {
		case "boot":
			for k, v := range e.KV {
				if request, ok := strings.CutPrefix(k, promptKeyPrefix); ok {
					prompts[request] = v
				}
			}
			emit.Say("Muse here. Talk to me with `haunt say`, or say `draw <something>`.")
			emit.SetTimer("hush", 6000)
		case "timer":
			if e.Name == "hush" {
				emit.HideSay()
			}
		case "message":
			text := strings.TrimSpace(e.Text)
			if text == "draw again" || text == "again" {
				if lastPrompt == "" {
					emit.Say("Draw what? Ask me for something first.")
					emit.SetTimer("hush", 5000)
					return
				}
				emit.Say("Again, differently…")
				emit.ImagineFresh(lastPrompt)
				return
			}
			if request, ok := strings.CutPrefix(text, "draw "); ok {
				lastRequest = request
				// Reuse the rewrite from last time, so the same request
				// paints the same picture from cache instead of paying for
				// a fresh rewrite and a fresh generation.
				if prompt, seen := prompts[request]; seen {
					lastPrompt = prompt
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
		case "ai_reply":
			if e.Tag == rewriteTag {
				lastPrompt = cleanPrompt(e.Text, lastRequest)
				prompts[lastRequest] = lastPrompt
				emit.Remember(promptKeyPrefix+lastRequest, lastPrompt)
				emit.Say("Painting…")
				emit.Imagine(lastPrompt)
				return
			}
			// The driver already streamed it to the balloon; linger a while.
			emit.SetTimer("hush", 12000)
		case "ai_image":
			emit.SetTimer("hush", 20000)
		case "ai_error":
			if e.Tag == rewriteTag && lastRequest != "" {
				// The rewrite is a nicety; paint the user's words instead.
				lastPrompt = lastRequest
				emit.Say("Painting…")
				emit.Imagine(lastPrompt)
				return
			}
			emit.Say("(my muse failed me: " + e.Text + ")")
			emit.SetTimer("hush", 8000)
		}
	}))
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
