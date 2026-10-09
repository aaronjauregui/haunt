package hermes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// A ghost's skills: routines hermes lists in the prompt and the model loads
// with skill_view when one applies (docs/dev/hermes.md, "Skills").
//
// Haunt's own skills and the ghost package's are copied into the profile,
// under SkillsDir, and named in skills.external_dirs. A copy rather than a
// path to the originals, because hermes may run in a container that sees
// only the profile (scripts/hermes-docker).
//
// The copy is read-only on disk. At the pinned hermes, skill_manage edits
// and deletes skills in external_dirs from a live turn (only the review
// fork is refused, in _background_review_write_guard), and the index even
// tells the model to patch a skill that has issues. A skill haunt ships is
// haunt's, so the directories are 0555: hermes's writes fail with a tool
// error, and `create` already refuses a name that exists. What the model
// learns goes to the profile's own skills/, which is its to change.
//
// Only Markdown is copied. Hermes does not import anything from a skill
// directory, but a script there is something a model with a shell could
// be told to run, and a ghost's skills are prose.

// SkillsDir is where haunt installs skills in a profile, relative to it.
// Everything under it is haunt's and rewritten on every start.
const SkillsDir = "haunt-skills"

// SkillSource is one set of skills to install. Its Name is the directory
// under SkillsDir, which hermes shows as the skills' category in its index.
type SkillSource struct {
	Name string
	// About is the category's line in the index.
	About string
	// FS holds one directory per skill, each with a SKILL.md.
	FS fs.FS
}

// installSkills rewrites SkillsDir from sources and reports whether any
// skill was installed. A skill name that an earlier source already
// installed is skipped: hermes would show one and load the other.
func installSkills(profile string, sources []SkillSource) (bool, error) {
	root := filepath.Join(profile, SkillsDir)
	if err := removeReadOnly(root); err != nil {
		return false, err
	}
	seen := map[string]bool{}
	for _, src := range sources {
		entries, err := fs.ReadDir(src.FS, ".")
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		dest := filepath.Join(root, src.Name)
		wrote := false
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || strings.HasPrefix(name, ".") || seen[name] {
				continue
			}
			if _, err := fs.Stat(src.FS, path.Join(name, "SKILL.md")); err != nil {
				continue
			}
			seen[name] = true
			wrote = true
			if err := copyMarkdown(src.FS, name, filepath.Join(dest, name)); err != nil {
				return false, err
			}
		}
		if wrote && src.About != "" {
			desc := "---\ndescription: " + yamlString(src.About) + "\n---\n"
			if err := os.WriteFile(filepath.Join(dest, "DESCRIPTION.md"), []byte(desc), 0o600); err != nil {
				return false, err
			}
		}
	}
	if len(seen) == 0 {
		return false, nil
	}
	return true, lockDown(root)
}

// copyMarkdown copies the .md files under dir in fsys to dest, skipping
// dotfiles and dot directories.
func copyMarkdown(fsys fs.FS, dir, dest string) error {
	return fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() || path.Ext(p) != ".md" {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dest, filepath.FromSlash(strings.TrimPrefix(p, dir)))
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o600)
	})
}

// lockDown makes everything under root read-only.
func lockDown(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		return os.Chmod(p, 0o444)
	})
	if err != nil {
		return err
	}
	// Deepest first, so each directory is still writable while its
	// children are changed.
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], 0o555); err != nil {
			return err
		}
	}
	return nil
}

// removeReadOnly removes root, which lockDown made read-only.
func removeReadOnly(root string) error {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.Chmod(p, 0o700)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.RemoveAll(root)
}

// yamlString quotes s for a one-line YAML scalar.
func yamlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ").Replace(s) + `"`
}

// Skill is one skill in a ghost's profile, for `haunt journal -skills`.
type Skill struct {
	Name        string
	Description string
	// Source is where it came from: "learned" (the model wrote it with
	// skill_manage), "hermes" (seeded or installed by hermes), or the name
	// of the SkillSource haunt installed it from.
	Source string
	Dir    string
	// Assumes lists the hermes tools a ghost does not have that the
	// skill's text names. A learned skill with any is one hermes's review
	// wrote for an agent with more tools than this one.
	Assumes []string
	// Deps is the front matter's `deps:`, the packages hermes installs
	// when the skill is viewed (tools/skills_tool.py skill_view) where
	// installs on demand are on. A ghost's profile has them off.
	Deps []string
}

// Skill sources that are not a SkillSource.
const (
	SkillLearned = "learned"
	SkillHermes  = "hermes"
)

// absentTools are the tools of disabledToolsets, as hermes names them
// (toolsets.resolve_toolset at the pinned commit). `patch` is left out:
// it is also skill_manage's own action, and an ordinary word.
var absentTools = regexp.MustCompile(`\b(terminal|process_manage|read_file|write_file|search_files|` +
	`browser_[a-z_]+|execute_code|delegate_task|web_search|web_extract|cronjob_manage|computer_use|` +
	`todo_list|vision_analyze|image_generate|text_to_speech|kanban_[a-z_]+|manage_connections|clarify)\b`)

// Skills lists the skills in a ghost's profile without changing anything:
// the profile's own skills/ directory, then what haunt installed.
func (c Config) Skills(ghost string) ([]Skill, error) {
	dir, err := c.ProfileDir(ghost)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("ghost %q has no hermes profile yet: %w", ghost, err)
	}
	local := filepath.Join(dir, "skills")
	fromHermes := hermesInstalled(local)
	var out []Skill
	err = walkSkills(local, func(s Skill) {
		s.Source = SkillLearned
		if fromHermes[filepath.Base(s.Dir)] {
			s.Source = SkillHermes
		}
		out = append(out, s)
	})
	if err != nil {
		return nil, err
	}
	root := filepath.Join(dir, SkillsDir)
	sources, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, src := range sources {
		if !src.IsDir() {
			continue
		}
		err := walkSkills(filepath.Join(root, src.Name()), func(s Skill) {
			s.Source = src.Name()
			out = append(out, s)
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// hermesInstalled is the skills hermes put in a profile's skills/: those
// in its bundled manifest (tools/skills_sync.py, "name:hash" lines) and
// its hub lock (tools/skill_usage.py, .hub/lock.json "installed").
func hermesInstalled(local string) map[string]bool {
	names := map[string]bool{}
	if data, err := os.ReadFile(filepath.Join(local, ".bundled_manifest")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if name, _, _ := strings.Cut(strings.TrimSpace(line), ":"); name != "" {
				names[name] = true
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(local, ".hub", "lock.json")); err == nil {
		var lock struct {
			Installed map[string]json.RawMessage `json:"installed"`
		}
		if json.Unmarshal(data, &lock) == nil {
			for name := range lock.Installed {
				names[name] = true
			}
		}
	}
	return names
}

// walkSkills calls fn for each SKILL.md under root, skipping the
// directories hermes's own scanner skips: dot directories (.archive, .hub
// …) and a skill's support directories.
func walkSkills(root string, fn func(Skill)) error {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != "SKILL.md" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := Skill{Name: filepath.Base(filepath.Dir(p)), Dir: filepath.Dir(p)}
		if head, _, ok := strings.Cut(strings.TrimPrefix(string(data), "---\n"), "\n---"); ok && strings.HasPrefix(string(data), "---\n") {
			var fm struct {
				Name, Description string
				Deps              any
			}
			if yaml.Unmarshal([]byte(head), &fm) == nil {
				if fm.Name != "" {
					s.Name = fm.Name
				}
				s.Description = fm.Description
				s.Deps = depNames(fm.Deps)
			}
		}
		seen := map[string]bool{}
		for _, t := range absentTools.FindAllString(string(data), -1) {
			if !seen[t] {
				seen[t] = true
				s.Assumes = append(s.Assumes, t)
			}
		}
		fn(s)
		// A skill's own subdirectories hold its references, not skills.
		return fs.SkipDir
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// depNames reads `deps:` as hermes does: one name or a list of them, blank
// entries dropped.
func depNames(v any) []string {
	var list []any
	switch d := v.(type) {
	case string:
		list = []any{d}
	case []any:
		list = d
	}
	var out []string
	for _, d := range list {
		if name := strings.TrimSpace(fmt.Sprint(d)); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// A ghost's toolsets and hermes tools, as hermes names them for the
// visibility conditions (model_tools.get_toolset_for_tool at the pinned
// commit): platform_toolsets.api_server with the MCP server filed under
// mcp-haunt.
var (
	ghostToolsets = []string{"memory", "session_search", "skills", "mcp-haunt"}
	ghostTools    = []string{"memory", "session_search", "skill_manage", "skill_view", "skills_list"}
)

// MCPToolName is the name hermes gives a tool the haunt MCP server
// offers (tools/mcp_tool_schema.py): prefixed, with anything outside
// [A-Za-z0-9_] folded to an underscore, dashes included.
func MCPToolName(tool string) string {
	b := []byte("mcp__haunt__" + tool)
	for i := len("mcp__haunt__"); i < len(b); i++ {
		c := b[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			b[i] = '_'
		}
	}
	return string(b)
}

// Unmet lists, for each skill in fsys, the requires_toolsets and
// requires_tools entries a ghost offering tools (named as the haunt MCP
// server names them) would not satisfy. Hermes hides such a skill from
// its index without a word, so a skill meant to use a declared tool
// server's tool, and spelling it wrong, would simply never be used.
// Skills with nothing unmet are left out.
func Unmet(fsys fs.FS, tools []string) (map[string][]string, error) {
	have := map[string]bool{}
	for _, t := range ghostTools {
		have[t] = true
	}
	for _, t := range tools {
		have[MCPToolName(t)] = true
	}
	entries, err := fs.ReadDir(fsys, ".")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, e := range entries {
		data, err := fs.ReadFile(fsys, path.Join(e.Name(), "SKILL.md"))
		if !e.IsDir() || err != nil {
			continue
		}
		head, _, ok := strings.Cut(strings.TrimPrefix(string(data), "---\n"), "\n---")
		if !ok || !strings.HasPrefix(string(data), "---\n") {
			continue
		}
		var fm struct {
			Metadata struct {
				Hermes struct {
					Toolsets []string `yaml:"requires_toolsets"`
					Tools    []string `yaml:"requires_tools"`
				}
			}
		}
		if yaml.Unmarshal([]byte(head), &fm) != nil {
			continue
		}
		var unmet []string
		for _, ts := range fm.Metadata.Hermes.Toolsets {
			if !slices.Contains(ghostToolsets, ts) {
				unmet = append(unmet, "toolset "+ts)
			}
		}
		for _, t := range fm.Metadata.Hermes.Tools {
			if !have[t] {
				unmet = append(unmet, "tool "+t)
			}
		}
		if len(unmet) > 0 {
			out[e.Name()] = unmet
		}
	}
	return out, nil
}
