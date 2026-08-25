package ghost

import (
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/session"
)

// TestIgnoredApps: the ghost must never report a password manager, and the
// careless case — a user who never wrote an ignore list — has to be the
// safe one.
func TestIgnoredApps(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	g := &Ghost{awareness: cfg.Awareness}

	for _, app := range []string{
		"1Password", "1password-gui", "org.keepassxc.KeePassXC",
		"bitwarden", "pinentry-gtk", "gnome-keyring-prompt",
	} {
		if !g.ignoredApp(app) {
			t.Errorf("%q is watched by default; it should not be", app)
		}
	}
	for _, app := range []string{"foot", "firefox", "code", ""} {
		if g.ignoredApp(app) {
			t.Errorf("%q is ignored by default; it should not be", app)
		}
	}
}

// TestIgnoreListReplaces: a user who writes their own list gets exactly
// that list, since the defaults would otherwise be impossible to shrink.
func TestIgnoreListReplaces(t *testing.T) {
	g := &Ghost{awareness: config.AwarenessConfig{Ignore: []string{"steam"}}}
	if !g.ignoredApp("Steam") {
		t.Error("the configured app is not ignored")
	}
	if g.ignoredApp("1password") {
		t.Error("the defaults came back after being replaced")
	}
}

// TestWatchNeedsSomebodyToHaveAsked: this is the whole reason window
// awareness can be asked for at all. The consent rule is that a question
// follows something the person did — so a brain emitting `watch` from a
// timer must not be able to conjure a permission prompt out of nothing, and
// the driver decides that rather than believing the brain.
func TestWatchNeedsSomebodyToHaveAsked(t *testing.T) {
	asked := false
	g := &Ghost{
		log:      hlog.Discard(),
		store:    &memStore{m: map[string]string{}},
		manifest: &Manifest{Ghost: GhostSection{Name: "wisp", Capabilities: []string{"window"}}},
		opts: Options{
			Granted: func(string) bool { return false },
			Consent: func(string, bool) error { asked = true; return nil },
		},
	}

	// Out of the blue: dropped without a word.
	g.execWatch()
	if asked {
		t.Error("a watch intent nobody provoked put a permission question on screen")
	}
	if g.watching.Load() {
		t.Error("window awareness started without consent")
	}
}

// TestWatchSaysWhyItCannot: a menu entry the user picked must not silently
// do nothing, so the two terminal cases answer in the balloon. (There is no
// surface here, so this only asserts the decision, not the pixels — what
// matters is that neither path reaches the consent question.)
func TestWatchSaysWhyItCannot(t *testing.T) {
	asked := false
	newGhost := func(caps []string, refused bool) *Ghost {
		g := &Ghost{
			log:      hlog.Discard(),
			store:    &memStore{m: map[string]string{}},
			manifest: &Manifest{Ghost: GhostSection{Name: "wisp", Capabilities: caps}},
			opts: Options{
				Granted: func(string) bool { return false },
				Refused: func(string) bool { return refused },
				Consent: func(string, bool) error { asked = true; return nil },
			},
		}
		g.markPrompted() // the user just picked the menu entry
		return g
	}

	// A ghost that never shipped asking to watch cannot be talked into it.
	newGhost([]string{"llm"}, false).execWatch()
	if asked {
		t.Error("a capability the manifest never declared was raised anyway")
	}
	// And "never ask again" means never, not until the next menu.
	newGhost([]string{"window"}, true).execWatch()
	if asked {
		t.Error("a standing refusal was asked about again")
	}
}

// TestSessionStateSurvivesTheGhost: the facts worth having are the ones
// about more than this minute, and a companion restarted at login every day
// could see none of them while nothing was written down.
func TestSessionStateSurvivesTheGhost(t *testing.T) {
	s := &memStore{m: map[string]string{}}
	g := &Ghost{log: hlog.Discard(), store: s}

	cfg := session.Config{RecurCount: 3, RecurGap: time.Minute, RecurWindow: time.Hour}
	m := session.New(cfg)
	at := time.Now().Add(-30 * time.Minute)
	for i := 0; i < 2; i++ {
		m.Observe("firefox", "build failed — CI", at)
		m.Observe("nvim", "session.go", at.Add(2*time.Minute))
		at = at.Add(5 * time.Minute)
	}
	g.sess = m
	g.saveSessionState()

	// A new run, reading what the last one left.
	restored := session.Restore(cfg, g.loadSessionState(), time.Now())
	var recurred bool
	for _, f := range restored.Observe("firefox", "build failed — CI", time.Now()) {
		recurred = recurred || f.Kind == session.Recurrence
	}
	if !recurred {
		t.Error("the recurrence did not survive the restart")
	}

	// And what went to disk is countable rather than readable.
	raw := s.m[sessionStateKey]
	for _, leak := range []string{"build failed", "session.go", "firefox"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the stored state contains %q: %s", leak, raw)
		}
	}
}

// TestMissingSessionStateIsJustAFreshModel: awareness is an enhancement,
// and nothing about it is worth failing a boot over.
func TestMissingSessionStateIsJustAFreshModel(t *testing.T) {
	s := &memStore{m: map[string]string{sessionStateKey: "{not json"}}
	g := &Ghost{log: hlog.Discard(), store: s}
	if st := g.loadSessionState(); len(st.Seen) != 0 || !st.LastSeen.IsZero() {
		t.Errorf("a corrupt snapshot was believed: %+v", st)
	}
}
