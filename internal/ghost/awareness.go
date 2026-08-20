package ghost

import (
	"encoding/json"
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
// and no model until the moment of speaking, and none of it ever leaves the
// machine.
//
// A little of it now outlives the session, under `_haunt/session` and
// nowhere else. It has to: a recurrence is about an hour of history and a
// morning is about yesterday, so a ghost started at login every day could
// notice neither. What is kept is the recurrence table, the cooldowns and
// when a window was last seen — with titles reduced to identities by the
// session package, so the file on disk can count sightings and says nothing
// about what was being read.

// sessionStateKey holds what the session model keeps across a restart.
const sessionStateKey = internalKeyPrefix + "session"

// sessionSaveEvery is how often the model's state is written while the
// ghost runs. Sightings accumulate without producing facts, so waiting for
// shutdown would lose an afternoon to a crash; writing on every poll would
// be a store write every three seconds for a table that rarely changes.
const sessionSaveEvery = time.Minute

// startWatching begins window awareness if it is not already running.
// Called at boot and again whenever the user asks for it (see execWatch),
// since a capability granted mid-session has to take effect without a
// restart — the loop it starts is the thing being consented to.
func (g *Ghost) startWatching() {
	if !g.watching.CompareAndSwap(false, true) {
		return
	}
	g.go_(hlog.Screen, "window awareness", func() {
		defer g.watching.Store(false)
		g.watchWindows()
	})
}

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
	// Published on the ghost so teardown can write it out on the way down.
	// The loop is the only caller of Observe, but Snapshot is read from
	// another goroutine, so both go through sessMu.
	g.sessMu.Lock()
	g.sess = session.Restore(g.awareness.Session, g.loadSessionState(), time.Now())
	facts := g.sess
	g.sessMu.Unlock()
	defer func() {
		g.saveSessionState()
		g.sessMu.Lock()
		g.sess = nil
		// Nothing is being watched any more, so nothing is known: a reply
		// after the capability is withdrawn must not report the last window
		// the ghost happened to catch before it stopped looking.
		g.seenWindow = WhyWindow{}
		g.sessMu.Unlock()
	}()
	t := time.NewTicker(windowPoll)
	defer t.Stop()
	save := time.NewTicker(sessionSaveEvery)
	defer save.Stop()
	for {
		select {
		case <-g.done:
			return
		case <-save.C:
			g.saveSessionState()
			continue
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

		// Remembered before the ignore check has a chance to blank it, so
		// that what the ghost knew and what it may act on stay one answer
		// rather than two: an ignored application is not seen at all.
		if g.ignoredApp(win.AppID) {
			// Not even the fact that it changed: a password manager
			// gaining focus is itself information the ghost does not need.
			// The ignore list applies *before* aggregation, or an ignored
			// application's time turns up in a duration statistic even
			// though its name never did.
			last = desktop.Window{}
			g.noteWindow("", "")
			g.sessMu.Lock()
			facts.Away(time.Now())
			g.sessMu.Unlock()
			continue
		}
		// The session model hears every poll, changed or not: a long
		// stretch is noticed by nothing happening.
		now := time.Now()
		g.noteWindow(win.AppID, win.Title)
		g.sessMu.Lock()
		noticed := facts.Observe(win.AppID, win.Title, now)
		g.sessMu.Unlock()
		for _, f := range noticed {
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

// loadSessionState reads what the last run left. A missing or unreadable
// value is simply a fresh model: this is an enhancement to awareness, and
// nothing about it is worth failing a boot over.
func (g *Ghost) loadSessionState() session.State {
	raw, ok, err := storeGet(g.store, sessionStateKey)
	if err != nil || !ok {
		return session.State{}
	}
	var st session.State
	if json.Unmarshal([]byte(raw), &st) != nil {
		return session.State{}
	}
	return st
}

// saveSessionState writes the model's state, if there is one to write.
//
// Called from the watch loop on a ticker and from teardown, which is why
// the model is reachable from the Ghost at all: the loop's own exit happens
// after `defer g.store.Close()` has already run (defers are LIFO and the
// done channel is closed later than the store is opened), so a save on the
// way out of the loop would always be a write to a closed database. The
// shutdown write has to happen on the main path, while the store is still
// there.
func (g *Ghost) saveSessionState() {
	g.sessMu.Lock()
	defer g.sessMu.Unlock()
	if g.sess == nil {
		return
	}
	b, err := json.Marshal(g.sess.Snapshot())
	if err != nil {
		return
	}
	if err := g.store.Set(sessionStateKey, string(b)); err != nil {
		g.log.Warn(hlog.Screen, "saving what the day looked like: %v", err)
	}
}

// execWatch is the user asking to be watched.
//
// Window awareness is the one capability that could never be asked for in
// the moment, because the thing that needs it is a background poller and a
// permission popup at login about nothing in particular is the pattern
// asking-in-the-moment exists to replace. The way out is that the question
// does not have to come from the ghost: a menu entry offering to watch is
// the person raising it themselves, which is a better moment than either
// the install sheet or a poller could ever produce.
//
// So the driver only honours this when the person has just done something.
// A brain cannot emit it from a timer and make a permission question appear
// out of nowhere; the causality clock that rations initiative answers this
// question too, and answers it driver-side, where a brain cannot claim to
// have been provoked.
func (g *Ghost) execWatch() {
	if !g.respondingToUser() {
		g.log.Debug(hlog.Screen, "watch intent dropped: nobody asked for it")
		return
	}
	// A user-initiated action that quietly does nothing is worse than a
	// refusal, so the two terminal cases say so rather than leaving
	// somebody clicking a menu entry that appears to be broken. The
	// ordinary "not now" is not one of them: that answer was just given, on
	// screen, and repeating it back is nagging.
	switch {
	case !g.declares("window"):
		g.sayBriefly(g.manifest.Ghost.Name + " was not built to watch what you are doing.")
		return
	case g.opts.Refused != nil && g.opts.Refused("window"):
		g.sayBriefly("You asked me never to watch. Undo that with: haunt perms grant window")
		return
	}
	if !g.ensureCapability("window") {
		return
	}
	// Granted, possibly a moment ago: the loop exited at boot because there
	// was nothing to watch with, and this is what starts it without a
	// restart. Already running is the common case and costs nothing.
	g.startWatching()
}

// sayBriefly puts a line from the driver in the balloon. Used where a
// person asked for something the driver cannot do: the answer belongs on
// screen, next to the menu entry they just picked, rather than in a log
// nobody is reading.
func (g *Ghost) sayBriefly(text string) {
	// Nowhere to show it is nowhere to read it — the same test canAsk makes
	// before putting a question up.
	if g.surface == nil {
		g.log.Info(hlog.Screen, "%s", text)
		return
	}
	g.showPaged(text)
	g.lingerBalloon(g.balloonGen(), noticeLinger)
}

// noticeLinger is long enough to read a sentence with a command in it.
const noticeLinger = 8 * time.Second
