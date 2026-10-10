// Package skill hosts skills in the agentskills format: a directory with
// a SKILL.md whose front matter names and describes it. Haunt's own
// skills are embedded; a ghost's learned ones live under its directory
// and the model writes them. The host renders the index the prompt
// carries, serves a skill's body on demand, and curates learned skills
// that go unused.
//
// The front matter is YAML between two `---` lines:
//
//	---
//	name: look-it-up
//	description: "Asked to look up, search, or check something online"
//	metadata:
//	  haunt:
//	    requires_tools: [search, read_page]
//	---
//
// A skill whose required tools are not all available is hidden: it is
// not in the index, View does not find it, and List leaves it out. That
// is how a ghost without the web stops being told how to search it.
//
// The learned directory holds one directory per skill, an archive/
// directory the curator moves unused skills into, and a small file of
// when each learned skill was last viewed. A learned skill unseen for 14
// days is stale; after 30 it is archived. Haunt's own skills are never
// curated or overwritten.
//
// See docs/ghosts/persona-and-skills.md for the format.
package skill

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Skill is one skill as the host sees it. Dir is the skill's directory:
// a path inside the embedded FS for haunt's own, an absolute path on
// disk for a learned one.
type Skill struct {
	Name        string
	Description string
	Dir         string
	Learned     bool
	Stale       bool
}

// Host serves skills to the brain.
type Host interface {
	Index() string // names and 60-char descriptions
	View(name string) (string, error)
	Write(name, body string) error // learned skills only
	Curate(now time.Time) error
	List() []Skill
}

const (
	// IndexDescription is how many characters of a description the index
	// shows. It is all the model reads before choosing to view a skill,
	// so a description says when the skill applies in that much.
	IndexDescription = 60
	// StaleAfter is how long a learned skill goes unviewed before the
	// index marks it stale.
	StaleAfter = 14 * 24 * time.Hour
	// ArchiveAfter is how long a learned skill goes unviewed before the
	// curator moves it to the archive.
	ArchiveAfter = 30 * 24 * time.Hour
	// MaxBody is the largest SKILL.md Write accepts. A skill is a page of
	// prose loaded into context, not a manual.
	MaxBody = 16 << 10

	// ArchiveDir is the learned directory's archive, and so a name no
	// learned skill may take.
	ArchiveDir = "archive"
	// seenFile records when each learned skill was last viewed, as a JSON
	// object of name to RFC 3339 time. It is a dotfile so it is never read
	// as a skill.
	seenFile = ".seen.json"
)

// Front is a SKILL.md's front matter, as far as haunt reads it.
type Front struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Metadata    struct {
		Haunt struct {
			RequiresTools []string `yaml:"requires_tools,omitempty"`
		} `yaml:"haunt,omitempty"`
	} `yaml:"metadata,omitempty"`
}

// RequiresTools is the tools the skill cannot be followed without.
func (f Front) RequiresTools() []string { return f.Metadata.Haunt.RequiresTools }

// Parse splits a SKILL.md into its front matter and body. It refuses a
// file without front matter, or one whose front matter has no name or
// description, since the index has nothing to show for it.
func Parse(data []byte) (Front, string, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return Front{}, "", errors.New("no front matter: a skill starts with a line of ---, then name and description, then ---")
	}
	head, body, ok := strings.Cut(text[len("---\n"):], "\n---\n")
	if !ok {
		head, ok = strings.CutSuffix(text[len("---\n"):], "\n---")
		if !ok {
			return Front{}, "", errors.New("front matter is not closed with a line of ---")
		}
	}
	var f Front
	if err := yaml.Unmarshal([]byte(head), &f); err != nil {
		return Front{}, "", fmt.Errorf("front matter: %v", err)
	}
	if f.Name == "" || f.Description == "" {
		return Front{}, "", errors.New("front matter needs a name and a description")
	}
	return f, strings.TrimLeft(body, "\n"), nil
}

var validName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidName reports whether name is a skill name in the agentskills
// sense: lowercase letters, digits and single hyphens, at most 64
// characters. The name is also the skill's directory.
func ValidName(name string) bool {
	return len(name) <= 64 && validName.MatchString(name)
}

// toolRef finds a backticked identifier, optionally written as a call.
var toolRef = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_.-]*)(\\([^`]*\\))?`")

// callVerb is the end of the text before a backticked name that makes it
// a tool: "call `x`", "invoke the `x`".
var callVerb = regexp.MustCompile(`(?i)\b(call|calls|calling|invoke|invokes|invoking|run|runs|running)\s+(the\s+)?$`)

// toolNoun is the start of the text after a backticked name that makes it
// a tool: "the `x` tool".
var toolNoun = regexp.MustCompile(`^\s+tools?\b`)

// foreignTools are tool names other agent harnesses give a model, which a
// model may carry into a skill it writes. None is a haunt tool, so a
// skill naming one tells the ghost to use something it has not got.
var foreignTools = map[string]bool{
	"bash": true, "shell": true, "run_shell": true, "terminal": true,
	"execute_code": true, "write_file": true, "edit_file": true,
	"web_search": true, "web_extract": true, "web_fetch": true,
	"session_search": true, "skill_manage": true, "skills_list": true,
	"delegate_task": true, "send_message": true, "cronjob": true,
	"vision_analyze": true, "image_generate": true, "text_to_speech": true,
}

// ToolRefs lists, in order of first appearance, the names in text that
// are written as tools: a backticked name that is called (`x(...)`,
// "call `x`"), named a tool ("the `x` tool"), shaped like a server's tool
// (`server__tool`), or one of the tool names other harnesses use. A
// backticked parameter such as `delay_ms` is none of these, so a skill
// may name its arguments freely.
func ToolRefs(text string) []string {
	var out []string
	for _, m := range toolRef.FindAllStringSubmatchIndex(text, -1) {
		name := text[m[2]:m[3]]
		before, after := text[max(0, m[0]-24):m[0]], text[m[1]:]
		isTool := m[4] >= 0 ||
			callVerb.MatchString(before) ||
			toolNoun.MatchString(after) ||
			strings.Contains(strings.Trim(name, "_"), "__") ||
			foreignTools[name]
		if isTool && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// Open loads the embedded skills and the learned ones under learnedDir,
// hiding any whose required tools are not in toolsAvailable. An embedded
// skill that does not parse, or whose name is not its directory's, is an
// error: it is haunt's own and wrong in the build. A learned one is
// skipped, so a skill the person broke by hand does not stop the ghost.
// A learned skill with the name of an embedded one is skipped too.
// learnedDir need not exist; Write creates it.
func Open(embedded fs.FS, learnedDir string, toolsAvailable []string) (Host, error) {
	h := &host{
		embedded: embedded,
		dir:      learnedDir,
		tools:    slices.Clone(toolsAvailable),
		now:      time.Now,
		seen:     map[string]time.Time{},
	}
	if err := h.load(); err != nil {
		return nil, err
	}
	return h, nil
}

type host struct {
	mu       sync.Mutex
	embedded fs.FS
	dir      string
	tools    []string
	now      func() time.Time
	skills   map[string]*entry
	seen     map[string]time.Time
}

type entry struct {
	Skill
	front  Front
	body   string
	hidden bool
}

func (h *host) available(tool string) bool { return slices.Contains(h.tools, tool) }

func (h *host) visible(f Front) bool {
	for _, t := range f.RequiresTools() {
		if !h.available(t) {
			return false
		}
	}
	return true
}

func (h *host) load() error {
	h.skills = map[string]*entry{}
	if h.embedded != nil {
		entries, err := fs.ReadDir(h.embedded, ".")
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for _, d := range entries {
			if !d.IsDir() || strings.HasPrefix(d.Name(), ".") {
				continue
			}
			data, err := fs.ReadFile(h.embedded, path.Join(d.Name(), "SKILL.md"))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			f, body, err := Parse(data)
			if err != nil {
				return fmt.Errorf("skill %s: %w", d.Name(), err)
			}
			if f.Name != d.Name() || !ValidName(f.Name) {
				return fmt.Errorf("skill %s: name %q is not its directory's", d.Name(), f.Name)
			}
			h.skills[f.Name] = &entry{
				Skill:  Skill{Name: f.Name, Description: f.Description, Dir: d.Name()},
				front:  f,
				body:   body,
				hidden: !h.visible(f),
			}
		}
	}
	if h.dir == "" {
		return nil
	}
	if data, err := os.ReadFile(filepath.Join(h.dir, seenFile)); err == nil {
		var raw map[string]time.Time
		if json.Unmarshal(data, &raw) == nil {
			h.seen = raw
		}
	}
	entries, err := os.ReadDir(h.dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	now := h.now()
	for _, d := range entries {
		name := d.Name()
		if !d.IsDir() || name == ArchiveDir || !ValidName(name) || h.skills[name] != nil {
			continue
		}
		e, err := h.readLearned(name)
		if err != nil {
			continue
		}
		h.skills[name] = e
		e.Stale = now.Sub(h.lastSeen(e)) > StaleAfter
	}
	return nil
}

// readLearned reads one learned skill from disk.
func (h *host) readLearned(name string) (*entry, error) {
	dir := filepath.Join(h.dir, name)
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return nil, err
	}
	f, body, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if f.Name != name {
		return nil, fmt.Errorf("skill %s: name %q is not its directory's", name, f.Name)
	}
	return &entry{
		Skill:  Skill{Name: name, Description: f.Description, Dir: dir, Learned: true},
		front:  f,
		body:   body,
		hidden: !h.visible(f),
	}, nil
}

// lastSeen is when a learned skill was last viewed or written, or, for
// one the host has no record of, when its SKILL.md last changed.
func (h *host) lastSeen(e *entry) time.Time {
	if t, ok := h.seen[e.Name]; ok {
		return t
	}
	if st, err := os.Stat(filepath.Join(e.Dir, "SKILL.md")); err == nil {
		return st.ModTime()
	}
	return h.now()
}

// sorted is the visible skills, haunt's own first, each group by name.
func (h *host) sorted() []*entry {
	var out []*entry
	for _, e := range h.skills {
		if !e.hidden {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Learned != out[j].Learned {
			return !out[i].Learned
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Index is one line per visible skill: its name, a mark on a learned
// one (and on a stale one), and its description cut to IndexDescription
// characters.
func (h *host) Index() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var b strings.Builder
	for _, e := range h.sorted() {
		b.WriteString("- ")
		b.WriteString(e.Name)
		switch {
		case e.Stale:
			b.WriteString(" (learned, stale)")
		case e.Learned:
			b.WriteString(" (learned)")
		}
		b.WriteString(": ")
		b.WriteString(cut(oneLine(e.Description), IndexDescription))
		b.WriteByte('\n')
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// cut shortens s to n characters, ending in an ellipsis when it cuts.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// View returns a visible skill's body, the front matter left off, with
// the names of any other files in its directory listed after it. Viewing
// a learned skill counts as using it: the curator's clock restarts and
// it is no longer stale.
func (h *host) View(name string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.skills[name]
	if e == nil || e.hidden {
		return "", fmt.Errorf("there is no skill named %q", name)
	}
	body := e.body
	if files := h.supportFiles(e); len(files) > 0 {
		body = strings.TrimRight(body, "\n") + "\n\nFiles in this skill: " + strings.Join(files, ", ") + "\n"
	}
	if e.Learned {
		h.seen[name] = h.now()
		e.Stale = false
		if err := h.saveSeen(); err != nil {
			return body, nil // the view stands; only the curator's record is behind
		}
	}
	return body, nil
}

// supportFiles lists the files in a skill's directory besides SKILL.md,
// relative to it, skipping dotfiles.
func (h *host) supportFiles(e *entry) []string {
	var fsys fs.FS
	var root string
	if e.Learned {
		fsys, root = os.DirFS(e.Dir), "."
	} else {
		fsys, root = h.embedded, e.Dir
	}
	var out []string
	_ = fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		if rel != "SKILL.md" {
			out = append(out, rel)
		}
		return nil
	})
	return out
}

// Write creates or replaces the learned skill name with body, a whole
// SKILL.md. It refuses a name that is not valid, that is one of haunt's
// own skills, or that is the archive's; a body without front matter, a
// name in it that is not name, or a body over MaxBody; and a body that
// requires or names a tool outside the available list, naming each such
// tool, since a skill telling the ghost to use what it has not got sends
// it after nothing. The file is rewritten in place; any support files
// in the directory stay.
func (h *host) Write(name, body string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dir == "" {
		return errors.New("this ghost has nowhere to keep skills")
	}
	if !ValidName(name) || name == ArchiveDir {
		return fmt.Errorf("%q is not a skill name: use lowercase letters, digits and hyphens", name)
	}
	if e := h.skills[name]; e != nil && !e.Learned {
		return fmt.Errorf("%q is one of haunt's own skills and cannot be changed", name)
	}
	if len(body) > MaxBody {
		return fmt.Errorf("skill %q is %d bytes; a skill is at most %d", name, len(body), MaxBody)
	}
	f, text, err := Parse([]byte(body))
	if err != nil {
		return fmt.Errorf("skill %q: %w", name, err)
	}
	if f.Name != name {
		return fmt.Errorf("skill %q: its front matter names it %q", name, f.Name)
	}
	var unknown []string
	for _, t := range append(slices.Clone(f.RequiresTools()), ToolRefs(text)...) {
		if !h.available(t) && !slices.Contains(unknown, t) {
			unknown = append(unknown, t)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("skill %q names %s, which this ghost does not have; it may name only: %s",
			name, strings.Join(unknown, ", "), strings.Join(h.tools, ", "))
	}
	data, err := render(f, text)
	if err != nil {
		return err
	}
	dir := filepath.Join(h.dir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("skill %q could not be saved: %w", name, bare(err))
	}
	tmp, err := os.CreateTemp(dir, ".SKILL.md.*")
	if err != nil {
		return fmt.Errorf("skill %q could not be saved: %w", name, bare(err))
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), filepath.Join(dir, "SKILL.md"))
	}
	if werr != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("skill %q could not be saved: %w", name, bare(werr))
	}
	h.skills[name] = &entry{
		Skill: Skill{Name: name, Description: f.Description, Dir: dir, Learned: true},
		front: f,
		body:  text,
	}
	h.seen[name] = h.now()
	_ = h.saveSeen() // a missing record only makes the skill look as old as its file
	return nil
}

// render writes a SKILL.md from its parsed parts, so what is kept holds
// only the front matter haunt reads.
func render(f Front, body string) ([]byte, error) {
	head, err := yaml.Marshal(f)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("---\n")
	b.Write(head)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteByte('\n')
	return b.Bytes(), nil
}

// bare drops the path from a filesystem error: what Write returns goes
// back to the model, which has no business with the ghost's directory.
func bare(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Err
	}
	return err
}

// Curate marks a learned skill stale once it has gone StaleAfter without
// a view, and moves it into the archive once it has gone ArchiveAfter.
// An archived skill replaces any older one of the same name there. It
// stops at the first skill it cannot move.
func (h *host) Curate(now time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dir == "" {
		return nil
	}
	names := make([]string, 0, len(h.skills))
	for name, e := range h.skills {
		if e.Learned {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var err error
	for _, name := range names {
		e := h.skills[name]
		last := h.lastSeen(e)
		h.seen[name] = last
		age := now.Sub(last)
		if age <= ArchiveAfter {
			e.Stale = age > StaleAfter
			continue
		}
		if err = h.archive(name); err != nil {
			break
		}
		delete(h.skills, name)
		delete(h.seen, name)
	}
	if serr := h.saveSeen(); err == nil {
		err = serr
	}
	return err
}

func (h *host) archive(name string) error {
	archive := filepath.Join(h.dir, ArchiveDir)
	if err := os.MkdirAll(archive, 0o700); err != nil {
		return err
	}
	dest := filepath.Join(archive, name)
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return os.Rename(filepath.Join(h.dir, name), dest)
}

func (h *host) saveSeen() error {
	if err := os.MkdirAll(h.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(h.seen, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(h.dir, seenFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(h.dir, seenFile))
}

// List is the visible skills, haunt's own first, each group by name.
func (h *host) List() []Skill {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Skill
	for _, e := range h.sorted() {
		out = append(out, e.Skill)
	}
	return out
}
