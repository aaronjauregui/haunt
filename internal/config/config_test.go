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

[backend.vision]
base_url = "http://localhost:11434/v1"
model = "qwen2.5vl"

[hermes]
first_token_timeout = 45

# Sections from before hermes was the brain are ignored, not refused, so an
# old config keeps working.
[memory]
consolidations_per_day = 1

[backend.llm]
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
	if f.Backend.Vision.Model != "qwen2.5vl" {
		t.Errorf("[backend.vision] model = %q", f.Backend.Vision.Model)
	}
	if got := f.Hermes.FirstTokenTimeout().Seconds(); got != 45 {
		t.Errorf("[hermes] first_token_timeout = %vs, want 45", got)
	}
	// Filled in at load, so nothing downstream re-derives them.
	if f.Backend.Budget.VisionRequestsPerHour == 0 {
		t.Error("the backend budget defaults were not applied")
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

// Shared folders come out absolute and clean, with ~ expanded, so nothing
// downstream has to know the person wrote them by hand.
func TestSharedFoldersAreExpanded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	write(t, `
[files]
unprompted = "skills"

[files.roots]
notes = "~/notes"
home = "~"
recipes = "/srv/recipes/../recipes/"
`)
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"notes":   filepath.Join(home, "notes"),
		"home":    home,
		"recipes": "/srv/recipes",
	}
	for name, dir := range want {
		if got := f.Files.Roots[name]; got != dir {
			t.Errorf("root %s = %q, want %q", name, got, dir)
		}
	}
	if got := strings.Join(f.Files.RootNames(), " "); got != "home notes recipes" {
		t.Errorf("root names = %q, want them sorted", got)
	}
	if got := f.Files.UnpromptedMode(); got != FilesSkills {
		t.Errorf("unprompted = %q, want skills", got)
	}
}

// Files start closed: with nothing set, no folder is shared and a ghost
// may not read on a turn the person did not start.
func TestFilesDefaultToNothingAndNever(t *testing.T) {
	t.Setenv("HAUNT_CONFIG_DIR", t.TempDir())
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Files.Roots) != 0 {
		t.Errorf("roots = %v, want none", f.Files.Roots)
	}
	if got := f.Files.UnpromptedMode(); got != FilesNever {
		t.Errorf("unprompted = %q, want never", got)
	}
	if f.Backend.Budget.UnpromptedFilePerHour != 4 {
		t.Errorf("unprompted_file_per_hour = %d, want the default 4",
			f.Backend.Budget.UnpromptedFilePerHour)
	}
}

// A folder that is not there, like an unmounted drive, is not a mistake in
// the config: it may be there tomorrow.
func TestAMissingFolderIsNotAConfigError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-mounted")
	write(t, "[files.roots]\nusb = \""+missing+"\"\n")
	f, err := Load()
	if err != nil {
		t.Fatalf("a missing folder was refused: %v", err)
	}
	if f.Files.Roots["usb"] != missing {
		t.Errorf("root usb = %q, want %q", f.Files.Roots["usb"], missing)
	}
}

// Each mistake in [files] is a startup error naming the file, rather than
// a ghost that cannot find a folder the person thinks they shared.
func TestABadFilesSectionNamesItself(t *testing.T) {
	for name, body := range map[string]string{
		"a capital in a name":    "[files.roots]\nNotes = \"/srv/notes\"\n",
		"a slash in a name":      "[files.roots]\n\"a/b\" = \"/srv/notes\"\n",
		"a leading dash":         "[files.roots]\n-notes = \"/srv/notes\"\n",
		"a name too long":        "[files.roots]\n" + strings.Repeat("a", 33) + " = \"/srv/notes\"\n",
		"an empty name":          "[files.roots]\n\"\" = \"/srv/notes\"\n",
		"a relative path":        "[files.roots]\nnotes = \"notes\"\n",
		"someone else's home":    "[files.roots]\nnotes = \"~bob/notes\"\n",
		"an empty path":          "[files.roots]\nnotes = \"\"\n",
		"a duplicate root":       "[files.roots]\nnotes = \"/srv/a\"\nnotes = \"/srv/b\"\n",
		"a bad unprompted value": "[files]\nunprompted = \"sometimes\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			write(t, body)
			_, err := Load()
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), "config.toml") {
				t.Errorf("error does not name the file: %v", err)
			}
		})
	}
}

// A FilesConfig built by hand, without Load, still reads as closed.
func TestUnpromptedFilesDefaultToNever(t *testing.T) {
	for set, want := range map[string]string{
		"": FilesNever, "nonsense": FilesNever,
		"never": FilesNever, "skills": FilesSkills, "any": FilesAny,
	} {
		if got := (FilesConfig{Unprompted: set}).UnpromptedMode(); got != want {
			t.Errorf("unprompted %q = %q, want %q", set, got, want)
		}
	}
}

// Without [appearance], every ghost is drawn at its own scale.
func TestAppearanceDefaultsToOne(t *testing.T) {
	t.Setenv("HAUNT_CONFIG_DIR", t.TempDir())
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := f.ShellScale("wisp"); got != 1 {
		t.Errorf("ShellScale with no config = %v, want 1", got)
	}
}

// A ghost's own entry replaces the global factor rather than multiplying
// it; the other ghosts keep the global one, and a ghost entry that leaves
// scale out falls back to it too.
func TestAppearancePerGhostReplacesGlobal(t *testing.T) {
	write(t, `
[appearance]
scale = 1.5

[appearance.ghost.wisp]
scale = 0.8

[appearance.ghost."my ghost"]
scale = 2

[appearance.ghost.quiet]
`)
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for ghost, want := range map[string]float64{
		"wisp":     0.8,
		"my ghost": 2,
		"quiet":    1.5,
		"other":    1.5,
	} {
		if got := f.ShellScale(ghost); got != want {
			t.Errorf("ShellScale(%q) = %v, want %v", ghost, got, want)
		}
	}
}

// A per-ghost entry works without a global one.
func TestAppearancePerGhostAlone(t *testing.T) {
	write(t, "[appearance.ghost.wisp]\nscale = 0.5\n")
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := f.ShellScale("wisp"); got != 0.5 {
		t.Errorf("ShellScale(wisp) = %v, want 0.5", got)
	}
	if got := f.ShellScale("other"); got != 1 {
		t.Errorf("ShellScale(other) = %v, want 1", got)
	}
}

// An explicit scale of zero or less is refused, naming the file and the
// table, with a quoted name written the way the person has to write it.
func TestAnImpossibleScaleIsRefused(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"[appearance]\nscale = 0\n", "[appearance] scale 0"},
		{"[appearance]\nscale = -1\n", "[appearance] scale -1"},
		{"[appearance.ghost.wisp]\nscale = 0\n", "[appearance.ghost.wisp] scale 0"},
		{"[appearance.ghost.\"my ghost\"]\nscale = -0.5\n", `[appearance.ghost."my ghost"] scale -0.5`},
	} {
		write(t, tc.body)
		_, err := Load()
		if err == nil {
			t.Errorf("%q loaded", tc.body)
			continue
		}
		path, _ := Path()
		if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: error %q does not name %s and %s", tc.body, err, path, tc.want)
		}
	}
}

// [model.ghost.<name>] changes the model for that ghost alone, key by key,
// and sampling field by field.
func TestConfigOverride(t *testing.T) {
	write(t, `
[model]
base_url = "http://127.0.0.1:8080/v1"
name = "gemma-4"
context_length = 32768
thinking = "low"

[model.sampling]
temperature = 0.7
min_p = 0.05

[model.ghost.wisp]
name = "x"
fold = true

[model.ghost.wisp.sampling]
temperature = 1.1
`)
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	wisp := f.ModelFor("wisp")
	if wisp.Name != "x" {
		t.Errorf("wisp's name = %q, want x", wisp.Name)
	}
	if wisp.BaseURL != "http://127.0.0.1:8080/v1" || wisp.ContextLength != 32768 || wisp.Thinking != "low" {
		t.Errorf("wisp lost the troupe's keys: %+v", wisp)
	}
	if !wisp.Fold() {
		t.Error("wisp's fold = true was lost")
	}
	if s := wisp.Sampling; s.Temperature == nil || *s.Temperature != 1.1 || s.MinP == nil || *s.MinP != 0.05 {
		t.Errorf("wisp's sampling = %+v", s)
	}
	other := f.ModelFor("ember")
	if other.Name != "gemma-4" || other.Fold() {
		t.Errorf("ember = %q fold %v, want the troupe's gemma-4, unfolded on a local server", other.Name, other.Fold())
	}
	if s := other.Sampling; s.Temperature == nil || *s.Temperature != 0.7 {
		t.Errorf("ember's sampling = %+v", s)
	}
	if !other.IsLocal() {
		t.Error("a loopback base_url is not local")
	}
}

// A missing [model] is the zero config: nothing to reach, nothing assumed.
func TestNoModelIsEmpty(t *testing.T) {
	write(t, "[quiet]\nhours = []\n")
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c := f.ModelFor("wisp"); c.BaseURL != "" || c.Name != "" || c.Sampling.Temperature != nil {
		t.Errorf("ModelFor with no [model] = %+v", c)
	}
}

func TestABadModelSectionNamesItself(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"[model]\ntrust = \"house\"\n", `[model] trust "house"`},
		{"[model]\nthinking = \"lots\"\n", `[model] thinking "lots"`},
		{"[model]\nbase_url = \"127.0.0.1:8080\"\n", `[model] base_url "127.0.0.1:8080"`},
		{"[model]\nstall_timeout = -1\n", `[model] stall_timeout -1`},
		{"[model.ghost.\"my ghost\"]\ntrust = \"x\"\n", `[model.ghost."my ghost"] trust "x"`},
	} {
		write(t, tc.body)
		_, err := Load()
		if err == nil {
			t.Errorf("%q loaded", tc.body)
			continue
		}
		path, _ := Path()
		if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: error %q does not name %s and %s", tc.body, err, path, tc.want)
		}
	}
}
