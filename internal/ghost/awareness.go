package ghost

import (
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/desktop"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/session"
)

// Knowing what you are doing. The compositor already tracks which window
// has focus and what it is called, and window titles carry far more than
// they look like they do: file names, branch names, error text, what is
// playing, which PR is open. That is most of the signal behind "still
// fighting that compiler error?" — and it costs nothing per observation, so
// it can run continuously where a vision model could not.
//
// It is also the most invasive thing the driver does, so it is gated three
// ways: the manifest must declare the capability, the user must grant it,
// and applications on the ignore list are never reported at all. A ghost
// that watches your screen has to be a ghost you chose.

// windowPoll is how often the focused window is checked. Frequent enough
// to notice a context switch, rare enough to be free.
const windowPoll = 3 * time.Second

// The middle layer. A brain given the raw stream is told which window has
// focus every three seconds and is expected to be interesting about it, but
// nothing interesting lives in one observation: the signal is the shape over
// time. internal/session aggregates the same stream into durations,
// alternations and recurrences, and those are published as their own events
// beside the raw ones.
//
// It is counters and timestamps — nothing per observation worth measuring,
// and no model until the moment of speaking. Nothing is written to disk: a
// log of somebody's working day should not outlive the reason for having
// it, and it never leaves the machine at all.

// watchWindows publishes a window event whenever the focused application or
// its title changes. Runs until the ghost stops.
func (g *Ghost) watchWindows() {
	if !g.capabilityActive("window") {
		return
	}
	if g.desktop == nil || g.desktop.Mechanism() == "none" {
		g.log.Info(hlog.Screen, "window awareness: no compositor IPC; nothing to watch")
		return
	}
	g.log.Info(hlog.Screen, "watching the focused window via %s", g.desktop.Mechanism())

	var last desktop.Window
	var complained bool
	facts := session.New(g.awareness.Session)
	t := time.NewTicker(windowPoll)
	defer t.Stop()
	for {
		select {
		case <-g.done:
			return
		case <-t.C:
		}

		// Checked every poll rather than once: a person who withdraws the
		// window capability while the ghost is running means now, and this
		// is the loop that would otherwise keep looking.
		if !g.capabilityActive("window") {
			g.log.Info(hlog.Screen, "window awareness stopped: the capability was withdrawn")
			return
		}

		win, err := g.desktop.ActiveWindow()
		if err != nil {
			if !complained {
				complained = true
				g.log.Warn(hlog.Screen, "window awareness: %v", err)
			}
			continue
		}
		complained = false

		if g.ignoredApp(win.AppID) {
			// Not even the fact that it changed: a password manager
			// gaining focus is itself information the ghost does not need.
			// The ignore list applies *before* aggregation, or an ignored
			// application's time turns up in a duration statistic even
			// though its name never did.
			last = desktop.Window{}
			facts.Away(time.Now())
			continue
		}
		// The session model hears every poll, changed or not: a long
		// stretch is noticed by nothing happening.
		now := time.Now()
		for _, f := range facts.Observe(win.AppID, win.Title, now) {
			g.log.Info(hlog.Screen, "noticed: %s", f.Summary)
			g.publish(proto.Event{
				Type: proto.EventSession,
				Name: string(f.Kind),
				Text: f.Summary,
				Session: &proto.SessionFact{
					Kind:    string(f.Kind),
					App:     f.App,
					Title:   f.Title,
					Minutes: f.Minutes,
					Count:   f.Count,
					Summary: f.Summary,
				},
			})
		}

		if win.AppID == last.AppID && win.Title == last.Title {
			continue
		}
		last = win
		if win.AppID == "" && win.Title == "" {
			continue // a bare desktop
		}
		g.publish(proto.Event{
			Type: proto.EventWindow,
			Name: win.AppID,
			Text: win.Title,
		})
	}
}

// ignoredApp reports whether an application is on the never-watch list.
// Matching is case-insensitive and by substring, so "1password" covers
// "1Password" and "1password-gui" without the user thinking about it.
func (g *Ghost) ignoredApp(appID string) bool {
	if appID == "" {
		return false
	}
	lower := strings.ToLower(appID)
	for _, ign := range g.awareness.Ignore {
		if ign != "" && strings.Contains(lower, strings.ToLower(ign)) {
			return true
		}
	}
	return false
}
