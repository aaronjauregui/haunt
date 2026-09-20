package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/aaronjauregui/haunt/internal/backend"
	"github.com/aaronjauregui/haunt/internal/hlog"
)

// The web, as a ghost reaches it: two tools, and the rules around them.
//
// Looking something up is the one thing a companion does that brings words
// into the turn which nobody in the room wrote. Everything here follows
// from that. The backend fetches and renders (internal/backend/web.go);
// this file decides whether the call may happen at all, and what the rest
// of the turn may do afterwards.
//
// Three gates, in order:
//
//   - The `web` capability, asked for in the balloon like any other. A
//     search nobody asked for cannot put the question — there is nothing
//     on screen to make sense of it — so it is refused instead.
//   - The unprompted policy, when the turn is one nobody started. The
//     driver cannot see which skill the model loaded, so "only where a
//     skill says to" is approximated by the kinds of event a skill acts
//     on, and bounded by a smaller ration and by quiet hours.

// webState is what the web tools need to know about the turn in flight.
//
// The tools do not run on the turn's goroutine: each call arrives over the
// control socket and is served on a connection of its own, so two calls in
// one turn can overlap if hermes ever issues them in parallel. Hence the
// mutex rather than plain fields.
type webState struct {
	mu sync.Mutex
	// person is whether somebody is waiting on this turn, and kind is the
	// event kind of one they are not ("occasion", "idle", ""). Both are
	// recorded when the turn starts, because a tool call cannot see the
	// turn it belongs to.
	person bool
	kind   string
}

// startWebTurn records what the turn is.
func (g *Ghost) startWebTurn(person bool, kind string) {
	g.web.mu.Lock()
	defer g.web.mu.Unlock()
	g.web.person, g.web.kind = person, kind
}

// webTurn reads both facts back.
func (g *Ghost) webTurn() (person bool, kind string) {
	g.web.mu.Lock()
	defer g.web.mu.Unlock()
	return g.web.person, g.web.kind
}

// unpromptedKinds are the events a ghost may look something up about
// without being asked, under the default policy.
//
// They are the ones a skill acts on and that carry no text from the
// person's screen: a morning occasion, an errand coming due, a goal that
// has gone quiet, waking up. The kinds left out — a session shape, going
// idle, a window observed, a neighbour's remark — are the ones whose
// envelope holds what the person happens to have in front of them, and a
// window title is exactly what should never become a query to somebody
// else's server.
var unpromptedKinds = map[string]bool{
	"occasion":      true,
	"intention_due": true,
	"goal_quiet":    true,
	"boot":          true,
}

// allowWeb settles whether a web call may happen now, and charges what an
// unprompted one costs. The refusal says which rule stopped it, so the
// model can say nothing rather than invent an answer.
func (g *Ghost) allowWeb() error {
	if g.backend == nil || !g.backend.SearchEnabled() {
		return refuse("there is no search backend configured")
	}
	person, kind := g.webTurn()
	if !person {
		switch g.backend.Unprompted() {
		case "never":
			return refuse("only when the person asks")
		case "any":
		default:
			if !unpromptedKinds[kind] {
				return refuse("only when the person asks, or where a skill says to")
			}
		}
		if muted, why := g.quiet.Active(); muted {
			return refuse("it is quiet time (%s)", why)
		}
		// The smaller ration first: the call charges the general one
		// itself, so an unprompted look spends both.
		if err := g.backend.SpendUnpromptedSearch(); err != nil {
			return refuse("%v", err)
		}
	}
	if !g.ensureCapability("web") {
		return refuse("the web capability is not granted")
	}
	return nil
}

// webRef is what a web tool's answer is logged as when it was too long to
// read in full, in the same spelling as a tool server's.
func webRef(tool string) toolRef { return toolRef{Server: "web", Tool: tool} }

func toolSearch(g *Ghost, ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", fmt.Errorf("search for what?")
	}
	if err := g.allowWeb(); err != nil {
		return "", err
	}
	results, err := g.backend.Search(ctx, a.Query, a.Count)
	if err != nil {
		return "", err
	}
	// What a ghost looked up is its own business: it goes in the journal,
	// where the person can see it, and to no other ghost in the room.
	g.execNote(fmt.Sprintf("searched %q (%d results)", clip(a.Query, 120), len(results)))
	g.log.Info(hlog.Tool, "searched %q: %d results", clip(a.Query, 120), len(results))
	return g.readable(webRef("search"), backend.RenderResults(a.Query, results)), nil
}

func toolReadPage(g *Ghost, ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		URL  string `json:"url"`
		From int    `json:"from"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.URL) == "" {
		return "", fmt.Errorf("read which page?")
	}
	if err := g.allowWeb(); err != nil {
		return "", err
	}
	page, err := g.backend.ReadPage(ctx, a.URL)
	if err != nil {
		return "", err
	}
	g.execNote("read " + clip(a.URL, 200))
	g.log.Info(hlog.Tool, "read %s", clip(a.URL, 200))
	return g.readable(webRef("read_page"), page.Excerpt(a.From)), nil
}
