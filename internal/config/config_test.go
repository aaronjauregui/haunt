package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAUNT_CONFIG_DIR", dir)
}

// One read gets every section. This is the whole point of the package:
// five subsystems used to open the same file for their own slice of it.
func TestOneLoadCarriesEverySection(t *testing.T) {
	write(t, `
[quiet]
hours = ["22:00-08:00"]

[animation]
reduced_motion = true

[awareness]
dwell = "10m"
away = "45m"

[curiosity]
actions_per_day = 3

[ai.vision]
base_url = "http://localhost:11434/v1"
model = "qwen2.5vl"

[hermes]
first_token_timeout = 45

# Sections from before hermes was the brain are ignored, not refused, so an
# old config keeps working.
[memory]
consolidations_per_day = 1

[ai.llm]
model = "qwen2.5"
`)
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Quiet.Hours) != 1 {
		t.Errorf("quiet hours = %v", f.Quiet.Hours)
	}
	if !f.Animation.ReducedMotion {
		t.Error("[animation] reduced_motion was lost")
	}
	if f.Awareness.Session.Dwell.Minutes() != 10 {
		t.Errorf("dwell = %v, want it parsed into the session config", f.Awareness.Session.Dwell)
	}
	if perGhost, room := f.Allowances(); perGhost != 3 || room != -1 {
		t.Errorf("allowances = (%d, %d), want (3, -1)", perGhost, room)
	}
	if f.AI.Vision.Model != "qwen2.5vl" {
		t.Errorf("[ai.vision] model = %q", f.AI.Vision.Model)
	}
	if got := f.Hermes.FirstTokenTimeout().Seconds(); got != 45 {
		t.Errorf("[hermes] first_token_timeout = %vs, want 45", got)
	}
	// Filled in at load, so nothing downstream re-derives them.
	if f.AI.Budget.VisionRequestsPerHour == 0 {
		t.Error("the AI budget defaults were not applied")
	}
}

// A missing file is the shipping configuration, not an error.
func TestNoFileIsDefaults(t *testing.T) {
	t.Setenv("HAUNT_CONFIG_DIR", t.TempDir())
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if perGhost, _ := f.Allowances(); perGhost != DefaultActionsPerDay {
		t.Errorf("allowances = %d, want the default %d", perGhost, DefaultActionsPerDay)
	}
	// The careless case has to be the safe one: a user who never wrote an
	// ignore list still gets the password managers.
	if len(f.Awareness.Ignore) == 0 {
		t.Error("the default ignore list is empty")
	}
}

// One file, so one error, naming the file a person has to go and edit.
func TestAMalformedFileNamesItself(t *testing.T) {
	write(t, "[curiosity\nactions_per_day = 3\n")
	_, err := Load()
	if err == nil {
		t.Fatal("a malformed config parsed")
	}
	if !strings.Contains(err.Error(), "config.toml") {
		t.Errorf("error does not name the file: %v", err)
	}
}

// The user's own ignore list replaces the defaults rather than adding to
// them, since they would otherwise be impossible to shrink.
func TestIgnoreListReplacesTheDefaults(t *testing.T) {
	write(t, "[awareness]\nignore = [\"steam\"]\n")
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Awareness.Ignore) != 1 || f.Awareness.Ignore[0] != "steam" {
		t.Errorf("ignore = %v, want exactly the configured list", f.Awareness.Ignore)
	}
}

// A late_hour early enough to make the whole day late is refused, with the
// reason, rather than quietly making the ghost remark all afternoon.
func TestAnImpossibleLateHourIsRefused(t *testing.T) {
	write(t, "[awareness]\nlate_hour = 3\n")
	if _, err := Load(); err == nil {
		t.Fatal("late_hour = 3 was accepted")
	}
}
