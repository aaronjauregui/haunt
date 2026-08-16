package ghost

import (
	"fmt"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/desktop"
	"github.com/aaronjauregui/haunt/internal/proto"
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

// watchWindows publishes a window event whenever the focused application or
// its title changes. Runs until the ghost stops.
func (g *Ghost) watchWindows() {
	if !g.capabilityActive("window") {
		return
	}
	if g.desktop == nil || g.desktop.Mechanism() == "none" {
		g.opts.Log("haunt: window awareness: no compositor IPC; nothing to watch")
		return
	}
	g.opts.Log(fmt.Sprintf("haunt: watching the focused window via %s", g.desktop.Mechanism()))

	var last desktop.Window
	var complained bool
	t := time.NewTicker(windowPoll)
	defer t.Stop()
	for {
		select {
		case <-g.done:
			return
		case <-t.C:
		}

		win, err := g.desktop.ActiveWindow()
		if err != nil {
			if !complained {
				complained = true
				g.opts.Log(fmt.Sprintf("haunt: window awareness: %v", err))
			}
			continue
		}
		complained = false

		if g.ignoredApp(win.AppID) {
			// Not even the fact that it changed: a password manager
			// gaining focus is itself information the ghost does not need.
			last = desktop.Window{}
			continue
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
