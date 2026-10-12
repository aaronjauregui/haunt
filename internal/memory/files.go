package memory

// The files are the memory; everything here reads and writes them
// without the database, so the CLI can list, show and remove facts with
// the ghost running or not, and the store's own writes go through the
// same code.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// CoreLimit bounds self.md and you.md: both are in every prompt.
	CoreLimit = 1500
	// IndexLimit bounds the topic index: it is in every prompt too.
	IndexLimit = 1500
	// LineLimit bounds one fact. A fact is a line, not a page; a page
	// belongs in a file the ghost reads, not in what it remembers.
	LineLimit = 500
	// topicLimit bounds a topic name, which is a file name and an index
	// entry.
	topicLimit = 48

	topicsDir = "topics"
	dateForm  = "2006-01-02"
)

// Sources are the provenance labels a fact may carry.
var Sources = []string{"person", "web", "file", "self"}

// Fact is one line of a topic file.
type Fact struct {
	Line string
	Prov Provenance
}

// Topic is one topic file read whole.
type Topic struct {
	Name    string
	Updated time.Time
	Facts   []Fact
	Past    []PastFact // the lines under ## Past, oldest retired first
}

// PastFact is a line under a topic's ## Past: kept and readable, no
// longer a fact. Retired is the date it was moved there, zero for a line
// the person moved by hand without one.
type PastFact struct {
	Fact
	Retired time.Time
}

// TopicInfo is what `haunt memory ls` and the index show of a topic.
type TopicInfo struct {
	Name  string
	Lines int    // facts, not counting the past
	Past  int    // lines under ## Past
	First string // the first fact, whole
}

// PastHeading starts the part of a topic file that holds retired lines.
// Everything below it is past: not a fact, not indexed, not in the
// prompt, but kept for the person (and `haunt memory show`) to read, and
// moved back up by hand if it was retired wrongly.
const PastHeading = "## Past"

// topicFile is a topic file as lines, so a rewrite keeps what the person
// wrote around the facts: front matter keys this package does not know,
// headings, blank lines.
type topicFile struct {
	front []string // between the --- fences, without them
	body  []string // above PastHeading
	// past is the lines below PastHeading, nil when the file has none,
	// so a file without one is written back without one.
	past []string
}

var (
	topicName  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	provenance = regexp.MustCompile(`\s*<!--\s*([a-z]+)(?:\s+t=(\d+))?(?:\s+(\d{4}-\d{2}-\d{2}))?\s*-->\s*$`)
	retired    = regexp.MustCompile(`\s*<!--\s*retired\s+(\d{4}-\d{2}-\d{2})\s*-->\s*$`)
)

// NormalizeTopic turns a topic as the model or the person wrote it into
// the file name it is kept under: lower case, spaces as hyphens. It
// refuses anything else that would not be a plain file name.
func NormalizeTopic(topic string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(topic))
	t = strings.Join(strings.Fields(t), "-")
	t = strings.TrimSuffix(t, ".md")
	if t == "" {
		return "", errors.New("memory: topic is empty")
	}
	if len(t) > topicLimit {
		return "", fmt.Errorf("memory: topic %q is longer than %d characters", t, topicLimit)
	}
	if !topicName.MatchString(t) {
		return "", fmt.Errorf("memory: topic %q may only hold letters, digits, '-' and '_'", t)
	}
	return t, nil
}

// cleanLine makes a fact one line with no comment markers in it, so it
// cannot break the file format or forge a provenance comment.
func cleanLine(line string) (string, error) {
	l := strings.Join(strings.Fields(line), " ")
	if strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "* ") {
		l = strings.TrimSpace(l[2:])
	}
	if strings.Contains(l, "<!--") || strings.Contains(l, "-->") {
		return "", errors.New("memory: a fact may not contain an HTML comment")
	}
	if l == "" {
		return "", errors.New("memory: the fact is empty")
	}
	if len(l) > LineLimit {
		return "", fmt.Errorf("memory: a fact is at most %d bytes", LineLimit)
	}
	return l, nil
}

func validSource(s string) bool {
	for _, v := range Sources {
		if s == v {
			return true
		}
	}
	return false
}

// parseFact reads one body line; ok is false for a line that is not a
// fact (blank, a heading).
func parseFact(raw string) (Fact, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || strings.HasPrefix(s, "#") {
		return Fact{}, false
	}
	if strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "* ") {
		s = strings.TrimSpace(s[2:])
	}
	f := Fact{Prov: Provenance{Source: "person"}}
	if m := provenance.FindStringSubmatchIndex(s); m != nil {
		src := s[m[2]:m[3]]
		if validSource(src) {
			f.Prov.Source = src
		}
		if m[4] >= 0 {
			f.Prov.Turn, _ = strconv.ParseInt(s[m[4]:m[5]], 10, 64)
		}
		if m[6] >= 0 {
			f.Prov.At, _ = time.ParseInLocation(dateForm, s[m[6]:m[7]], time.Local)
		}
		s = strings.TrimSpace(s[:m[0]])
	}
	if s == "" {
		return Fact{}, false
	}
	f.Line = s
	return f, true
}

// parsePast reads one line under ## Past: the retired comment, if any,
// then the fact with its provenance as it was.
func parsePast(raw string) (PastFact, bool) {
	var p PastFact
	if m := retired.FindStringSubmatchIndex(raw); m != nil {
		p.Retired, _ = time.ParseInLocation(dateForm, raw[m[2]:m[3]], time.Local)
		raw = raw[:m[0]]
	}
	f, ok := parseFact(raw)
	p.Fact = f
	return p, ok
}

// SameLine reports whether a fact is the line asked for: case and spacing
// aside, with a bullet or a provenance comment on want ignored, so the
// model may quote the line as it saw it.
func SameLine(f Fact, want string) bool {
	w := strings.Join(strings.Fields(want), " ")
	if p, ok := parseFact(w); ok {
		w = p.Line
	}
	return strings.EqualFold(f.Line, w)
}

// formatFact is the body line for a fact.
func formatFact(line string, p Provenance) string {
	return fmt.Sprintf("- %s <!-- %s t=%d %s -->", line, p.Source, p.Turn, p.At.Format(dateForm))
}

func parseTopicFile(data string) topicFile {
	lines := strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var tf topicFile
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				tf.front = lines[1:i]
				tf.body = lines[i+1:]
				return tf.splitPast()
			}
		}
	}
	tf.body = lines
	return tf.splitPast()
}

// splitPast moves the lines below PastHeading out of the body. The blank
// lines before the heading are the writer's to put back (String), so a
// line appended to the body lands above the gap.
func (tf topicFile) splitPast() topicFile {
	for i, l := range tf.body {
		if strings.TrimSpace(l) == PastHeading {
			tf.past = append([]string{}, tf.body[i+1:]...)
			body := tf.body[:i]
			for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
				body = body[:len(body)-1]
			}
			tf.body = body
			break
		}
	}
	return tf
}

func (tf topicFile) frontValue(key string) string {
	for _, l := range tf.front {
		k, v, ok := strings.Cut(l, ":")
		if ok && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (tf *topicFile) setFront(key, value string) {
	for i, l := range tf.front {
		k, _, ok := strings.Cut(l, ":")
		if ok && strings.TrimSpace(k) == key {
			tf.front[i] = key + ": " + value
			return
		}
	}
	tf.front = append(tf.front, key+": "+value)
}

func (tf topicFile) String() string {
	var b strings.Builder
	b.WriteString("---\n")
	for _, l := range tf.front {
		b.WriteString(l + "\n")
	}
	b.WriteString("---\n")
	for _, l := range tf.body {
		b.WriteString(l + "\n")
	}
	if tf.past != nil {
		if len(tf.body) > 0 {
			b.WriteString("\n")
		}
		b.WriteString(PastHeading + "\n")
		for _, l := range tf.past {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

// pastFacts is the lines under ## Past that read as facts.
func (tf topicFile) pastFacts() []PastFact {
	var out []PastFact
	for _, l := range tf.past {
		if p, ok := parsePast(l); ok {
			out = append(out, p)
		}
	}
	return out
}

func (tf topicFile) facts() []Fact {
	var out []Fact
	for _, l := range tf.body {
		if f, ok := parseFact(l); ok {
			out = append(out, f)
		}
	}
	return out
}

func (tf topicFile) topic(name string) Topic {
	t := Topic{Name: name, Facts: tf.facts(), Past: tf.pastFacts()}
	t.Updated, _ = time.Parse(time.RFC3339, tf.frontValue("updated"))
	return t
}

func topicPath(dir, name string) string {
	return filepath.Join(dir, topicsDir, name+".md")
}

func readTopicFile(dir, name string) (topicFile, error) {
	b, err := os.ReadFile(topicPath(dir, name))
	if err != nil {
		return topicFile{}, err
	}
	return parseTopicFile(string(b)), nil
}

// writeFile replaces path atomically: written beside it, then renamed
// into place, so the watcher and a reader never see half a file.
func writeFile(path, data string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func writeTopicFile(dir, name string, tf topicFile, now time.Time) error {
	if tf.frontValue("topic") == "" {
		tf.setFront("topic", name)
	}
	tf.setFront("updated", now.Format(time.RFC3339))
	return writeFile(topicPath(dir, name), tf.String())
}

// topicNames lists the topic files: plain names ending in .md, so an
// editor's swap, backup and temp files are not topics.
func topicNames(dir string) ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(dir, topicsDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".md") {
			continue
		}
		stem := strings.TrimSuffix(n, ".md")
		if topicName.MatchString(stem) && len(stem) <= topicLimit {
			names = append(names, stem)
		}
	}
	sort.Strings(names)
	return names, nil
}

// Topics lists the topics under a memory directory, with their line
// counts, read from the files.
func Topics(dir string) ([]TopicInfo, error) {
	names, err := topicNames(dir)
	if err != nil {
		return nil, scrub(dir, err)
	}
	var out []TopicInfo
	for _, n := range names {
		tf, err := readTopicFile(dir, n)
		if err != nil {
			return nil, scrub(dir, err)
		}
		facts := tf.facts()
		ti := TopicInfo{Name: n, Lines: len(facts), Past: len(tf.pastFacts())}
		if len(facts) > 0 {
			ti.First = facts[0].Line
		}
		out = append(out, ti)
	}
	return out, nil
}

// ReadTopic reads one topic from the files.
func ReadTopic(dir, topic string) (Topic, error) {
	name, err := NormalizeTopic(topic)
	if err != nil {
		return Topic{}, err
	}
	tf, err := readTopicFile(dir, name)
	if errors.Is(err, os.ErrNotExist) {
		return Topic{}, fmt.Errorf("memory: no topic %q", name)
	}
	if err != nil {
		return Topic{}, scrub(dir, err)
	}
	return tf.topic(name), nil
}

// RemoveTopic deletes a topic file.
func RemoveTopic(dir, topic string) error {
	name, err := NormalizeTopic(topic)
	if err != nil {
		return err
	}
	err = os.Remove(topicPath(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("memory: no topic %q", name)
	}
	return scrub(dir, err)
}

// RemoveLine deletes the nth fact (1-based, as `haunt memory show`
// numbers them) from a topic, and returns it. Past lines are not
// numbered. The topic file goes when its last line, fact or past, does.
func RemoveLine(dir, topic string, n int) (Fact, error) {
	name, err := NormalizeTopic(topic)
	if err != nil {
		return Fact{}, err
	}
	tf, err := readTopicFile(dir, name)
	if errors.Is(err, os.ErrNotExist) {
		return Fact{}, fmt.Errorf("memory: no topic %q", name)
	}
	if err != nil {
		return Fact{}, scrub(dir, err)
	}
	seen := 0
	for i, l := range tf.body {
		f, ok := parseFact(l)
		if !ok {
			continue
		}
		seen++
		if seen == n {
			tf.body = append(tf.body[:i:i], tf.body[i+1:]...)
			return f, scrub(dir, saveOrRemove(dir, name, tf))
		}
	}
	return Fact{}, fmt.Errorf("memory: topic %q has %d lines, no line %d", name, seen, n)
}

// saveOrRemove writes a topic back, or removes it when nothing is left
// in it, facts or past.
func saveOrRemove(dir, name string, tf topicFile) error {
	if len(tf.facts()) == 0 && len(tf.pastFacts()) == 0 {
		return os.Remove(topicPath(dir, name))
	}
	return writeTopicFile(dir, name, tf, time.Now())
}

// coreFile is the file behind SetCore's which.
func coreFile(which string) (string, error) {
	switch which {
	case "self":
		return "self.md", nil
	case "you":
		return "you.md", nil
	}
	return "", fmt.Errorf("memory: core file is \"self\" or \"you\", not %q", which)
}

// cut shortens s to at most n bytes without splitting a character.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// scrubbed is an error whose message names the memory directory as
// "memory" rather than by its absolute path, keeping what it wraps.
type scrubbed struct {
	msg string
	err error
}

func (e *scrubbed) Error() string { return e.msg }
func (e *scrubbed) Unwrap() error { return e.err }

// scrub rewrites err so it does not carry dir's absolute path: errors
// reach the model, and where the person keeps their files is not its
// business.
func scrub(dir string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, d := range pathForms(dir) {
		msg = strings.ReplaceAll(msg, d, "memory")
	}
	if msg == err.Error() {
		return err
	}
	return &scrubbed{msg: msg, err: err}
}

// pathForms is dir as it may appear in an error: as given, absolute, and
// with symlinks resolved. Longest first, so a longer form is replaced
// before a prefix of it.
func pathForms(dir string) []string {
	forms := []string{filepath.Clean(dir)}
	if abs, err := filepath.Abs(dir); err == nil {
		forms = append(forms, abs)
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		forms = append(forms, real)
	}
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	var out []string
	for _, f := range forms {
		if f != "." && f != "" {
			out = append(out, f)
		}
	}
	return out
}
