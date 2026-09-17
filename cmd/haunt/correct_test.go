package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/store"
	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestCorrectingARunningGhost is the whole feature end to end, and the thing
// it is really testing is the flow: *see what it thinks* → *say that is
// wrong* → *watch it stop thinking that*. Each of those three steps is a
// separate program talking to a live ghost, and the middle one is the only
// one that did not exist before.
//
// It starts from an index written the old way — bare strings, no
// provenance, no state — because that is what every installed ghost holds,
// and a correction that only worked on facts written after the struct
// existed would be a correction nobody could use for months.
//
// The observable is the system prompt, because that is where the failure
// lived: the entity path retrieves by *name*, so the wrong fact went in
// front of the model every single time its subject was mentioned.
func TestCorrectingARunningGhost(t *testing.T) {
	c := testenv.StartT(t)
	prompts := &systemPrompts{}
	backend := fakeMemoryBackend(t, prompts)
	defer backend.Close()

	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	cfgDir := filepath.Join(dir, "config")
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`[ai.llm]
base_url = %q
model = "fake"

[ai.embed]
model = "fake-embed"

[ai.budget]
llm_requests_per_hour = 20
`, backend.URL+"/v1")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	// An entity index as an installed ghost actually holds one: facts are
	// bare strings. Seeded before the ghost starts, which is the only time
	// anything but the ghost may write these keys — while it runs the index
	// is resident and a second writer would be silently overwritten.
	seedLegacyEntity(t, stateDir)

	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[brain]
builtin = true

[persona]
system = "You are Wisp."
history_turns = 1
`)

	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "100", "-y", "100",
		"-state", stateDir, "-record", record, gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_CONFIG_DIR="+cfgDir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	})
	waitFor(t, stdout, "READY")

	env := append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	runHaunt := func(args ...string) string {
		t.Helper()
		hc := exec.Command(bin, args...)
		hc.Env = env
		out, err := hc.CombinedOutput()
		if err != nil {
			t.Fatalf("haunt %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	replies := 0
	say := func(text string) {
		t.Helper()
		runHaunt("say", "-ghost", "wisp", text)
		replies++
		waitRecordCount(t, record, `"type":"ai_reply"`, replies)
	}

	// Step one: see what it thinks. The numbers are the contract `haunt
	// correct` is pointed at, and they have to be there for an index that
	// never had them.
	listing := runHaunt("journal", "-entities", "-state", stateDir)
	for _, want := range []string{"Sam (person)", "1  works at Acme", "2  drinks oolong"} {
		if !strings.Contains(listing, want) {
			t.Fatalf("journal -entities missing %q:\n%s", want, listing)
		}
	}

	// The failure itself: naming the subject puts every belief about it in
	// front of the model, whether or not it bears on what was said.
	say("how is sam doing")
	if got := prompts.last(); !strings.Contains(got, "works at Acme") {
		t.Fatalf("the belief under test never reached the prompt:\n%s", got)
	}

	// Step two: say that is wrong. This is the part that did not exist.
	runHaunt("correct", "-ghost", "wisp", "Sam", "1", "left Acme in March")

	// Step three: watch it stop thinking that. Same utterance, same name,
	// and the retired belief is gone from the prompt while the untouched
	// one is not — and what the person said is marked as theirs, because
	// blocking supersession at write time does nothing here if the model is
	// handed one undifferentiated list.
	say("how is sam doing")
	got := prompts.last()
	if strings.Contains(got, "works at Acme") {
		t.Errorf("a corrected belief still reaches the model:\n%s", got)
	}
	if !strings.Contains(got, "left Acme in March") {
		t.Errorf("the correction never reached the model:\n%s", got)
	}
	if !strings.Contains(got, "they told you") {
		t.Errorf("the person's own correction is not marked as theirs:\n%s", got)
	}
	if !strings.Contains(got, "drinks oolong") {
		t.Errorf("a correction took an untouched belief with it:\n%s", got)
	}

	// And it superseded rather than deleted: what it used to think is still
	// readable, which is the property that makes "you used to think X"
	// something a person can check rather than have to remember.
	listing = runHaunt("journal", "-entities", "-state", stateDir)
	if !strings.Contains(listing, "no longer: works at Acme") {
		t.Errorf("the retired belief left no trace:\n%s", listing)
	}
	if !strings.Contains(listing, "(you told it)") {
		t.Errorf("the journal does not say which belief came from the person:\n%s", listing)
	}

	// Naming a subject it has never heard of is refused rather than
	// silently filed, which is the difference between a correction and a
	// second way to write facts.
	bad := exec.Command(bin, "correct", "-ghost", "wisp", "Moth", "is a ghost")
	bad.Env = env
	if out, err := bad.CombinedOutput(); err == nil {
		t.Errorf("correcting an unknown subject succeeded: %s", out)
	}

	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("ghost exited uncleanly: %v", err)
	}
}

// seedLegacyEntity writes an entity index in the shape ghosts installed
// before facts were structs actually hold: a JSON array of bare strings.
func seedLegacyEntity(t *testing.T, stateDir string) {
	t.Helper()
	s, err := store.Open(filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	raw, err := json.Marshal(map[string]any{
		"n": "Sam",
		"k": "person",
		"f": []string{"works at Acme", "drinks oolong"},
		"a": now.Add(-90 * 24 * time.Hour),
		"b": now,
		"c": 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("_haunt/entity/sam", string(raw)); err != nil {
		t.Fatal(err)
	}
}
