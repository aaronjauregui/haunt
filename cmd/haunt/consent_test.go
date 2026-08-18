package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestConsentInTheBalloon: an installed ghost whose capabilities were all
// withheld asks for one when it first needs it, and the answer sticks.
//
// This is the consent model the design always wanted and the install sheet
// could never be: a wall of checkboxes at the least informed moment,
// replaced by a question about the thing happening right now.
func TestConsentInTheBalloon(t *testing.T) {
	c := testenv.StartT(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"allowed at last\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer backend.Close()

	dir := t.TempDir()
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("[ai.llm]\nbase_url = %q\nmodel = \"fake\"\n", backend.URL+"/v1")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	envAll := []string{
		"HAUNT_CONFIG_DIR=" + cfgDir,
		"HAUNT_DATA_DIR=" + filepath.Join(dir, "data"),
		"HAUNT_RUNTIME_DIR=" + filepath.Join(dir, "run"),
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), envAll...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("haunt %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	// A ghost that asks for the model, installed by someone who answered
	// "N" — which is the default at that prompt, and the state this whole
	// feature exists for.
	src := filepath.Join(dir, "wisp")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `[ghost]
name = "wisp"
version = "0.1.0"
capabilities = ["message", "llm"]

[persona]
system = "You are Wisp."
`
	if err := os.WriteFile(filepath.Join(src, "ghost.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	run("keygen")
	pkg := filepath.Join(dir, "wisp.haunt")
	run("pack", "-o", pkg, src)
	run("install", pkg) // nothing granted
	// Except the socket `haunt say` needs, which cannot be turned on while
	// the ghost runs (no socket, nothing to ask through) — the one
	// capability this feature deliberately leaves to install time.
	run("perms", "grant", "message")
	if out := run("perms"); !strings.Contains(out, "[no ] llm") {
		t.Fatalf("expected llm withheld:\n%s", out)
	}

	kb, err := c.NewKeyboard()
	if err != nil {
		t.Fatal(err)
	}
	defer kb.Close()

	const spriteX, spriteY = 100, 100
	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "100", "-y", "100", "-record", record)
	cmd.Env = append(c.Env(), append(envAll, "HAUNT_DEBUG=balloon,session")...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
		if t.Failed() {
			t.Logf("ghost stderr:\n%s", stderr.String())
		}
	})
	waitFor(t, stdout, "READY")

	say := func(text string) {
		t.Helper()
		sc := exec.Command(bin, "say", "-ghost", "wisp", text)
		sc.Env = append(os.Environ(), envAll...)
		if out, err := sc.CombinedOutput(); err != nil {
			t.Fatalf("say: %v: %s", err, out)
		}
	}

	// Talking to it needs the model it was never granted, so it asks —
	// about this, now, rather than at install time.
	say("hello")
	waitLog(t, stderr, "asking whether wisp may llm")
	waitBalloon(t, c, dir, spriteX, spriteY, true)

	// Answered from the keyboard: "1" is Allow.
	if err := kb.Key(2); err != nil {
		t.Fatal(err)
	}

	// The grant is written where `haunt perms` reads it...
	waitLog(t, stderr, "granted: llm")
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(run("perms"), "[yes] llm") {
		if time.Now().After(deadline) {
			t.Fatalf("the grant never reached the grants file:\n%s", run("perms"))
		}
		wait100ms()
	}

	// ...and the request that provoked the question goes through, so the
	// answer arrives as if nothing had happened.
	waitRecordContains(t, record, "allowed at last")

	// Asked again later, it does not ask again: the grant answers for
	// itself now.
	before := strings.Count(stderr.String(), "asking whether")
	say("and again")
	waitRecordCount(t, record, `"type":"ai_reply"`, 2)
	if got := strings.Count(stderr.String(), "asking whether"); got != before {
		t.Errorf("it asked again about something already granted (%d → %d)", before, got)
	}
}

// "Never ask again" has to survive the session, or no is only ever "not
// yet" — the thing that makes a permission prompt a nuisance.
func TestNeverAskAgainIsRecorded(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A backend that would answer, so a refusal is the only reason nothing
	// happens.
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"),
		[]byte("[ai.llm]\nbase_url = \"http://127.0.0.1:1/v1\"\nmodel = \"fake\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	envAll := []string{
		"HAUNT_CONFIG_DIR=" + cfgDir,
		"HAUNT_DATA_DIR=" + filepath.Join(dir, "data"),
		"HAUNT_RUNTIME_DIR=" + filepath.Join(dir, "run"),
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), envAll...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("haunt %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	src := filepath.Join(dir, "wisp")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "ghost.toml"), []byte(`[ghost]
name = "wisp"
version = "0.1.0"
capabilities = ["message", "llm"]

[persona]
system = "You are Wisp."
`), 0o644); err != nil {
		t.Fatal(err)
	}
	run("keygen")
	pkg := filepath.Join(dir, "wisp.haunt")
	run("pack", "-o", pkg, src)
	run("install", pkg)
	run("perms", "grant", "message") // the way in; llm stays withheld

	kb, err := c.NewKeyboard()
	if err != nil {
		t.Fatal(err)
	}
	defer kb.Close()

	cmd := exec.Command(bin, "run", "-x", "100", "-y", "100")
	cmd.Env = append(c.Env(), append(envAll, "HAUNT_DEBUG=session")...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
		if t.Failed() {
			t.Logf("ghost stderr:\n%s", stderr.String())
		}
	})
	waitFor(t, stdout, "READY")

	say := exec.Command(bin, "say", "-ghost", "wisp", "hello")
	say.Env = append(os.Environ(), envAll...)
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}
	waitLog(t, stderr, "asking whether wisp may llm")
	// The log line comes before the panel: wait for the question to be on
	// screen and holding the keyboard, or the answer lands on nothing.
	waitBalloon(t, c, dir, 100, 100, true)

	// "3" is Never ask again.
	if err := kb.Key(4); err != nil {
		t.Fatal(err)
	}
	waitLog(t, stderr, "refused for good")

	// The listing says so, in words that distinguish "not yet" from "the
	// ghost has been told to stop asking".
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(run("perms"), "will not ask again") {
		if time.Now().After(deadline) {
			t.Fatalf("the refusal is not visible in perms:\n%s", run("perms"))
		}
		wait100ms()
	}

	// And granting it later clears the refusal: saying yes is also saying
	// the ghost may ask about this again.
	run("perms", "grant", "llm")
	out := run("perms")
	if strings.Contains(out, "will not ask again") || !strings.Contains(out, "[yes] llm") {
		t.Errorf("granting did not clear the refusal:\n%s", out)
	}
}
