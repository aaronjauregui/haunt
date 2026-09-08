package ghost

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

func correctable(t *testing.T) *Ghost {
	t.Helper()
	return &Ghost{store: &memStore{m: map[string]string{}}, log: hlog.Discard()}
}

func subjectNamed(t *testing.T, g *Ghost, name string) Entity {
	t.Helper()
	for _, e := range g.loadEntities() {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("no entity named %q in %+v", name, g.loadEntities())
	return Entity{}
}

// A correction retires the belief it names and records what is true, and
// the retired one stops reaching the prompt without leaving the record.
func TestCorrectionSupersedesRatherThanDeletes(t *testing.T) {
	g := correctable(t)
	g.recordEntity("Sam", "person", FactInference, []string{"works at Acme", "drinks oolong"})

	sam := subjectNamed(t, g, "Sam")
	var wrong Fact
	for _, f := range sam.Facts {
		if f.Text == "works at Acme" {
			wrong = f
		}
	}
	if wrong.ID == 0 {
		t.Fatalf("facts have no ids: %+v", sam.Facts)
	}

	if _, err := g.applyCorrection("sam", wrong.ID, "left Acme in March"); err != nil {
		t.Fatal(err)
	}

	sam = subjectNamed(t, g, "Sam")
	if n := len(sam.Facts); n != 3 {
		t.Fatalf("want the retired fact kept, got %d facts: %+v", n, sam.Facts)
	}
	standing := sam.StandingFacts()
	if len(standing) != 2 {
		t.Fatalf("want two standing facts, got %+v", standing)
	}
	for _, f := range standing {
		if f.Text == "works at Acme" {
			t.Error("the corrected belief is still standing")
		}
	}

	// It stops entering context, and what the person said is marked as
	// theirs so the model is not left weighing two anonymous clauses.
	ctx := g.entityContext("how is Sam")
	if strings.Contains(ctx, "works at Acme") {
		t.Errorf("a retired belief reached the prompt: %s", ctx)
	}
	if !strings.Contains(ctx, "they told you: left Acme in March") {
		t.Errorf("the person's own correction is not marked as theirs: %s", ctx)
	}
	if !strings.Contains(ctx, "drinks oolong") {
		t.Errorf("an untouched belief was lost: %s", ctx)
	}
}

// The point of supersession over deletion: what the ghost used to think is
// still readable, and says what replaced it.
func TestRetiredFactKeepsItsTrail(t *testing.T) {
	g := correctable(t)
	g.recordEntity("Sam", "person", FactInference, []string{"works at Acme"})
	old := subjectNamed(t, g, "Sam").Facts[0]

	if _, err := g.applyCorrection("Sam", old.ID, "left Acme in March"); err != nil {
		t.Fatal(err)
	}
	sam := subjectNamed(t, g, "Sam")

	var retired, replacement Fact
	for _, f := range sam.Facts {
		if f.Standing() {
			replacement = f
		} else {
			retired = f
		}
	}
	if retired.Until.IsZero() {
		t.Fatal("the retired fact has no time of death")
	}
	if retired.By != replacement.ID {
		t.Errorf("retired fact points at %d, replacement is %d", retired.By, replacement.ID)
	}
	if replacement.From != FactPerson {
		t.Errorf("a correction was filed as %q, want %q", replacement.From, FactPerson)
	}
}

// The failure this exists to prevent: the ghost concludes something, is
// corrected, and files the same conclusion again on the next pass. Nothing
// with less authority than the person may re-file what the person threw out.
func TestCorrectedBeliefIsNotRefiled(t *testing.T) {
	g := correctable(t)
	g.recordEntity("Sam", "person", FactInference, []string{"works at Acme"})
	old := subjectNamed(t, g, "Sam").Facts[0]
	if _, err := g.applyCorrection("Sam", old.ID, "left Acme in March"); err != nil {
		t.Fatal(err)
	}

	// Consolidation reads the same old exchanges and draws the same
	// conclusion. It must not stand again.
	g.recordEntity("Sam", "person", FactInference, []string{"works at Acme"})
	g.recordEntity("Sam", "person", FactExchange, []string{"WORKS AT ACME"})

	for _, f := range subjectNamed(t, g, "Sam").StandingFacts() {
		if strings.EqualFold(f.Text, "works at Acme") {
			t.Fatalf("a corrected belief came back: %+v", f)
		}
	}

	// The person may always change their own mind, though.
	sam := subjectNamed(t, g, "Sam")
	if _, err := g.applyCorrection("Sam", 0, "works at Acme"); err != nil {
		t.Fatal(err)
	}
	var back bool
	for _, f := range subjectNamed(t, g, "Sam").StandingFacts() {
		back = back || f.Text == "works at Acme"
	}
	if !back {
		t.Errorf("the person could not restore their own fact: %+v", sam.Facts)
	}
}

// Correcting a correction is an ordinary correction: no new rule, and the
// person is never blocked by their own earlier word.
func TestCorrectingACorrection(t *testing.T) {
	g := correctable(t)
	g.recordEntity("Sam", "person", FactInference, []string{"works at Acme"})

	first := subjectNamed(t, g, "Sam").Facts[0]
	if _, err := g.applyCorrection("Sam", first.ID, "left Acme in March"); err != nil {
		t.Fatal(err)
	}
	var second Fact
	for _, f := range subjectNamed(t, g, "Sam").StandingFacts() {
		second = f
	}
	if _, err := g.applyCorrection("Sam", second.ID, "rejoined Acme in September"); err != nil {
		t.Fatal(err)
	}

	standing := subjectNamed(t, g, "Sam").StandingFacts()
	if len(standing) != 1 || standing[0].Text != "rejoined Acme in September" {
		t.Fatalf("want only the latest correction standing, got %+v", standing)
	}
}

// The cap counts standing facts, so correcting the ghost can never push a
// true fact off the end — and the list as a whole is still bounded, because
// "kept past the cap" on its own is an unbounded write.
func TestBothFactCaps(t *testing.T) {
	g := correctable(t)
	for i := 0; i < maxEntityFacts; i++ {
		g.recordEntity("Sam", "person", FactInference, []string{"fact " + string(rune('a'+i))})
	}
	if n := len(subjectNamed(t, g, "Sam").StandingFacts()); n != maxEntityFacts {
		t.Fatalf("want %d standing, got %d", maxEntityFacts, n)
	}

	// Retiring one must not cost a true fact its slot.
	sam := subjectNamed(t, g, "Sam")
	if _, err := g.applyCorrection("Sam", sam.Facts[0].ID, "actually something else"); err != nil {
		t.Fatal(err)
	}
	sam = subjectNamed(t, g, "Sam")
	if n := len(sam.StandingFacts()); n != maxEntityFacts {
		t.Errorf("a correction cost a standing slot: %d standing", n)
	}
	for _, want := range []string{"fact b", "fact c", "fact d", "fact e", "fact f"} {
		found := false
		for _, f := range sam.StandingFacts() {
			found = found || f.Text == want
		}
		if !found {
			t.Errorf("%q was pushed off by a correction", want)
		}
	}

	// And the whole list stays bounded however often it is corrected.
	for i := 0; i < 40; i++ {
		sam = subjectNamed(t, g, "Sam")
		var victim Fact
		for _, f := range sam.Facts {
			if f.Standing() {
				victim = f
				break
			}
		}
		if _, err := g.applyCorrection("Sam", victim.ID, "correction number "+string(rune('a'+i%26))); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(subjectNamed(t, g, "Sam").Facts); n > maxEntityFactSlots {
		t.Errorf("the fact list grew to %d, past the %d ceiling", n, maxEntityFactSlots)
	}
}

// An index written before facts were structs decodes as bare text with no
// provenance and no state — the weakest kind, so a correction can retire it
// without a migration pass.
func TestLegacyBareStringFactsDecode(t *testing.T) {
	legacy := `{"n":"Sam","k":"person","f":["works at Acme","drinks oolong"],` +
		`"a":"2024-01-01T00:00:00Z","b":"2024-02-01T00:00:00Z","c":3}`
	e, err := DecodeEntity(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Facts) != 2 || e.Facts[0].Text != "works at Acme" {
		t.Fatalf("decoded %+v", e.Facts)
	}
	if e.Facts[0].From != FactInference {
		t.Errorf("a legacy fact claims provenance %q", e.Facts[0].From)
	}
	if !e.Facts[0].Standing() {
		t.Error("a legacy fact decoded as already retired")
	}
	// Ids, so a correction has something stable to name.
	if e.Facts[0].ID == 0 || e.Facts[0].ID == e.Facts[1].ID {
		t.Errorf("legacy facts have no distinct ids: %+v", e.Facts)
	}

	// And it round-trips into the new shape.
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	again, err := DecodeEntity(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if again.Facts[1].Text != "drinks oolong" {
		t.Errorf("round trip lost a fact: %+v", again.Facts)
	}
}

// A correction names a subject the ghost knows and a belief it actually
// holds, and says so plainly when it does not.
func TestCorrectionRefusesWhatItCannotFind(t *testing.T) {
	g := correctable(t)
	g.recordEntity("Sam", "person", FactInference, []string{"works at Acme"})

	if _, err := g.applyCorrection("Moth", 0, "is a ghost"); err == nil {
		t.Error("corrected a subject it has never heard of")
	}
	if _, err := g.applyCorrection("Sam", 99, "something"); err == nil {
		t.Error("retired a belief that does not exist")
	}
	if _, err := g.applyCorrection("Sam", 0, ""); err == nil {
		t.Error("accepted a correction with nothing in it")
	}

	// Retiring twice is refused too: the second call names a fact that is
	// no longer standing.
	sam := subjectNamed(t, g, "Sam")
	if _, err := g.applyCorrection("Sam", sam.Facts[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.applyCorrection("Sam", sam.Facts[0].ID, ""); err == nil {
		t.Error("retired the same belief twice")
	}
}

// A subject with nothing left standing stops being described at all, rather
// than appearing in a prompt as a name with no content.
func TestFullyRetractedSubjectLeavesContext(t *testing.T) {
	g := correctable(t)
	g.recordEntity("Sam", "person", FactInference, []string{"works at Acme"})
	sam := subjectNamed(t, g, "Sam")
	if _, err := g.applyCorrection("Sam", sam.Facts[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	if ctx := g.entityContext("how is Sam"); ctx != "" {
		t.Errorf("a subject with no standing facts still reached the prompt: %q", ctx)
	}
}

// Correcting a subject protects its slot: a name somebody cared enough to
// fix must not be the next one evicted.
func TestCorrectionRefreshesTheSubject(t *testing.T) {
	g := correctable(t)
	g.recordEntity("Sam", "person", FactInference, []string{"works at Acme"})
	before := subjectNamed(t, g, "Sam")
	before.Last = time.Now().Add(-90 * 24 * time.Hour)
	before.Mentions = 1
	if err := g.putEntity(before); err != nil {
		t.Fatal(err)
	}

	if _, err := g.applyCorrection("Sam", before.Facts[0].ID, "left Acme"); err != nil {
		t.Fatal(err)
	}
	after := subjectNamed(t, g, "Sam")
	if !after.Last.After(before.Last) || after.Mentions <= before.Mentions {
		t.Errorf("a correction did not refresh the subject: %+v", after)
	}
}

// The socket path. A correction is settled by the driver and never reaches
// the brain, and it does not depend on the "message" capability: that
// capability is about whether outside programs may put words in front of
// the person, and a correction is the person editing what their own ghost
// believes about them. A ghost that could be read and not told it is wrong
// is the exact asymmetry revision exists to remove.
func TestCorrectionOverTheControlSocket(t *testing.T) {
	srv := fakeEmbedServer(t, 8, nil)
	defer srv.Close()

	s := &memStore{m: map[string]string{}}
	g := testGhost(t, s, srv.URL, "test-model")
	// "llm" only: no "message" grant anywhere.
	g.recordEntity("Sam", "person", FactInference, []string{"works at Acme"})
	sam := subjectNamed(t, g, "Sam")

	err := g.publishFromControl(proto.Event{
		Type: proto.EventCorrect,
		Name: "Sam",
		Fact: sam.Facts[0].ID,
		Text: "left Acme in March",
	})
	if err != nil {
		t.Fatalf("a correction was refused without the message capability: %v", err)
	}
	standing := subjectNamed(t, g, "Sam").StandingFacts()
	if len(standing) != 1 || standing[0].Text != "left Acme in March" {
		t.Fatalf("the correction did not land: %+v", standing)
	}
	if standing[0].From != FactPerson {
		t.Errorf("a socket correction was filed as %q", standing[0].From)
	}

	// And an ordinary message over the same socket is still gated.
	if err := g.publishFromControl(proto.Event{Type: proto.EventMessage, Text: "hello"}); err == nil {
		t.Error("a message was accepted without the message capability")
	}
}

// A ghost with no memory says so rather than failing obscurely.
func TestCorrectionNeedsMemory(t *testing.T) {
	g := correctable(t)
	g.manifest = &Manifest{Ghost: GhostSection{Capabilities: []string{"message"}}}
	err := g.publishFromControl(proto.Event{Type: proto.EventCorrect, Name: "Sam", Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "no memory") {
		t.Errorf("want a plain refusal, got %v", err)
	}
}
