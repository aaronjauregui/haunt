package ghost

import "testing"

// TestIgnoredApps: the ghost must never report a password manager, and the
// careless case — a user who never wrote an ignore list — has to be the
// safe one.
func TestIgnoredApps(t *testing.T) {
	cfg, err := loadAwarenessConfig()
	if err != nil {
		t.Fatal(err)
	}
	g := &Ghost{awareness: cfg}

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
	g := &Ghost{awareness: AwarenessConfig{Ignore: []string{"steam"}}}
	if !g.ignoredApp("Steam") {
		t.Error("the configured app is not ignored")
	}
	if g.ignoredApp("1password") {
		t.Error("the defaults came back after being replaced")
	}
}
