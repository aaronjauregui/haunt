package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/aaronjauregui/haunt/internal/backend"
	"github.com/aaronjauregui/haunt/internal/hlog"
)

// The web, as a ghost reaches it: two tools, and the rules around them.
//
// Looking something up brings words into the turn which nobody in the room
// wrote. Everything here follows from that. The backend fetches and
// renders (internal/backend/web.go); this file decides whether the call
// may happen at all, and what the rest of the turn may do afterwards.
//
// Three gates, in order, each as the trust table says for where the model
// runs (trust.go):
//
//   - The `web` capability. On cloud it is asked for in the balloon like
//     any other, and a search nobody asked for cannot put the question —
//     there is nothing on screen to make sense of it — so it is refused
//     instead. On local it needs no question; a refusal for good still
//     stands.
//   - On a turn nobody started: on cloud the unprompted policy, then quiet
//     hours and a smaller ration. The driver cannot see which skill the
//     model loaded, so "only where a skill says to" is approximated by the
//     kinds of event a skill acts on. On local only quiet hours and the
//     ration apply.
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
//
// The web is not the only way in: a file in a folder the person shared is
// a stranger's words as much as a page is. So the turn records where what
// it read came from (turn.tainted), and the gate asks by source. After a
// file, the web verbs themselves are gated too, since a query or a URL is
// a way for a file's contents to leave the machine.

// taintSource is a set of the places a turn has read from. The source
// matters as well as the fact: what may leave the machine afterwards
// depends on what came in (verb.leaves).
type taintSource uint8

const (
	// taintWeb is a search result list or a page.
	taintWeb taintSource = 1 << iota
	// taintFile is text from a file in a folder the person shared.
	taintFile

	// taintAny is every source: the verbs that reach past the person are
	// gated whatever was read.
	taintAny = taintWeb | taintFile
)

// what is the source as the object of "read", in the words the person is
// asked in: "the web", "a file", or "the web and a file".
func (s taintSource) what() string {
	switch {
	case s&taintWeb != 0 && s&taintFile != 0:
		return "the web and a file"
	case s&taintFile != 0:
		return "a file"
	default:
		return "the web"
	}
}

// taintingReads is the ghost's verbs whose results can carry somebody
// else's words, by the source they taint a turn with.
var taintingReads = map[string]taintSource{
	"search":       taintWeb,
	"read_page":    taintWeb,
	"read_file":    taintFile,
	"search_files": taintFile,
}

// UntrustedRead reports whether the ghost's verb tool brings somebody
// else's words into a turn, and from where, as the object of "read": "the
// web" or "a file". It is the taint table read from outside the turn, for
// `haunt history`, which checks what the ghost saved to memory against
// what these returned. A result that carried no
// such words, such as a refusal, shares none with anything saved, so the
// table needs no finer test than the name.
func UntrustedRead(tool string) (from string, ok bool) {
	src, ok := taintingReads[tool]
	if !ok {
		return "", false
	}
	return src.what(), true
}

// unpromptedKinds are the events a ghost on cloud may look something up
// about without being asked, under the default policy.
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
	return g.readGate("web", g.backend.Unprompted(), func() error {
		// The call charges the general ration itself, so an unprompted
		// look spends both.
		if err := g.backend.SpendUnpromptedSearch(); err != nil {
			return refuse("%v", err)
		}
		return nil
	})
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
	g.taint(taintWeb)
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
	g.taint(taintWeb)
	return g.readable(webRef("read_page"), page.Excerpt(a.From)), nil
}

// Taint, applied.
//
// gatedAfter is the whole of the rule on cloud, in one place rather than
// in fifteen decisions: which sources, once read, put this call to the
// person. The trust table narrows it for a model on this machine
// (gateAfterReading in trust.go).
// A verb that reaches past the person is gated after anything was read,
// and `style` is one only when it is being changed rather than read. A
// verb that leaves the machine is gated after a file, so that what a file
// said cannot become a query or a URL sent to somebody else's server; it
// stays free after the web, because a search built from a search result
// sends nothing that was not already out there.
func gatedAfter(name string, sideEffect, leaves bool, args json.RawMessage) taintSource {
	switch {
	case sideEffecting(name, sideEffect, args):
		return taintAny
	case leaves:
		return taintFile
	}
	return 0
}

// sideEffecting reports whether a call reaches past the person: a verb
// marked so, with `style` counting only as a set or a reset.
func sideEffecting(name string, sideEffect bool, args json.RawMessage) bool {
	if !sideEffect {
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

// allowAfterReading is the gate a call passes once the turn has read
// something from one of the sources in after. Until it has, it is not a
// gate at all.
//
// On a person's turn the effect is put to them, in the balloon, in the
// words of what is about to happen and of what was read. Nothing is
// recorded either way: this is not a grant, it is one answer about one
// act, and the next one asks again. On a turn nobody started there is
// nobody to ask, and the act is simply refused — a background loop that
// has just read somebody else's words is the worst possible moment to take
// somebody's word for anything.
//
// The state is read from the turn itself: decisions are applied one at a
// time on the turn's own goroutine, so what a call sees is what that turn
// read, and nothing else's.
func (g *Ghost) allowAfterReading(t *turn, what string, after taintSource) error {
	tainted := t.taintedBy()
	if tainted&after == 0 {
		return nil
	}
	read := tainted.what()
	if !t.person {
		return refuse("not after reading %s on your own; ask them first", read)
	}
	if !g.canAskNow() {
		return refuse("not after reading %s, without asking them first", read)
	}
	g.log.Info(hlog.Session, "asking whether %s may %s after reading %s", g.manifest.Ghost.Name, what, read)
	answer := g.ask(g.manifest.Ghost.Name+" read "+read+" just now and wants to "+what+". Allow?",
		consentAllow, consentNotThisTime)
	if answer != consentAllow {
		g.log.Info(hlog.Session, "not allowed after reading %s: %s", read, what)
		return refuse("they did not want that after what you read")
	}
	return nil
}

// consentNotThisTime is the refusing half of the taint question. It is not
// "never ask again": the answer is about this act, after this reading, and
// there is nothing standing to remember.
const consentNotThisTime = "Not this time"

// actSummary is the one line the person is asked about: what the ghost is
// about to do, in the shape they would say it, from the arguments it
// called with.
func actSummary(name string, args json.RawMessage) string {
	var a struct {
		Query string `json:"query"`
		URL   string `json:"url"`
		Ghost string `json:"ghost"`
		Text  string `json:"text"`
		Name  string `json:"name"`
		Set   string `json:"set"`
		Reset bool   `json:"reset"`
	}
	json.Unmarshal(args, &a)
	switch name {
	case "search":
		return fmt.Sprintf("search the web for %q", clip(a.Query, 120))
	case "read_page":
		return "open " + clip(a.URL, 200)
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
