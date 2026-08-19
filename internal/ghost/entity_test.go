package ghost

import (
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

func TestEntitySlug(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Sam", "sam"},
		{"sam,", "sam"},
		{"  SAM  ", "sam"},
		{"the haunt driver", "the-haunt-driver"},
		{"C++", "c"},
		{"", ""},
		{"!!!", ""},
	} {
		if got := entitySlug(tc.in); got != tc.want {
			t.Errorf("entitySlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestMentionsWholeWords: the entity path is a substring match, which is
// only safe because it is anchored. "Sam" must not fire on "same", or a
// ghost that knows one person starts explaining them at random.
func TestMentionsWholeWords(t *testing.T) {
	for _, tc := range []struct {
		text, name string
		want       bool
	}{
		{"how is sam doing", "sam", true},
		{"sam?", "sam", true},
		{"(sam)", "sam", true},
		{"the same thing", "sam", false},
		{"flotsam", "sam", false},
		{"i am going", "go", false},
		{"working on go today", "go", true},
		{"", "sam", false},
		{"anything", "", false},
	} {
		if got := mentions(tc.text, tc.name); got != tc.want {
			t.Errorf("mentions(%q, %q) = %v, want %v", tc.text, tc.name, got, tc.want)
		}
	}
}

// TestEntityMerge: the same subject named twice is one entity, gaining
// facts rather than replacing them, and a fact volunteered twice is
// agreement rather than news.
func TestEntityMerge(t *testing.T) {
	s := &memStore{m: map[string]string{}}
	g := &Ghost{store: s, log: hlog.Discard()}

	g.recordEntity("Sam", "person", []string{"drinks oolong"})
	g.recordEntity("sam,", "person", []string{"Drinks oolong", "is writing a compiler"})

	ents := g.loadEntities()
	if len(ents) != 1 {
		t.Fatalf("stored %d entities, want 1: %+v", len(ents), ents)
	}
	e := ents[0]
	if len(e.Facts) != 2 {
		t.Errorf("facts = %v, want two (the repeat is not news)", e.Facts)
	}
	if e.Mentions != 2 {
		t.Errorf("mentions = %d, want 2", e.Mentions)
	}
	if e.Name != "Sam" {
		t.Errorf("name = %q; the first spelling is kept, not the punctuation "+
			"a later mention happened to carry", e.Name)
	}
}

// TestEntityRejectsSentences: asking a small model for subjects reliably
// produces the occasional sentence where a name belongs, and one of those
// in the index would be pasted into every prompt that happened to contain
// its words.
func TestEntityRejectsSentences(t *testing.T) {
	s := &memStore{m: map[string]string{}}
	g := &Ghost{store: s, log: hlog.Discard()}

	g.recordEntity(strings.Repeat("a name far too long to be one ", 4), "person", []string{"x"})
	g.recordEntity("", "person", []string{"x"})
	// A subject nothing is known about is not worth a slot either.
	g.recordEntity("Sam", "person", nil)

	if ents := g.loadEntities(); len(ents) != 0 {
		t.Errorf("stored %d entities, want none: %+v", len(ents), ents)
	}
}

// TestEntityContext: the payoff. "who is Sam" is a question about a
// subject, and cosine recall can only return the nearest exchange and hope;
// this path answers it completely and costs nothing at query time.
func TestEntityContext(t *testing.T) {
	s := &memStore{m: map[string]string{}}
	g := &Ghost{store: s, log: hlog.Discard()}
	g.recordEntity("Sam", "person", []string{"drinks oolong", "is writing a compiler"})
	g.recordEntity("Biscuit", "cat", []string{"sits on the keyboard"})

	got := g.entityContext("how is Sam getting on")
	if !strings.Contains(got, "Sam (person)") || !strings.Contains(got, "writing a compiler") {
		t.Errorf("entity context missing what it knows about Sam:\n%s", got)
	}
	if strings.Contains(got, "Biscuit") {
		t.Errorf("entity context volunteered an unmentioned subject:\n%s", got)
	}
	if got := g.entityContext("what is the weather like"); got != "" {
		t.Errorf("entity context on an utterance naming nobody:\n%s", got)
	}
}

// TestEntityEvictionKeepsTheTalkedAbout: the index is bounded like the
// memory ring, and gives up the subject nobody has mentioned in a while
// rather than the one that keeps coming up.
func TestEntityEvictionKeepsTheTalkedAbout(t *testing.T) {
	s := &memStore{m: map[string]string{}}
	g := &Ghost{store: s, log: hlog.Discard()}

	g.recordEntity("Sam", "person", []string{"the one who matters"})
	for i := 0; i < 8; i++ {
		g.recordEntity("Sam", "person", []string{"fact " + string(rune('a'+i))})
	}
	for i := 0; i < maxEntities+5; i++ {
		g.recordEntity("passer-by-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "person",
			[]string{"came up once"})
	}

	ents := g.loadEntities()
	if len(ents) > maxEntities {
		t.Errorf("entity index grew to %d, past the cap of %d", len(ents), maxEntities)
	}
	found := false
	for _, e := range ents {
		if e.Name == "Sam" {
			found = true
		}
	}
	if !found {
		t.Error("the most-mentioned subject was evicted before the one-offs")
	}
}
