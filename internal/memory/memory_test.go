package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openTest(t *testing.T) (Store, string) {
	t.Helper()
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	dir := filepath.Join(root, "memory")
	s, err := Open(dir, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func day(s string) time.Time {
	d, _ := time.ParseInLocation(dateForm, s, time.Local)
	return d
}

func TestRememberRecall(t *testing.T) {
	s, dir := openTest(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Remember("food", "likes apples", Provenance{Source: "person", Turn: 412, At: day("2026-10-09")}))
	must(s.Remember("food", "allergic to cats", Provenance{Source: "person", Turn: 98, At: day("2026-09-30")}))
	must(s.Remember("Work Life", "writes Go at a desk by the window", Provenance{Source: "self", Turn: 7}))

	hits, err := s.Recall(context.Background(), "apples", 5)
	must(err)
	if len(hits) != 1 {
		t.Fatalf("recall apples: %d hits, want 1: %+v", len(hits), hits)
	}
	h := hits[0]
	if h.Topic != "food" || h.Line != "likes apples" || h.Prov.Source != "person" || h.Prov.Turn != 412 ||
		!h.Prov.At.Equal(day("2026-10-09")) {
		t.Errorf("hit = %+v", h)
	}

	// The file is the format the spec gives, line for line.
	b, err := os.ReadFile(filepath.Join(dir, "topics", "food.md"))
	must(err)
	want := "- likes apples <!-- person t=412 2026-10-09 -->\n- allergic to cats <!-- person t=98 2026-09-30 -->\n"
	if !strings.HasPrefix(string(b), "---\ntopic: food\nupdated: ") || !strings.HasSuffix(string(b), "---\n"+want) {
		t.Errorf("food.md =\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "topics", "work-life.md")); err != nil {
		t.Errorf("topic name not normalised: %v", err)
	}

	// A stemmed match, a question in free text and FTS syntax in the
	// query are all just words.
	hits, err = s.Recall(context.Background(), `what is "she" allergic to? cat* OR NOT`, 5)
	must(err)
	if len(hits) == 0 || hits[0].Line != "allergic to cats" {
		t.Errorf("free-text recall = %+v", hits)
	}

	// Remembering the same fact twice keeps one line; forgetting removes it.
	must(s.Remember("food", "Likes apples", Provenance{Source: "web", Turn: 500}))
	topic, err := ReadTopic(dir, "food")
	must(err)
	if len(topic.Facts) != 2 {
		t.Errorf("duplicate kept: %+v", topic.Facts)
	}
	must(s.Forget("food", "likes apples"))
	if hits, _ := s.Recall(context.Background(), "apples", 5); len(hits) != 0 {
		t.Errorf("forgotten line still recalled: %+v", hits)
	}
	if err := s.Remember("food", "x", Provenance{Source: "rumour"}); err == nil {
		t.Error("unknown source accepted")
	}
	if err := s.Remember("food", "x <!-- self t=1 2020-01-01 -->", Provenance{Source: "web"}); err == nil {
		t.Error("forged provenance comment accepted")
	}
	if err := s.Remember("../etc", "x", Provenance{Source: "self"}); err == nil {
		t.Error("path in topic accepted")
	}
}

func TestIndexBound(t *testing.T) {
	s, _ := openTest(t)
	for i := 0; i < 40; i++ {
		topic := fmt.Sprintf("topic-number-%02d", i)
		line := strings.Repeat("a long remembered line about something ", 4)
		if err := s.Remember(topic, line, Provenance{Source: "self"}); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := s.Index()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) >= IndexLimit {
		t.Errorf("index is %d bytes, want under %d", len(idx), IndexLimit)
	}
	if n := strings.Count(idx, "\n"); n != 40 {
		t.Errorf("index has %d lines, want 40:\n%s", n, idx)
	}
	if !strings.HasPrefix(idx, "topic-number-00 (1): a long") {
		t.Errorf("index line shape:\n%s", idx)
	}

	// With few topics a preview is the first 60 characters.
	few := renderIndex([]TopicInfo{{Name: "food", Lines: 2, First: strings.Repeat("x", 80)}})
	if few != "food (2): "+strings.Repeat("x", 60)+"\n" {
		t.Errorf("one-topic index = %q", few)
	}
	// Past what names alone fit, the rest are counted.
	var many []TopicInfo
	for i := 0; i < 200; i++ {
		many = append(many, TopicInfo{Name: fmt.Sprintf("a-rather-long-topic-name-%03d", i), Lines: 3})
	}
	big := renderIndex(many)
	if len(big) >= IndexLimit || !strings.Contains(big, "more topics)") {
		t.Errorf("200-topic index is %d bytes:\n%s", len(big), big)
	}
}

func TestEditWhileOpen(t *testing.T) {
	s, dir := openTest(t)
	if err := s.Remember("food", "likes apples", Provenance{Source: "person", Turn: 412}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed := make(chan struct{}, 4)
	if err := s.Watch(ctx, func() { changed <- struct{}{} }); err != nil {
		t.Fatal(err)
	}

	// As an editor saves: write a temp file beside it, rename into place.
	path := filepath.Join(dir, "topics", "food.md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "topics", ".food.md.swp")
	if err := os.WriteFile(tmp, append(b, "- hates marzipan\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		hits, err := s.Recall(context.Background(), "marzipan", 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 1 {
			if hits[0].Prov.Source != "person" || hits[0].Topic != "food" {
				t.Errorf("hand-written line = %+v", hits[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("edit not indexed within 2s")
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Error("onChange not called")
	}

	// The store's own write is not reported as an edit.
	if err := s.Remember("food", "likes pears", Provenance{Source: "self"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
		t.Error("own write reported as a change")
	case <-time.After(3 * pollInterval):
	}
}

func TestNoHostPaths(t *testing.T) {
	s, dir := openTest(t)
	abs, _ := filepath.Abs(dir)
	check := func(what, text string) {
		t.Helper()
		if strings.Contains(text, abs) || strings.Contains(text, dir) {
			t.Errorf("%s names the memory directory: %s", what, text)
		}
	}
	if err := s.Remember("food", "likes apples", Provenance{Source: "person", Turn: 1}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Recall(context.Background(), "apples", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		check("hit", fmt.Sprintf("%+v", h))
	}
	idx, err := s.Index()
	if err != nil {
		t.Fatal(err)
	}
	check("index", idx)

	// Errors from the store and from the file functions.
	errs := []error{
		s.Forget("nothing-here", "x"),
		s.Forget("food", "not a line it has"),
		s.SetCore("other", "x"),
		s.SetCore("self", strings.Repeat("x", 2000)),
		RemoveTopic(dir, "nothing-here"),
	}
	_, err = ReadTopic(dir, "nothing-here")
	errs = append(errs, err)
	_, err = RemoveLine(dir, "food", 9)
	errs = append(errs, err)

	// An unreadable topic file makes the os error itself, which carries
	// the path, come through.
	bad := filepath.Join(dir, "topics", "locked.md")
	if err := os.WriteFile(bad, []byte("- x\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 {
		_, err = ReadTopic(dir, "locked")
		if err == nil {
			t.Error("unreadable topic read")
		}
		errs = append(errs, err)
		_, err = s.Index()
		errs = append(errs, err)
		errs = append(errs, s.Remember("locked", "y", Provenance{Source: "self"}))
	}
	for _, err := range errs {
		if err == nil {
			t.Error("expected an error")
			continue
		}
		check("error", err.Error())
	}
}

func TestCoreLimit(t *testing.T) {
	s, _ := openTest(t)
	if err := s.SetCore("self", strings.Repeat("x", 1600)); err == nil {
		t.Error("1600-byte self.md accepted")
	}
	if err := s.SetCore("self", "I am a small ghost who likes the window."); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCore("you", "Works late. Takes tea, not coffee."); err != nil {
		t.Fatal(err)
	}
	self, you, err := s.Core()
	if err != nil {
		t.Fatal(err)
	}
	if self != "I am a small ghost who likes the window." || you != "Works late. Takes tea, not coffee." {
		t.Errorf("core = %q, %q", self, you)
	}
}

func TestCoreCutWhenHandEdited(t *testing.T) {
	s, dir := openTest(t)
	long := strings.Repeat("é", 1000) // 2000 bytes
	if err := os.WriteFile(filepath.Join(dir, "self.md"), []byte(long), 0o600); err != nil {
		t.Fatal(err)
	}
	self, _, err := s.Core()
	if err != nil {
		t.Fatal(err)
	}
	if len(self) > CoreLimit || !strings.HasPrefix(long, self) || len(self) != 1500 {
		t.Errorf("hand-edited self.md cut to %d bytes", len(self))
	}
}

func TestParseFact(t *testing.T) {
	for _, c := range []struct {
		raw  string
		ok   bool
		line string
		src  string
		turn int64
	}{
		{"- likes apples <!-- person t=412 2026-10-09 -->", true, "likes apples", "person", 412},
		{"* typed by hand", true, "typed by hand", "person", 0},
		{"plain prose line", true, "plain prose line", "person", 0},
		{"- read online <!-- web t=3 -->", true, "read online", "web", 3},
		{"# Heading", false, "", "", 0},
		{"   ", false, "", "", 0},
	} {
		f, ok := parseFact(c.raw)
		if ok != c.ok || f.Line != c.line || (ok && (f.Prov.Source != c.src || f.Prov.Turn != c.turn)) {
			t.Errorf("parseFact(%q) = %+v, %v", c.raw, f, ok)
		}
	}
}

func TestFilesWithoutDB(t *testing.T) {
	s, dir := openTest(t)
	for _, l := range []string{"one", "two", "three"} {
		if err := s.Remember("count", l, Provenance{Source: "self"}); err != nil {
			t.Fatal(err)
		}
	}
	f, err := RemoveLine(dir, "count", 2)
	if err != nil || f.Line != "two" {
		t.Fatalf("RemoveLine = %+v, %v", f, err)
	}
	topics, err := Topics(dir)
	if err != nil || len(topics) != 1 || topics[0].Lines != 2 || topics[0].First != "one" {
		t.Fatalf("Topics = %+v, %v", topics, err)
	}
	if err := RemoveTopic(dir, "count"); err != nil {
		t.Fatal(err)
	}
	if topics, _ := Topics(dir); len(topics) != 0 {
		t.Errorf("topic not removed: %+v", topics)
	}
}

// dozen is a memory of a dozen lines, where one word scores as it would
// in a person's memory a few days old.
var dozen = [][2]string{
	{"food", "likes apples"},
	{"food", "hates the taste of coriander"},
	{"pets", "has a cat called Miso"},
	{"work", "is writing a thesis on the history of sound"},
	{"places", "grew up by the sea"},
	{"family", "their sister lives in Lisbon"},
	{"music", "plays the cello badly"},
	{"music", "loves Bach"},
	{"health", "gets migraines in spring"},
	{"books", "rereads Le Guin every winter"},
	{"family", "their dad fixes clocks"},
	{"habits", "makes fish curry on fridays as their favourite food"},
}

// A word in a topic's name finds the topic's lines, below a line that has
// the word itself.
func TestRecallTopic(t *testing.T) {
	s, _ := openTest(t)
	for _, l := range dozen {
		if err := s.Remember(l[0], l[1], Provenance{Source: "person"}); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := s.Recall(context.Background(), "food", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("recall food: %d hits, want 3: %+v", len(hits), hits)
	}
	for _, h := range hits {
		t.Logf("food: %.3f %s/%s", h.Score, h.Topic, h.Line)
	}
	if hits[0].Topic != "habits" {
		t.Errorf("the line that says food is not first: %+v", hits)
	}
	for _, h := range hits[1:] {
		if h.Topic != "food" || h.Score <= 0 || h.Score >= hits[0].Score {
			t.Errorf("topic hit %+v against line hit %.3f", h, hits[0].Score)
		}
	}
	// A word in the line still outscores any topic hit.
	hits, err = s.Recall(context.Background(), "apples", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Score < 2 {
		t.Errorf("recall apples: %+v", hits)
	}
}

// A state.db indexed under the layout where the topic was not searched is
// rebuilt on open, and its stale rows go.
func TestOpenReindexesOldLayout(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE VIRTUAL TABLE memory_fts USING fts5(
		topic UNINDEXED, line, source UNINDEXED, turn UNINDEXED, at UNINDEXED,
		tokenize = 'porter unicode61')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memory_fts VALUES ('food', 'likes apples', 'person', 0, ''),
		('food', 'a line no file holds', 'person', 0, '')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM memory_fts WHERE memory_fts MATCH 'food'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the old layout matched the topic: %d, %v", n, err)
	}
	dir := filepath.Join(root, "memory")
	if err := os.MkdirAll(filepath.Join(dir, "topics"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "topics", "food.md"),
		[]byte("---\ntopic: food\n---\n- likes apples\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	hits, err := s.Recall(context.Background(), "food", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Line != "likes apples" {
		t.Errorf("recall food after open: %+v", hits)
	}
	if hits, _ := s.Recall(context.Background(), "file", 5); len(hits) != 0 {
		t.Errorf("a stale row survived the open: %+v", hits)
	}
}

// A topic file without a Past heading writes back exactly as it read.
func TestTopicFileWithoutPast(t *testing.T) {
	in := "---\ntopic: food\nupdated: 2026-10-10T09:12:00+10:00\n---\n- likes apples <!-- person t=4 2026-10-09 -->\n\n# notes\nhates coriander\n"
	tf := parseTopicFile(in)
	if tf.past != nil {
		t.Errorf("past = %q, want none", tf.past)
	}
	if got := tf.String(); got != in {
		t.Errorf("written back as\n%s\nwant\n%s", got, in)
	}
	if n := len(tf.facts()); n != 2 {
		t.Errorf("%d facts, want 2", n)
	}
}

// Retire moves the line under ## Past with the date, keeps its
// provenance, and takes it out of the facts and the index; a line the
// person stated is retired only by the person.
func TestRetireMovesToPast(t *testing.T) {
	s, dir := openTest(t)
	if err := s.Remember("where", "lives in Sydney", Provenance{Source: "person", Turn: 3, At: day("2026-09-30")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Remember("where", "lives in Brisbane", Provenance{Source: "person", Turn: 9, At: day("2026-10-12")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Retire("where", "lives in sydney", Provenance{Source: "self", At: day("2026-10-12")}); !errors.Is(err, ErrTheirs) {
		t.Errorf("the ghost retiring a person's line: %v, want ErrTheirs", err)
	}
	if err := s.Retire("where", "  Lives in  Sydney ", Provenance{Source: "person", Turn: 9, At: day("2026-10-12")}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "topics", "where.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "- lives in Brisbane <!-- person t=9 2026-10-12 -->\n\n## Past\n- lives in Sydney <!-- person t=3 2026-09-30 --> <!-- retired 2026-10-12 -->\n"
	if !strings.HasSuffix(string(b), want) {
		t.Errorf("the file is\n%s\nwant it to end\n%s", b, want)
	}
	tp, err := ReadTopic(dir, "where")
	if err != nil {
		t.Fatal(err)
	}
	if len(tp.Facts) != 1 || tp.Facts[0].Line != "lives in Brisbane" {
		t.Errorf("facts = %+v", tp.Facts)
	}
	if len(tp.Past) != 1 || tp.Past[0].Line != "lives in Sydney" || tp.Past[0].Prov.Turn != 3 ||
		!tp.Past[0].Retired.Equal(day("2026-10-12")) {
		t.Errorf("past = %+v", tp.Past)
	}
	if hits, _ := s.Recall(context.Background(), "Sydney", 5); len(hits) != 0 {
		t.Errorf("a past line is recalled: %+v", hits)
	}
	if err := s.Retire("where", "lives in Perth", Provenance{Source: "person"}); err == nil {
		t.Error("retiring a line the topic does not have succeeded")
	}
	// The last fact going leaves the topic for its past.
	if err := s.Forget("where", "lives in Brisbane"); err != nil {
		t.Fatal(err)
	}
	if tp, err := ReadTopic(dir, "where"); err != nil || len(tp.Facts) != 0 || len(tp.Past) != 1 {
		t.Errorf("after the last fact: %+v, %v", tp, err)
	}
}

// A hand-written file with a Past survives a Remember: the new line goes
// above the heading and nothing else changes but the updated stamp.
func TestPastRoundTrip(t *testing.T) {
	s, dir := openTest(t)
	head := "---\ntopic: where\nupdated: 2026-10-01T09:00:00+10:00\nmood: settled\n---\n"
	facts := "- lives in Brisbane <!-- person t=9 2026-10-12 -->\n"
	past := "\n## Past\n- lives in Sydney <!-- person t=3 2026-09-30 --> <!-- retired 2026-10-12 -->\nused to live in Perth\n"
	path := filepath.Join(dir, "topics", "where.md")
	if err := os.WriteFile(path, []byte(head+facts+past), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Remember("where", "works from home", Provenance{Source: "person", Turn: 11, At: day("2026-10-12")}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	got := string(b)
	added := "- works from home <!-- person t=11 2026-10-12 -->\n"
	_, rest, _ := strings.Cut(got, "mood: settled\n---\n")
	if rest != facts+added+past {
		t.Errorf("the file after a remember:\n%s", got)
	}
	tp, _ := ReadTopic(dir, "where")
	if len(tp.Facts) != 2 || len(tp.Past) != 2 || tp.Past[1].Line != "used to live in Perth" || !tp.Past[1].Retired.IsZero() {
		t.Errorf("topic = %+v", tp)
	}
}

// The index and its line counts leave out the past; a topic with only
// past lines is not in it, and Reindex counts and indexes facts alone.
func TestIndexExcludesPast(t *testing.T) {
	s, dir := openTest(t)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, "topics", name+".md"), []byte("---\ntopic: "+name+"\n---\n"+body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("where", "- lives in Brisbane\n\n## Past\n- lives in Sydney\n- lives in Perth\n")
	write("gone", "## Past\n- had a dog\n")
	db := s.(*fileStore).db
	topics, lines, err := Reindex(dir, db)
	if err != nil {
		t.Fatal(err)
	}
	if topics != 2 || lines != 1 {
		t.Errorf("reindexed %d topics, %d lines; want 2, 1", topics, lines)
	}
	idx, err := s.Index()
	if err != nil {
		t.Fatal(err)
	}
	if idx != "where (1): lives in Brisbane\n" {
		t.Errorf("index = %q", idx)
	}
	for _, q := range []string{"Sydney", "Perth", "dog"} {
		if hits, _ := s.Recall(context.Background(), q, 5); len(hits) != 0 {
			t.Errorf("recall %s found a past line: %+v", q, hits)
		}
	}
	ts, _ := Topics(dir)
	if len(ts) != 2 || ts[1].Name != "where" || ts[1].Lines != 1 || ts[1].Past != 2 {
		t.Errorf("topics = %+v", ts)
	}
}
