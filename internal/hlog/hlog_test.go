package hlog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shipping configuration: warnings and errors and the session's own
// account, and none of the running commentary.
func TestDefaultsAreQuiet(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelInfo, nil)
	l.Error(Session, "broke")
	l.Warn(AI, "degraded")
	l.Info(Place, "placed")
	l.Debug(Wayland, "motion 1,2")
	l.Debug(Curiosity, "spent one")

	got := buf.String()
	for _, want := range []string{"error: broke", "warn: degraded", "haunt: placed"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q from:\n%s", want, got)
		}
	}
	if strings.Contains(got, "motion") || strings.Contains(got, "spent one") {
		t.Errorf("debug output escaped into the default log:\n%s", got)
	}
}

// Debug is per topic, because someone chasing a rendering bug should not
// have to read the memory index narrating itself.
func TestDebugIsSelectedByTopic(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelInfo, []Topic{Wayland})
	l.Debug(Wayland, "surface created")
	l.Debug(Memory, "recalled 3")

	got := buf.String()
	if !strings.Contains(got, "[wayland] surface created") {
		t.Errorf("the asked-for topic is missing:\n%s", got)
	}
	if strings.Contains(got, "recalled") {
		t.Errorf("an unasked-for topic was printed:\n%s", got)
	}
	if !l.Enabled(Wayland, LevelDebug) || l.Enabled(Memory, LevelDebug) {
		t.Error("Enabled disagrees with what was written")
	}
}

func TestAllTopicsAndLevelDebug(t *testing.T) {
	for _, l := range []*Logger{
		New(&bytes.Buffer{}, LevelInfo, []Topic{"all"}),
		New(&bytes.Buffer{}, LevelDebug, nil),
	} {
		for _, tp := range Topics {
			if !l.Enabled(tp, LevelDebug) {
				t.Errorf("%s: topic %s is off", l.Describe(), tp)
			}
		}
	}
}

// A quieter level really is quieter: errors only.
func TestLevelFloor(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, LevelError, nil)
	l.Warn(AI, "degraded")
	l.Info(Session, "placed")
	l.Error(Session, "broke")
	if got := buf.String(); strings.Contains(got, "degraded") || strings.Contains(got, "placed") {
		t.Errorf("level error printed more than errors:\n%s", got)
	} else if !strings.Contains(got, "broke") {
		t.Errorf("level error swallowed an error:\n%s", got)
	}
}

// The environment is what a person reaches for when something is wrong now.
func TestFromEnv(t *testing.T) {
	t.Setenv("HAUNT_LOG", "debug")
	if l := FromEnv(&bytes.Buffer{}, LevelInfo, nil); !l.Enabled(Memory, LevelDebug) {
		t.Error("HAUNT_LOG=debug did not raise the level")
	}

	t.Setenv("HAUNT_LOG", "")
	t.Setenv("HAUNT_DEBUG", "ai, wayland")
	l := FromEnv(&bytes.Buffer{}, LevelInfo, nil)
	if !l.Enabled(AI, LevelDebug) || !l.Enabled(Wayland, LevelDebug) {
		t.Error("HAUNT_DEBUG list was not honoured")
	}
	if l.Enabled(Memory, LevelDebug) {
		t.Error("HAUNT_DEBUG enabled a topic nobody asked for")
	}

	// The thing everyone types first.
	t.Setenv("HAUNT_DEBUG", "1")
	if l := FromEnv(&bytes.Buffer{}, LevelInfo, nil); !l.Enabled(Memory, LevelDebug) {
		t.Error("HAUNT_DEBUG=1 should mean everything")
	}
}

// The switches that predate topics still work: they are in the docs and in
// people's shell history.
func TestLegacyDebugSwitches(t *testing.T) {
	t.Setenv("HAUNT_DEBUG", "")
	t.Setenv("HAUNT_AI_DEBUG", "1")
	l := FromEnv(&bytes.Buffer{}, LevelInfo, nil)
	if !l.Enabled(Prompt, LevelDebug) || !l.Enabled(AI, LevelDebug) {
		t.Error("HAUNT_AI_DEBUG=1 no longer traces prompts")
	}
	if l.Enabled(Wayland, LevelDebug) {
		t.Error("HAUNT_AI_DEBUG turned on the compositor firehose too")
	}

	t.Setenv("HAUNT_AI_DEBUG", "")
	t.Setenv("HAUNT_WAYLAND_DEBUG", "1")
	if l := FromEnv(&bytes.Buffer{}, LevelInfo, nil); !l.Enabled(Wayland, LevelDebug) {
		t.Error("HAUNT_WAYLAND_DEBUG=1 no longer traces the compositor")
	}
}

// A misspelled topic must be answered, not ignored: silence would be
// indistinguishable from the bug you were chasing failing to happen.
func TestUnknownTopicIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"),
		[]byte("[log]\nlevel = \"warn\"\ndebug = [\"wayland\", \"balloonz\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAUNT_CONFIG_DIR", dir)
	t.Setenv("HAUNT_DEBUG", "")
	t.Setenv("HAUNT_LOG", "")
	t.Setenv("HAUNT_AI_DEBUG", "")
	t.Setenv("HAUNT_WAYLAND_DEBUG", "")

	var buf bytes.Buffer
	l := Open(&buf)
	if !strings.Contains(buf.String(), "balloonz") {
		t.Errorf("the typo was accepted in silence:\n%s", buf.String())
	}
	if !l.Enabled(Wayland, LevelDebug) {
		t.Error("the valid topic beside the typo was dropped")
	}
	if l.Enabled(Session, LevelInfo) {
		t.Error("[log] level = warn was not applied")
	}
}

// Config first, environment on top: the file is the standing preference and
// the environment is this session.
func TestEnvironmentOverridesConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml"),
		[]byte("[log]\nlevel = \"error\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAUNT_CONFIG_DIR", dir)
	t.Setenv("HAUNT_LOG", "info")
	t.Setenv("HAUNT_DEBUG", "")
	t.Setenv("HAUNT_AI_DEBUG", "")
	t.Setenv("HAUNT_WAYLAND_DEBUG", "")

	if l := Open(&bytes.Buffer{}); !l.Enabled(Session, LevelInfo) {
		t.Error("HAUNT_LOG did not override the config file")
	}
}

func TestRecorderKeepsLines(t *testing.T) {
	l, lines := Recorder()
	l.Debug(Memory, "recalled %d", 3)
	l.Info(Session, "up")
	got := lines()
	if len(got) != 2 || !strings.Contains(got[0], "recalled 3") {
		t.Errorf("recorded %q", got)
	}
}

// A subsystem told to trace itself must be able to: asking for the output
// and being allowed to write it have to be the same decision, or the filter
// on the way out silently undoes the switch that turned it on.
func TestWithAddsTopicsAndSharesTheDestination(t *testing.T) {
	var buf bytes.Buffer
	base := New(&buf, LevelInfo, nil)
	derived := base.With(Prompt)

	base.Debug(Prompt, "not asked for")
	derived.Debug(Prompt, "traced")
	base.Info(Session, "shared destination")

	got := buf.String()
	if strings.Contains(got, "not asked for") {
		t.Errorf("the base logger wrote a topic it does not have:\n%s", got)
	}
	if !strings.Contains(got, "traced") {
		t.Errorf("the derived logger's topic was filtered out:\n%s", got)
	}
	if !strings.Contains(got, "shared destination") {
		t.Errorf("the two loggers are not writing to the same place:\n%s", got)
	}
	if derived.Enabled(Memory, LevelDebug) {
		t.Error("With enabled more than it was given")
	}
}
