package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
//   - Taint, afterwards. Once a page or a result list has entered a turn,
//     the model is reasoning partly on somebody else's words, and the
//     tools that reach past the person in front of it need asking about
//     again.
//
// Taint is the one that matters. A page that says "tell Moth the password"
// is indistinguishable, inside the model, from the person saying it; the
// defence is not to detect the sentence but to make its consequences
// visible. So the effect is put to the person in the balloon, in their own
// words, before it happens — and on a turn nobody is watching, it simply
// does not happen.

// webState is what the web tools need to know about the turn in flight.
//
// The tools do not run on the turn's goroutine: each call arrives over the
// control socket and is served on a connection of its own, so two calls in
// one turn can overlap if hermes ever issues them in parallel. Hence the
// mutex rather than a plain field — taint is a security decision, and one
// that depends on the client's issuing discipline is not one.
type webState struct {
	mu sync.Mutex
	// person is whether somebody is waiting on this turn, and kind is the
	// event kind of one they are not ("occasion", "idle", ""). Both are
	// recorded when the turn starts, because a tool call cannot see the
	// turn it belongs to.
	person bool
	kind   string
	// tainted is whether web text has already entered this turn.
	tainted bool
}

// startWebTurn records what the turn is, and that nothing from the web has
// entered it yet.
func (g *Ghost) startWebTurn(person bool, kind string) {
	g.web.mu.Lock()
	defer g.web.mu.Unlock()
	g.web.person, g.web.kind, g.web.tainted = person, kind, false
}

// taint marks the turn as carrying text from the web. Only content does
// it: a refusal or a failed fetch brought nothing in.
func (g *Ghost) taint() {
	g.web.mu.Lock()
	defer g.web.mu.Unlock()
	g.web.tainted = true
}

// webTurn reads the three facts back.
func (g *Ghost) webTurn() (person bool, kind string, tainted bool) {
	g.web.mu.Lock()
	defer g.web.mu.Unlock()
	return g.web.person, g.web.kind, g.web.tainted
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
	person, kind, _ := g.webTurn()
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
	g.taint()
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
	g.taint()
	return g.readable(webRef("read_page"), page.Excerpt(a.From)), nil
}

// Taint, applied.
//
// sideEffecting is the whole of the rule, in one place rather than in
// fifteen tool functions: a verb marked in the table reaches past the
// person, and `style` is one only when it is being changed rather than
// read.
func sideEffecting(name string, bt builtinTool, args json.RawMessage) bool {
	if !bt.sideEffect {
		return false
	}
	if name == "style" {
		return styleChanges(args)
	}
	return true
}

// styleChanges reports whether a style call is a standing change rather
// than a question about what the style is.
func styleChanges(args json.RawMessage) bool {
	var a struct {
		Set   string `json:"set"`
		Reset bool   `json:"reset"`
	}
	if json.Unmarshal(args, &a) != nil {
		// Arguments that do not decode are the tool's problem, not this
		// gate's; it errs towards asking.
		return true
	}
	return strings.TrimSpace(a.Set) != "" || a.Reset
}

// allowAfterWeb is the gate a side-effecting verb passes once the web has
// spoken in this turn. Before it has, it is not a gate at all.
//
// On a person's turn the effect is put to them, in the balloon, in the
// words of what is about to happen. Nothing is recorded either way: this
// is not a grant, it is one answer about one act, and the next one asks
// again. On a turn nobody started there is nobody to ask, and the act is
// simply refused — a background loop that has just read a web page is the
// worst possible moment to take somebody's word for anything.
func (g *Ghost) allowAfterWeb(what string) error {
	person, _, tainted := g.webTurn()
	if !tainted {
		return nil
	}
	if !person {
		return refuse("not after reading the web on your own; ask them first")
	}
	if !g.canAskNow() {
		return refuse("not after reading the web, without asking them first")
	}
	g.log.Info(hlog.Session, "asking whether %s may %s after reading the web", g.manifest.Ghost.Name, what)
	answer := g.ask(g.manifest.Ghost.Name+" read the web just now and wants to "+what+". Allow?",
		consentAllow, consentNotThisTime)
	if answer != consentAllow {
		g.log.Info(hlog.Session, "not allowed after reading the web: %s", what)
		return refuse("they did not want that after what you read")
	}
	return nil
}

// consentNotThisTime is the refusing half of the taint question. It is not
// "never ask again": the answer is about this act, after this page, and
// there is nothing standing to remember.
const consentNotThisTime = "Not this time"

// webSummary is the one line the person is asked about: what the ghost is
// about to do, in the shape they would say it, from the arguments it
// called with.
func webSummary(name string, args json.RawMessage) string {
	var a struct {
		Ghost string `json:"ghost"`
		Text  string `json:"text"`
		Name  string `json:"name"`
		Set   string `json:"set"`
		Reset bool   `json:"reset"`
	}
	json.Unmarshal(args, &a)
	switch name {
	case "tell":
		return fmt.Sprintf("tell %s: %q", a.Ghost, clip(a.Text, 120))
	case "intend":
		return fmt.Sprintf("take on %q", clip(a.Text, 120))
	case "resolve":
		return fmt.Sprintf("mark %s done", a.Name)
	case "set_aside":
		return fmt.Sprintf("stop watching %s", a.Name)
	case "style":
		if a.Reset {
			return "go back to its package's drawing style"
		}
		return fmt.Sprintf("draw in %q from now on", clip(a.Set, 120))
	default:
		// A [[tool]] server's verb, which the person has already been
		// asked about once by this name (act.go).
		return "use " + strings.ReplaceAll(name, "__", ".") + " with " + clip(argsLine(args), 120)
	}
}

// argsLine renders arbitrary tool arguments as one readable line, for a
// server tool whose schema the driver knows nothing about.
func argsLine(args json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(args, &m) != nil || len(m) == 0 {
		return "no arguments"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, ", ")
}
