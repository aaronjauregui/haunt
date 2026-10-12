package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// fileStore is the Store: the files under dir, indexed into db.
type fileStore struct {
	dir string
	db  *sql.DB

	// mu serialises writes to the files and the snapshot the watcher
	// compares against, so the store's own writes are not mistaken for
	// the person's.
	mu   sync.Mutex
	snap snapshot

	closeOnce sync.Once
	closed    chan struct{}
}

func open(dir string, db *sql.DB) (*fileStore, error) {
	if db == nil {
		return nil, errors.New("memory: no database")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(abs, topicsDir), 0o700); err != nil {
		return nil, scrub(abs, err)
	}
	s := &fileStore{dir: abs, db: db, closed: make(chan struct{})}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := Reindex(abs, db); err != nil {
		return nil, err
	}
	s.snap = scan(abs)
	return s, nil
}

func (s *fileStore) Core() (self, you string, err error) {
	read := func(name string) (string, error) {
		b, err := os.ReadFile(filepath.Join(s.dir, name))
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", scrub(s.dir, err)
		}
		// The person may write past the limit by hand; the prompt still
		// gets no more than CoreLimit.
		return cut(string(b), CoreLimit), nil
	}
	if self, err = read("self.md"); err != nil {
		return "", "", err
	}
	if you, err = read("you.md"); err != nil {
		return "", "", err
	}
	return self, you, nil
}

func (s *fileStore) SetCore(which, text string) error {
	name, err := coreFile(which)
	if err != nil {
		return err
	}
	if len(text) > CoreLimit {
		return fmt.Errorf("memory: %s is %d bytes; it must stay under %d", name, len(text), CoreLimit)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeFile(filepath.Join(s.dir, name), text); err != nil {
		return scrub(s.dir, err)
	}
	s.snap.restamp(s.dir, name)
	return nil
}

func (s *fileStore) Index() (string, error) {
	all, err := Topics(s.dir)
	if err != nil {
		return "", err
	}
	// A topic left with only past lines has nothing to recall.
	topics := all[:0]
	for _, t := range all {
		if t.Lines > 0 {
			topics = append(topics, t)
		}
	}
	return renderIndex(topics), nil
}

// renderIndex is one line per topic: its name, its line count and as
// much of its first line as fits, up to 60 characters. The whole stays
// under IndexLimit: the previews shrink as topics are added, and past the
// point where even the names do not fit, the last topics are summed up in
// a closing line.
func renderIndex(topics []TopicInfo) string {
	const preview = 60
	heads := make([]string, len(topics))
	used := 0
	for i, t := range topics {
		heads[i] = t.Name + " (" + strconv.Itoa(t.Lines) + ")"
		used += len(heads[i]) + 1
	}
	room := IndexLimit - 1 - used
	if room < 0 {
		// Names alone overflow: keep as many as fit with the closing line.
		var b strings.Builder
		for i, h := range heads {
			more := fmt.Sprintf("(and %d more topics)\n", len(heads)-i)
			if b.Len()+len(h)+1+len(more) > IndexLimit-1 {
				b.WriteString(more)
				return b.String()
			}
			b.WriteString(h + "\n")
		}
		return b.String()
	}
	per := preview
	if len(topics) > 0 && room/len(topics)-2 < per {
		per = room/len(topics) - 2
	}
	var b strings.Builder
	for i, t := range topics {
		b.WriteString(heads[i])
		if p := firstChars(t.First, preview); per > 0 && p != "" {
			b.WriteString(": " + cut(p, per))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// firstChars is s's first n characters.
func firstChars(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

func (s *fileStore) Remember(topic, line string, p Provenance) error {
	name, err := NormalizeTopic(topic)
	if err != nil {
		return err
	}
	l, err := cleanLine(line)
	if err != nil {
		return err
	}
	if !validSource(p.Source) {
		return fmt.Errorf("memory: source %q is not one of %s", p.Source, strings.Join(Sources, ", "))
	}
	if p.At.IsZero() {
		p.At = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tf, err := readTopicFile(s.dir, name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return scrub(s.dir, err)
	}
	for _, f := range tf.facts() {
		if strings.EqualFold(f.Line, l) {
			return nil // already known; the first provenance stands
		}
	}
	tf.body = append(tf.body, formatFact(l, p))
	if err := writeTopicFile(s.dir, name, tf, time.Now()); err != nil {
		return scrub(s.dir, err)
	}
	return s.reindexTopicLocked(name)
}

func (s *fileStore) Forget(topic, line string) error {
	name, err := NormalizeTopic(topic)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(line) == "" {
		if err := RemoveTopic(s.dir, name); err != nil {
			return err
		}
		return s.reindexTopicLocked(name)
	}
	tf, err := readTopicFile(s.dir, name)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("memory: no topic %q", name)
	}
	if err != nil {
		return scrub(s.dir, err)
	}
	kept := tf.body[:0:0]
	gone := 0
	for _, raw := range tf.body {
		if f, ok := parseFact(raw); ok && SameLine(f, line) {
			gone++
			continue
		}
		kept = append(kept, raw)
	}
	if gone == 0 {
		return fmt.Errorf("memory: topic %q has no line %q", name, strings.Join(strings.Fields(line), " "))
	}
	tf.body = kept
	if err := saveOrRemove(s.dir, name, tf); err != nil {
		return scrub(s.dir, err)
	}
	return s.reindexTopicLocked(name)
}

// ErrTheirs is Retire's refusal of a line the person stated, asked by
// anyone else.
var ErrTheirs = errors.New("memory: a line the person stated is retired only on their word")

func (s *fileStore) Retire(topic, line string, by Provenance) error {
	name, err := NormalizeTopic(topic)
	if err != nil {
		return err
	}
	if by.At.IsZero() {
		by.At = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tf, err := readTopicFile(s.dir, name)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("memory: no topic %q", name)
	}
	if err != nil {
		return scrub(s.dir, err)
	}
	kept := tf.body[:0:0]
	var moved []string
	for _, raw := range tf.body {
		f, ok := parseFact(raw)
		if !ok || !SameLine(f, line) {
			kept = append(kept, raw)
			continue
		}
		if f.Prov.Source == "person" && by.Source != "person" {
			return ErrTheirs
		}
		l := strings.TrimSpace(raw)
		if !strings.HasPrefix(l, "- ") && !strings.HasPrefix(l, "* ") {
			l = "- " + l
		}
		moved = append(moved, l+" <!-- retired "+by.At.Format(dateForm)+" -->")
	}
	if len(moved) == 0 {
		return fmt.Errorf("memory: topic %q has no line %q", name, strings.Join(strings.Fields(line), " "))
	}
	tf.body = kept
	tf.past = append(append([]string{}, tf.past...), moved...)
	if err := writeTopicFile(s.dir, name, tf, time.Now()); err != nil {
		return scrub(s.dir, err)
	}
	return s.reindexTopicLocked(name)
}

func (s *fileStore) Facts(topic string) ([]Fact, error) {
	name, err := NormalizeTopic(topic)
	if err != nil {
		return nil, err
	}
	tf, err := readTopicFile(s.dir, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, scrub(s.dir, err)
	}
	return tf.facts(), nil
}

// reindexTopicLocked replaces one topic's rows after the store wrote its
// file, and moves that file's stamp on so the watcher does not see the
// write as the person's. Only that file's: an edit the person made to
// another file meanwhile is still the watcher's to find. Callers hold mu.
func (s *fileStore) reindexTopicLocked(name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM memory_fts WHERE topic = ?`, name); err != nil {
		return err
	}
	tf, err := readTopicFile(s.dir, name)
	if err == nil {
		if err := insertFacts(tx, name, tf.facts()); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return scrub(s.dir, err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.snap.restamp(s.dir, filepath.Join(topicsDir, name+".md"))
	return nil
}

func (s *fileStore) Recall(ctx context.Context, q string, n int) ([]Hit, error) {
	match := ftsQuery(q)
	if match == "" {
		return nil, nil
	}
	if n <= 0 {
		n = 5
	}
	if n > 50 {
		n = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT topic, line, source, turn, at, bm25(memory_fts, ?, ?) AS bm
		FROM memory_fts WHERE memory_fts MATCH ?
		ORDER BY bm LIMIT ?`, topicWeight, lineWeight, match, n)
	if err != nil {
		return nil, fmt.Errorf("memory: recall: %w", err)
	}
	defer rows.Close()
	var hits []Hit
	for rows.Next() {
		var h Hit
		var at string
		var rank float64
		if err := rows.Scan(&h.Topic, &h.Line, &h.Prov.Source, &h.Prov.Turn, &at, &rank); err != nil {
			return nil, err
		}
		h.Prov.At, _ = time.ParseInLocation(dateForm, at, time.Local)
		// bm25 is lower for a better match; a score reads the other way.
		h.Score = -rank
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// The BM25 weights of the two searched columns. The line keeps weight 1,
// so a match on the line's own words scores as it would with the line
// alone and keyed context's cutoff (in package ghost) reads the same. A
// topic word counts for less: the topic is one or two words on every line
// it holds, and a match there says the line is about the subject, not
// what it says about it. At 0.4 a topic-only hit ranks below a hit on the
// same word in a line up to about four times the average line's length,
// and against a dozen lines, a two-line topic's words score 0.55 to 0.76
// where the same word in a line scores about 0.8 and a content word 2 to
// 2.5.
const (
	topicWeight = 0.4
	lineWeight  = 1.0
)

// ftsQuery turns free text into an FTS5 query: each word quoted, any of
// them matching. Free text is not FTS syntax, and a stray quote or
// operator from the model must not become a query error.
func ftsQuery(q string) string {
	words := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for i, w := range words {
		words[i] = `"` + w + `"`
	}
	return strings.Join(words, " OR ")
}

func (s *fileStore) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

// Reindex drops and rebuilds the FTS table in db from the topic files
// under dir, and says how many topics and lines it indexed. The table is
// derived and can always be rebuilt; dropping it rather than emptying it
// means a change to its columns needs no migration: Open calls Reindex,
// so a store indexed under an older layout is rebuilt in the current one
// the first time it is opened.
func Reindex(dir string, db *sql.DB) (topics, lines int, err error) {
	names, err := topicNames(dir)
	if err != nil {
		return 0, 0, scrub(dir, err)
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DROP TABLE IF EXISTS memory_fts`); err != nil {
		return 0, 0, err
	}
	// The topic and the line are searched, weighted by topicWeight and
	// lineWeight; the rest rides along for the hit.
	if _, err := tx.Exec(`CREATE VIRTUAL TABLE memory_fts USING fts5(
		topic, line, source UNINDEXED, turn UNINDEXED, at UNINDEXED,
		tokenize = 'porter unicode61')`); err != nil {
		return 0, 0, fmt.Errorf("memory: creating the index: %w", err)
	}
	for _, n := range names {
		tf, err := readTopicFile(dir, n)
		if errors.Is(err, os.ErrNotExist) {
			continue // removed between the listing and the read
		}
		if err != nil {
			return 0, 0, scrub(dir, err)
		}
		facts := tf.facts()
		if err := insertFacts(tx, n, facts); err != nil {
			return 0, 0, err
		}
		topics++
		lines += len(facts)
	}
	return topics, lines, tx.Commit()
}

func insertFacts(tx *sql.Tx, topic string, facts []Fact) error {
	for _, f := range facts {
		at := ""
		if !f.Prov.At.IsZero() {
			at = f.Prov.At.Format(dateForm)
		}
		if _, err := tx.Exec(`INSERT INTO memory_fts (topic, line, source, turn, at) VALUES (?, ?, ?, ?, ?)`,
			topic, f.Line, f.Prov.Source, f.Prov.Turn, at); err != nil {
			return err
		}
	}
	return nil
}
