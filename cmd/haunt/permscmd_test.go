package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestPermsListGrantRevoke: the listing says what was asked for and what
// was given, and the two verbs change it. Consent used to be a single
// yes/no at install with no way back; this is the way back.
func TestPermsListGrantRevoke(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
	env := []string{
		"HAUNT_CONFIG_DIR=" + filepath.Join(dir, "config"),
		"HAUNT_DATA_DIR=" + filepath.Join(dir, "data"),
		"HAUNT_RUNTIME_DIR=" + filepath.Join(dir, "run"),
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("haunt %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	fails := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("haunt %s succeeded, want an error: %s", strings.Join(args, " "), out)
		}
		return string(out)
	}

	// An installed ghost that asks for three things, granted none of them:
	// install's own "[y/N]" default, which is the case that most needs a
	// way back.
	ghostDir := filepath.Join(dir, "wisp")
	if err := os.MkdirAll(ghostDir, 0o755); err != nil {
		t.Fatal(err)
	}
	build(t, filepath.Join(ghostDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")
	manifest := `[ghost]
name = "wisp"
version = "0.1.0"
capabilities = ["message", "llm", "window"]

[brain]
wasm = "brain.wasm"
`
	if err := os.WriteFile(filepath.Join(ghostDir, "ghost.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	run("keygen")
	pkg := filepath.Join(dir, "wisp.haunt")
	run("pack", "-o", pkg, ghostDir)
	run("install", pkg) // no -yes: nothing granted

	out := run("perms")
	for _, want := range []string{"wisp", "[no ] message", "[no ] llm", "[no ] window"} {
		if !strings.Contains(out, want) {
			t.Errorf("perms listing missing %q:\n%s", want, out)
		}
	}
	// The listing explains what each capability means, since "window" on
	// its own is not consent to anything a person could weigh.
	if !strings.Contains(out, "see which application") {
		t.Errorf("the listing does not say what the capabilities do:\n%s", out)
	}

	if out := run("perms", "grant", "llm"); !strings.Contains(out, "granted llm") {
		t.Errorf("grant output: %s", out)
	}
	if out := run("perms"); !strings.Contains(out, "[yes] llm") {
		t.Errorf("listing after grant:\n%s", out)
	}
	if out := run("perms", "revoke", "llm"); !strings.Contains(out, "revoked llm") {
		t.Errorf("revoke output: %s", out)
	}
	if out := run("perms"); !strings.Contains(out, "[no ] llm") {
		t.Errorf("listing after revoke:\n%s", out)
	}

	// Repeats say so rather than pretending to have done something.
	run("perms", "grant", "llm")
	if out := run("perms", "grant", "llm"); !strings.Contains(out, "already has") {
		t.Errorf("second grant: %s", out)
	}
	run("perms", "revoke", "llm")
	if out := run("perms", "revoke", "llm"); !strings.Contains(out, "does not have") {
		t.Errorf("second revoke: %s", out)
	}

	// Granting something the ghost never asked for would be theatre: the
	// driver needs both a declaration and a grant, so it would stay
	// inactive and the listing would say so forever.
	if out := fails("perms", "grant", "screen"); !strings.Contains(out, "does not ask for") {
		t.Errorf("granting an undeclared capability: %s", out)
	}
	// And a typo is answered with the real names.
	if out := fails("perms", "grant", "windows"); !strings.Contains(out, "window") {
		t.Errorf("typo output: %s", out)
	}
	if out := fails("perms", "nosuchghost"); !strings.Contains(out, "is not installed") {
		t.Errorf("unknown ghost: %s", out)
	}

	// A ghost that checks something on a schedule says so here, which is
	// the one place a person can settle a standing arrangement in advance:
	// a poll asks in the balloon, but only while somebody is at the desk.
	moth := filepath.Join(dir, "moth")
	if err := os.MkdirAll(moth, 0o755); err != nil {
		t.Fatal(err)
	}
	pollster := `[ghost]
name = "moth"
version = "0.1.0"
capabilities = ["llm", "tools"]

[persona]
system = "You are Moth."

[[tool]]
name = "calendar"
command = ["mcp-caldav"]

[[trigger]]
cron = "*/15 * * * *"
name = "calendar"
tool = "calendar.upcoming"
args = { within = "2h" }
`
	if err := os.WriteFile(filepath.Join(moth, "ghost.toml"), []byte(pollster), 0o644); err != nil {
		t.Fatal(err)
	}
	mothPkg := filepath.Join(dir, "moth.haunt")
	run("pack", "-o", mothPkg, moth)
	run("install", mothPkg)

	out = run("perms", "moth")
	for _, want := range []string{"on a schedule", "calendar.upcoming", "*/15 * * * *", "[no ]"} {
		if !strings.Contains(out, want) {
			t.Errorf("perms listing for a polling ghost missing %q:\n%s", want, out)
		}
	}
	if out := run("perms", "grant", "tool:calendar.upcoming", "moth"); !strings.Contains(out, "granted") {
		t.Errorf("granting a scheduled tool in advance: %s", out)
	}
	if out := run("perms", "moth"); !strings.Contains(out, "[yes] calendar") {
		t.Errorf("the granted poll is not marked as allowed:\n%s", out)
	}
}

// TestPermsAppliesToARunningGhost is the whole point of the feature: a
// capability withdrawn while the ghost is running stops working, and one
// granted starts, without a restart. A permission you can only change by
// restarting the thing is not really under your control.
func TestPermsAppliesToARunningGhost(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	envFor := func(cmd *exec.Cmd) {
		cmd.Env = append(c.Env(),
			"HAUNT_CONFIG_DIR="+filepath.Join(dir, "config"),
			"HAUNT_DATA_DIR="+filepath.Join(dir, "data"),
			"HAUNT_RUNTIME_DIR="+filepath.Join(dir, "run"),
		)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		envFor(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("haunt %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	ghostDir := filepath.Join(dir, "whisper")
	if err := os.MkdirAll(ghostDir, 0o755); err != nil {
		t.Fatal(err)
	}
	build(t, filepath.Join(ghostDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")
	manifest := `[ghost]
name = "whisper"
version = "0.1.0"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
`
	if err := os.WriteFile(filepath.Join(ghostDir, "ghost.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	run("keygen")
	pkg := filepath.Join(dir, "whisper.haunt")
	run("pack", "-o", pkg, ghostDir)
	run("install", "-yes", pkg) // message granted

	daemon := exec.Command(bin, "run", "-x", "100", "-y", "100")
	envFor(daemon)
	stdout, err := daemon.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &syncBuffer{}
	daemon.Stderr = stderr
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		daemon.Process.Signal(syscall.SIGTERM)
		daemon.Wait()
		if t.Failed() {
			t.Logf("ghost stderr:\n%s", stderr.String())
		}
	})
	waitFor(t, stdout, "READY")

	// The message capability is what the control socket needs, so `haunt
	// say` working is the observable form of "granted".
	say := func() error {
		cmd := exec.Command(bin, "say", "hello")
		envFor(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		return nil
	}
	if err := say(); err != nil {
		t.Fatalf("say with the capability granted: %v", err)
	}

	// Withdraw it while the ghost is running.
	run("perms", "revoke", "message")
	waitLog(t, stderr, "revoked: message")

	// The socket stays open — it was created at startup, and closing it
	// would be a different change — but the events it carries are refused,
	// which is the guarantee that matters: the ghost stops acting on them.
	deadline := time.Now().Add(10 * time.Second)
	refused := false
	for time.Now().Before(deadline) {
		if err := say(); err != nil {
			refused = true
			break
		}
		if strings.Contains(stderr.String(), "capability not granted") {
			refused = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !refused {
		t.Errorf("the ghost kept accepting messages after the grant was revoked:\n%s", stderr.String())
	}

	// And granting it again is enough to bring it back.
	run("perms", "grant", "message")
	waitLog(t, stderr, "granted: message")
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := say(); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Error("the ghost never started accepting messages again")
}
