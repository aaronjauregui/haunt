package ghost

import (
	"encoding/json"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// "Why did you say that?"
//
// A ghost says things whose reasons are invisible, and a companion you
// cannot interrogate is one you end up either over-trusting or quietly
// distrusting. So every turn on the model keeps a record of what went into
// it and what came of it: what provoked it (the person's words or the
// event envelope), what the ghost could see on screen, which decisions it
// made, and whether it chose to say nothing. The conversation itself is in
// the transcript (transcript.go); `haunt history -why` matches the two up.
//
// Two deliberate limits:
//
//   - It is conversation content, so it lives in the ghost's own store and
//     never goes near a session recording (internal/bus), which is a
//     debugging artefact people share.
//   - The ring is short. The honest scope of the question is "that thing
//     you just said", not an audit log going back to installation.

const (
	// WhyStoreKey holds the trace, and is exported for `haunt history`.
	WhyStoreKey = internalKeyPrefix + "why"
	// whyKept is how many turns keep their reasons: enough that anything
	// still near the bottom of `haunt history` can still be asked about.
	whyKept = 12
)

// Why is the account of one turn.
type Why struct {
	Time time.Time `json:"time"`
	// User is what provoked it: the person's words, or the event envelope.
	User string `json:"user,omitempty"`
	// SelfPrompt says the ghost was woken by an event rather than spoken
	// to, so User is an envelope and not something the person said.
	SelfPrompt bool   `json:"self_prompt,omitempty"`
	Reply      string `json:"reply"`
	// Suppressed says the model chose silence (stay_silent): nothing
	// reached the balloon, and the record says so rather than leaving a
	// turn that seems to have vanished.
	Suppressed bool `json:"suppressed,omitempty"`
	// Acted is the decisions made in the course of the turn, in order.
	// When a ghost has *done* something, "why did you say that" and "what
	// did you do" are the same question, and the answer should not have to
	// be pieced together from the journal.
	Acted []string `json:"acted,omitempty"`
	// Window is what the ghost could see of the screen when it answered,
	// recorded only while the window capability is actually granted. It is
	// the context the ghost had, not necessarily context it used.
	Window WhyWindow `json:"window,omitempty"`
}

// WhyWindow is the focused window at the moment of a reply.
type WhyWindow struct {
	App   string `json:"app,omitempty"`
	Title string `json:"title,omitempty"`
}

// Empty reports whether nothing about the screen was known.
func (w WhyWindow) Empty() bool { return w.App == "" && w.Title == "" }

// noteWhy records the reasons behind a reply, oldest entries falling off
// the end.
func (g *Ghost) noteWhy(w Why) {
	raw, _, err := g.store.Get(WhyStoreKey)
	if err != nil {
		g.log.Warn(hlog.Store, "reading the reply trace: %v", err)
		return
	}
	var kept []Why
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &kept); err != nil {
			// A corrupt trace is not worth failing a conversation over;
			// start it again rather than lose the reply that caused it.
			g.log.Warn(hlog.Store, "the reply trace was unreadable; starting a new one")
			kept = nil
		}
	}
	kept = append(kept, w)
	if len(kept) > whyKept {
		kept = kept[len(kept)-whyKept:]
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return
	}
	if err := g.store.Set(WhyStoreKey, string(b)); err != nil {
		g.log.Warn(hlog.Store, "saving the reply trace: %v", err)
	}
}

// DecodeWhy parses stored traces, oldest first.
func DecodeWhy(raw string) ([]Why, error) {
	var out []Why
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// currentWindow is what the watch loop last saw, or the zero value when
// nothing is being watched.
func (g *Ghost) currentWindow() WhyWindow {
	g.sessMu.Lock()
	defer g.sessMu.Unlock()
	return g.seenWindow
}

// noteWindow records what the watch loop is looking at, so a reply can say
// what the ghost could see when it gave it.
func (g *Ghost) noteWindow(app, title string) {
	g.sessMu.Lock()
	g.seenWindow = WhyWindow{App: app, Title: title}
	g.sessMu.Unlock()
}
