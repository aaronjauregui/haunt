package hlog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the `[log]` section of the user config.
type Config struct {
	// Level is the floor for ordinary output: "error", "warn", "info"
	// (default) or "debug". Debug here means *every* topic, which is the
	// blunt instrument; Topics is the sharp one.
	Level string `toml:"level"`
	// Topics lists the subsystems whose debug output is wanted, e.g.
	// ["backend", "wayland"]. "all" enables everything.
	Topics []string `toml:"debug"`
}

// configPath is where the user's config lives. Deliberately a copy of the
// same few lines in internal/quiet rather than an import: a logging package
// that depends on half the driver cannot be used by the other half.
func configPath() (string, error) {
	if dir := os.Getenv("HAUNT_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "config.toml"), nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "haunt", "config.toml"), nil
}

// Load reads the `[log]` section. A missing file is not an error: the
// defaults are the shipping configuration.
func Load() (Config, error) {
	path, err := configPath()
	if err != nil {
		return Config{}, err
	}
	var root struct {
		Log Config `toml:"log"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	if err := toml.Unmarshal(data, &root); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return root.Log, nil
}

// Open builds the driver's logger from the config file and the
// environment, in that order of precedence — the environment wins, because
// it is what a person reaches for when something is wrong right now.
//
// A bad level or an unknown topic is reported rather than ignored: a
// misspelled HAUNT_DEBUG that silently produced no output would be
// indistinguishable from the bug you were chasing not happening.
func Open(w io.Writer) *Logger {
	cfg, cfgErr := Load()

	level := LevelInfo
	var bad []string
	if cfg.Level != "" {
		if lv, ok := ParseLevel(cfg.Level); ok {
			level = lv
		} else {
			bad = append(bad, "[log] level = "+cfg.Level)
		}
	}
	var topics []Topic
	for _, name := range cfg.Topics {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if !KnownTopic(name) {
			bad = append(bad, "[log] debug topic "+name)
			continue
		}
		topics = append(topics, Topic(name))
	}
	if s := os.Getenv("HAUNT_LOG"); s != "" {
		if _, ok := ParseLevel(s); !ok {
			bad = append(bad, "HAUNT_LOG="+s)
		}
	}
	for _, t := range parseTopics(os.Getenv("HAUNT_DEBUG")) {
		if !KnownTopic(string(t)) {
			bad = append(bad, "HAUNT_DEBUG topic "+string(t))
		}
	}

	l := FromEnv(w, level, topics)
	if cfgErr != nil {
		l.Warn(Session, "reading the log config: %v", cfgErr)
	}
	for _, b := range bad {
		l.Warn(Session, "ignoring %s: not one of %s", b, topicList())
	}
	// Say so when the log is not at its defaults. Debug output that is on
	// for a reason nobody remembers — a stale environment variable, a line
	// left in the config — is otherwise indistinguishable from the driver
	// having become chatty on its own.
	if desc := l.Describe(); desc != LevelInfo.String() {
		l.Info(Session, "log: %s", desc)
	}
	return l
}

func topicList() string {
	names := make([]string, 0, len(Topics)+1)
	for _, t := range Topics {
		names = append(names, string(t))
	}
	names = append(names, "all")
	return strings.Join(names, ", ")
}
