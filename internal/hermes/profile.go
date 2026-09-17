package hermes

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/aaronjauregui/haunt/plugins"
)

// Config is the [hermes] section of the haunt config: where hermes is and
// what the ghosts' profiles are made from. Everything is optional; the
// zero value runs `hermes` from PATH against the person's own hermes home.
type Config struct {
	// Command is the hermes executable (argv). Default ["hermes"].
	Command []string `toml:"command"`
	// Home is the hermes root the ghost profiles live under. Default
	// $HERMES_HOME, else ~/.hermes — the person's own, so `hermes -p
	// haunt-<ghost>` reaches the same profile the ghost runs on.
	Home string `toml:"home"`
	// Model, when set, is written into each profile's config.yaml as its
	// model block. Left unset, the profile keeps whatever the person
	// configured in it (`hermes -p haunt-<ghost> model`), and haunt never
	// touches it. Hermes needs a model with at least a 64K context window.
	Model *ModelConfig `toml:"model"`
	// ToolTimeout bounds one tool call from hermes, in seconds, which is
	// how long an `ask` may wait on the person. Default 600.
	ToolTimeout int `toml:"tool_timeout"`
	// Memory selects the memory provider: "" for hermes's built-in memory,
	// or "haunt" to read and correct beliefs through haunt's plugin
	// (plugins/hermes/haunt).
	MemoryProvider string `toml:"memory_provider"`
}

// ModelConfig is the model block of a profile.
type ModelConfig struct {
	Provider      string `toml:"provider" yaml:"provider,omitempty"`
	BaseURL       string `toml:"base_url" yaml:"base_url,omitempty"`
	Default       string `toml:"name" yaml:"default,omitempty"`
	ContextLength int    `toml:"context_length" yaml:"context_length,omitempty"`
}

// Root is the hermes home the profiles live under.
func (c Config) Root() (string, error) {
	if c.Home != "" {
		return expandHome(c.Home)
	}
	if h := os.Getenv("HERMES_HOME"); h != "" {
		// A HERMES_HOME that is itself a profile belongs to whoever set it;
		// the ghosts go beside it, under the same root.
		if filepath.Base(filepath.Dir(h)) == "profiles" {
			return filepath.Dir(filepath.Dir(h)), nil
		}
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".hermes"), nil
}

func (c Config) command() []string {
	if len(c.Command) > 0 {
		return c.Command
	}
	return []string{"hermes"}
}

func (c Config) toolTimeout() int {
	if c.ToolTimeout > 0 {
		return c.ToolTimeout
	}
	return 600
}

func expandHome(p string) (string, error) {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, rest), nil
	}
	return p, nil
}

// profileNameRE is hermes's own rule for profile names.
var profileNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ProfileName is the hermes profile a ghost runs on. Prefixed, so a ghost
// can never land on a profile the person made for themselves, and folded
// to the characters hermes accepts.
func ProfileName(ghost string) string {
	var b strings.Builder
	b.WriteString("haunt-")
	for _, r := range strings.ToLower(ghost) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := b.String()
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

// Profile is a ghost's hermes profile on disk.
type Profile struct {
	Ghost string
	// Dir is the profile directory, which is HERMES_HOME for the gateway.
	Dir string
	// Key is the API server's bearer key, and Port where it listens.
	Key  string
	Port int
}

// URL is where the profile's API server answers.
func (p Profile) URL() string {
	return "http://127.0.0.1:" + strconv.Itoa(p.Port)
}

// ProfileSpec is everything EnsureProfile writes that comes from haunt.
type ProfileSpec struct {
	Ghost   string
	Persona Persona
	// Serve is the argv hermes runs to reach the ghost's tools: the haunt
	// binary and `mcp-serve --ghost <name>`.
	Serve []string
	// Env is passed to that process. Hermes gives MCP servers a filtered
	// environment (XDG_* survive, HAUNT_* do not), so anything haunt's own
	// socket lookup depends on has to be named here.
	Env map[string]string
}

// OpenProfile reads an existing profile's key and port without changing
// anything, for the CLI.
func (c Config) OpenProfile(ghost string) (Profile, error) {
	root, err := c.Root()
	if err != nil {
		return Profile{}, err
	}
	p := Profile{Ghost: ghost, Dir: filepath.Join(root, "profiles", ProfileName(ghost))}
	env, err := readEnv(filepath.Join(p.Dir, ".env"))
	if err != nil {
		return Profile{}, fmt.Errorf("ghost %q has no hermes profile yet: %w", ghost, err)
	}
	p.Key = env["API_SERVER_KEY"]
	p.Port, _ = strconv.Atoi(env["API_SERVER_PORT"])
	if p.Key == "" || p.Port == 0 {
		return Profile{}, fmt.Errorf("the hermes profile at %s has no API server key or port", p.Dir)
	}
	return p, nil
}

// EnsureProfile creates or refreshes a ghost's profile.
//
// What is haunt's is rewritten every time: SOUL.md, and the keys in
// config.yaml that make the profile a ghost's — the API server, the
// toolset restriction, the haunt MCP server. Everything else in
// config.yaml is left as the person set it, the model above all, and
// MEMORY.md, USER.md and the session database are never touched: they are
// the ghost's life, not its configuration.
func (c Config) EnsureProfile(spec ProfileSpec) (Profile, error) {
	root, err := c.Root()
	if err != nil {
		return Profile{}, err
	}
	name := ProfileName(spec.Ghost)
	if !profileNameRE.MatchString(name) {
		return Profile{}, fmt.Errorf("cannot make a hermes profile name from %q", spec.Ghost)
	}
	p := Profile{Ghost: spec.Ghost, Dir: filepath.Join(root, "profiles", name)}
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return Profile{}, err
	}

	// The key and the port are chosen once and kept, so the CLI can reach a
	// running ghost's gateway and a restart does not move it.
	envPath := filepath.Join(p.Dir, ".env")
	env, err := readEnv(envPath)
	if err != nil && !os.IsNotExist(err) {
		return Profile{}, err
	}
	if env == nil {
		env = map[string]string{}
	}
	changed := false
	if len(env["API_SERVER_KEY"]) < 32 {
		var k [32]byte
		if _, err := rand.Read(k[:]); err != nil {
			return Profile{}, err
		}
		env["API_SERVER_KEY"] = hex.EncodeToString(k[:])
		changed = true
	}
	if port, _ := strconv.Atoi(env["API_SERVER_PORT"]); port == 0 {
		port, err := freePort()
		if err != nil {
			return Profile{}, err
		}
		env["API_SERVER_PORT"] = strconv.Itoa(port)
		changed = true
	}
	if env["API_SERVER_HOST"] != "127.0.0.1" {
		env["API_SERVER_HOST"] = "127.0.0.1"
		changed = true
	}
	if env["HAUNT_GHOST"] != spec.Ghost {
		// For the memory provider plugin, which runs inside hermes and has
		// to find the ghost's socket.
		env["HAUNT_GHOST"] = spec.Ghost
		changed = true
	}
	if changed {
		if err := writeEnv(envPath, env); err != nil {
			return Profile{}, err
		}
	}
	p.Key = env["API_SERVER_KEY"]
	p.Port, _ = strconv.Atoi(env["API_SERVER_PORT"])

	if err := writeIfChanged(filepath.Join(p.Dir, "SOUL.md"), []byte(Soul(spec.Persona)), 0o600); err != nil {
		return Profile{}, err
	}

	// Hermes seeds a new profile with its bundled skills (mail, documents,
	// a code agent …) and lists them in the prompt, and a ghost with that
	// list believes it can do those things. The marker limits the seed to
	// hermes's essentials. Only before the first seed: once skills/ exists
	// the choice is the person's (`hermes skills opt-in`).
	if _, err := os.Stat(filepath.Join(p.Dir, "skills")); os.IsNotExist(err) {
		marker := filepath.Join(p.Dir, noBundledSkills)
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			note := "Written by haunt: a ghost is seeded with hermes's essential skills only.\n" +
				"Delete this file and run `hermes skills opt-in --sync` for the rest.\n"
			if err := os.WriteFile(marker, []byte(note), 0o600); err != nil {
				return Profile{}, err
			}
		}
	}
	if c.MemoryProvider == "haunt" {
		if err := installPlugin(filepath.Join(p.Dir, "plugins", "haunt")); err != nil {
			return Profile{}, fmt.Errorf("installing the haunt memory provider: %w", err)
		}
	}
	if err := c.writeConfig(filepath.Join(p.Dir, "config.yaml"), spec); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// installPlugin writes the memory provider into the profile's own plugin
// directory, where hermes looks for user plugins. Rewritten every start, so
// the plugin always matches the driver talking to it.
func installPlugin(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return fs.WalkDir(plugins.MemoryProvider, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := plugins.MemoryProvider.ReadFile(path)
		if err != nil {
			return err
		}
		return writeIfChanged(filepath.Join(dir, filepath.Base(path)), data, 0o600)
	})
}

// disabledToolsets is subtracted last, whatever else enables them: a later
// hermes widening an explicit toolset list must not hand a ghost a shell.
var disabledToolsets = []string{
	"terminal", "file", "browser", "code_execution", "delegation", "web",
	"cronjob", "computer_use", "todo", "vision", "image_gen", "tts",
	"kanban", "connections", "clarify",
}

// noBundledSkills is hermes's opt-out marker for bundled skills
// (tools/skills_sync.py NO_BUNDLED_SKILLS_MARKER).
const noBundledSkills = ".no-bundled-skills"

// configVersion is hermes's config schema version (`_config_version` in
// hermes_cli/config_defaults.py) at the commit docs/hermes-brain.md pins.
const configVersion = 45

func (c Config) writeConfig(path string, spec ProfileSpec) error {
	doc := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if doc == nil {
			doc = map[string]any{}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	section := func(key string) map[string]any {
		if m, ok := doc[key].(map[string]any); ok {
			return m
		}
		m := map[string]any{}
		doc[key] = m
		return m
	}

	// A config.yaml without a schema version reads to hermes as one too old
	// to migrate, which it warns about on every start. What haunt writes is
	// current as of the hermes it was checked against; a version already
	// there is hermes's to keep.
	if _, set := doc["_config_version"]; !set {
		doc["_config_version"] = configVersion
	}
	if c.Model != nil {
		doc["model"] = c.Model
	}

	// The API server is the turn channel. Host and port come from .env,
	// where the CLI can read them too.
	platforms := section("platforms")
	platforms["api_server"] = map[string]any{"enabled": true}

	// Only these, and the haunt server's own toolset (named after its key
	// under mcp_servers). No clarify: it is inert on the API server, and
	// haunt's `ask` is the way to put a question.
	pt := section("platform_toolsets")
	pt["api_server"] = []string{"memory", "session_search", "skills", "haunt"}
	agent := section("agent")
	agent["disabled_toolsets"] = disabledToolsets

	// Tool search hides MCP tools behind a lookup the model has to think of
	// using; a ghost's verbs must be in front of it.
	tools := section("tools")
	tools["tool_search"] = map[string]any{"enabled": "off"}

	memory := section("memory")
	if _, set := memory["write_approval"]; !set {
		// A companion that asks permission to remember your cat's name is
		// not a companion. Only defaulted: the person may want otherwise.
		memory["write_approval"] = false
	}
	if c.MemoryProvider != "" {
		memory["provider"] = c.MemoryProvider
	} else if memory["provider"] == "haunt" {
		delete(memory, "provider")
	}

	servers := section("mcp_servers")
	serve := map[string]any{
		"command":         spec.Serve[0],
		"args":            spec.Serve[1:],
		"timeout":         c.toolTimeout(),
		"connect_timeout": 30,
	}
	if len(spec.Env) > 0 {
		serve["env"] = spec.Env
	}
	servers["haunt"] = serve

	out, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	header := []byte("# Written by haunt for ghost " + spec.Ghost + ". The api_server, toolset,\n" +
		"# tool_search and mcp_servers.haunt settings are rewritten on every start;\n" +
		"# everything else (the model above all) is yours to change.\n")
	return writeIfChanged(path, append(header, out...), 0o600)
}

func writeIfChanged(path string, data []byte, mode os.FileMode) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == string(data) {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readEnv parses a dotenv file: KEY=VALUE lines, # comments, optional
// quotes.
func readEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	env := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		} else {
			v = strings.Trim(v, "'")
		}
		env[strings.TrimSpace(k)] = v
	}
	return env, sc.Err()
}

// writeEnv rewrites a dotenv file, keeping the order and any lines it does
// not own, since the person may keep their provider keys in it too.
func writeEnv(path string, env map[string]string) error {
	var lines []string
	seen := map[string]bool{}
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			k, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "export "), "=")
			k = strings.TrimSpace(k)
			if ok && !strings.HasPrefix(k, "#") {
				if v, have := env[k]; have {
					line = k + "=" + v
					seen[k] = true
				}
			}
			lines = append(lines, line)
		}
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		if !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, k+"="+env[k])
	}
	return writeIfChanged(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// freePort asks the kernel for a loopback port nobody is using.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}
