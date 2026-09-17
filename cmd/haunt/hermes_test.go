package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/hermes"
	"github.com/aaronjauregui/haunt/internal/store"
	"github.com/aaronjauregui/haunt/internal/testenv"
	"gopkg.in/yaml.v3"
)

// TestHermesBrainEndToEnd: a ghost with no [brain] runs on hermes, and the
// whole chain holds with no hermes installed — the driver writes the
// profile and starts the gateway (testdata/fakehermes), the gateway spawns
// `haunt mcp-serve` from that profile and lists the ghost's verbs through
// it, a boot envelope is answered with silence and nothing is shown, a
// person asking for a reminder becomes a tool call that lands in the
// ghost's store, and stopping the ghost stops the gateway.
func TestHermesBrainEndToEnd(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rig := newHermesRig(t, dir, nil, "")
	rt, cfgDir, fakeLog, bin := rig.RT, rig.CfgDir, rig.FakeLog, rig.Bin
	hermesHome := filepath.Join(dir, "hermes")
	stateDir := filepath.Join(dir, "state")
	const persona = "You are Wisp, a wry desktop spirit who keeps an eye on the kitchen."
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[persona]
system = "`+persona+`"
`)

	const posX, posY = 100, 100
	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", strconv.Itoa(posX), "-y", strconv.Itoa(posY),
		"-state", stateDir, "-record", record, gDir)
	cmd.Env = append(c.Env(), rig.env()...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			t.Logf("haunt: %s", sc.Text())
		}
	}()
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cmd.Process.Signal(syscall.SIGINT)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("haunt exited uncleanly: %v", err)
			}
		case <-time.After(30 * time.Second):
			cmd.Process.Kill()
			t.Errorf("haunt did not stop within 30s of SIGINT")
		}
	}
	t.Cleanup(stop)
	waitFor(t, stdout, "READY")
	go func() {
		// Keep draining, so the ghost never blocks on a full pipe.
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
		}
	}()

	// 1. The boot envelope reached the brain, and silence stayed silent.
	boot := waitFakeLog(t, fakeLog, 30*time.Second, func(e fakeEntry) bool {
		return e.Kind == "reply" && strings.HasPrefix(e.Input, hermes.EnvelopePrefix)
	})
	t.Logf("boot turn answered %s after start", time.Since(started).Round(time.Millisecond))
	if !strings.Contains(boot.Input, "boot") {
		t.Errorf("first envelope = %q, want the boot envelope", boot.Input)
	}
	if boot.Reply != "NO_REPLY" {
		t.Fatalf("boot reply = %q", boot.Reply)
	}
	chat := waitFakeLog(t, fakeLog, time.Second, func(e fakeEntry) bool {
		return e.Kind == "chat" && e.Input == boot.Input
	})
	if !strings.HasPrefix(chat.SystemMessage, "Now: ") {
		t.Errorf("system_message = %q, want the turn's ambient context", chat.SystemMessage)
	}
	if chat.SessionKey != "wisp" {
		t.Errorf("X-Hermes-Session-Key = %q, want the ghost's name", chat.SessionKey)
	}
	time.Sleep(time.Second)
	if balloonVisible(t, c, dir, posX, posY) {
		t.Error("a balloon is showing after a NO_REPLY boot")
	}

	// The gateway spawned the MCP server and saw the ghost's verbs.
	list := waitFakeLog(t, fakeLog, 10*time.Second, func(e fakeEntry) bool {
		return e.Kind == "tools_list" || e.Kind == "mcp_error"
	})
	if list.Kind != "tools_list" {
		t.Fatalf("MCP handshake failed: %s", list.Error)
	}
	for _, want := range []string{"intend", "say", "ask", "face", "resolve", "intentions"} {
		if !slices.Contains(list.Tools, want) {
			t.Errorf("tools/list = %v, missing %q", list.Tools, want)
		}
	}
	spawned := waitFakeLog(t, fakeLog, time.Second, func(e fakeEntry) bool { return e.Kind == "mcp_spawned" })
	if spawned.Command != bin || !slices.Equal(spawned.Args, []string{"mcp-serve", "--ghost", "wisp"}) {
		t.Errorf("MCP server = %s %v, want %s mcp-serve --ghost wisp", spawned.Command, spawned.Args, bin)
	}
	if spawned.Env["HAUNT_RUNTIME_DIR"] != rt {
		t.Errorf("MCP server env = %v, want HAUNT_RUNTIME_DIR=%s", spawned.Env, rt)
	}

	// 2. A person asks for a reminder; the model calls intend through MCP.
	sayAt := time.Now()
	sc := exec.Command(bin, "say", "-ghost", "wisp", "remind me in ten minutes to check the oven")
	sc.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	if out, err := sc.CombinedOutput(); err != nil {
		t.Fatalf("haunt say: %v: %s", err, out)
	}
	call := waitFakeLog(t, fakeLog, 30*time.Second, func(e fakeEntry) bool { return e.Kind == "tool_call" })
	if call.IsError || call.Error != "" || !strings.HasPrefix(call.Text, "hermes/") {
		t.Fatalf("intend call = %+v, want a result naming a hermes/ intention", call)
	}
	remind := waitFakeLog(t, fakeLog, 10*time.Second, func(e fakeEntry) bool {
		return e.Kind == "reply" && strings.Contains(e.Input, "remind me")
	})
	t.Logf("reminder turn answered %s after say", time.Since(sayAt).Round(time.Millisecond))
	if remind.Reply != "Done: "+call.Text {
		t.Errorf("reply = %q", remind.Reply)
	}
	intentions := waitHermesIntentions(t, bin, stateDir, rt, "check the oven")
	if !strings.Contains(intentions, "(due ") {
		t.Errorf("the reminder was filed without a due time:\n%s", intentions)
	}
	if !balloonVisible(t, c, dir, posX, posY) {
		t.Error("no balloon for the reply to the person")
	}

	// `haunt history` reads the conversation back from hermes, and the
	// silent boot turn reads as silence, not as the token.
	hc := exec.Command(bin, "history", "-why", "-state", stateDir, "wisp")
	hc.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_CONFIG_DIR="+cfgDir)
	out, err := hc.CombinedOutput()
	if err != nil {
		t.Fatalf("haunt history: %v: %s", err, out)
	}
	for _, want := range []string{"event: kind=boot", "ghost: (said nothing)", "woke, and chose not to speak",
		"you:   remind me in ten minutes", "ghost: Done: hermes/", "did something first: mcp__haunt__intend"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("history lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "NO_REPLY") {
		t.Errorf("history shows the silence token:\n%s", out)
	}

	// 3. The profile is the ghost's.
	profile := filepath.Join(hermesHome, "profiles", "haunt-wisp")
	soul, err := os.ReadFile(filepath.Join(profile, "SOUL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(soul), persona) {
		t.Errorf("SOUL.md does not carry the persona:\n%s", soul)
	}
	yml, err := os.ReadFile(filepath.Join(profile, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var pc struct {
		PlatformToolsets map[string][]string `yaml:"platform_toolsets"`
	}
	if err := yaml.Unmarshal(yml, &pc); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(pc.PlatformToolsets["api_server"], "haunt") {
		t.Errorf("platform_toolsets.api_server = %v, want haunt among them", pc.PlatformToolsets["api_server"])
	}

	// 4. Stopping the ghost stops the gateway and its MCP server.
	start := waitFakeLog(t, fakeLog, time.Second, func(e fakeEntry) bool { return e.Kind == "start" })
	listening := waitFakeLog(t, fakeLog, time.Second, func(e fakeEntry) bool { return e.Kind == "listening" })
	stopAt := time.Now()
	stop()
	t.Logf("ghost stopped in %s", time.Since(stopAt).Round(time.Millisecond))
	if err := syscall.Kill(start.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("fakehermes (pid %d) is still there after the ghost stopped: %v", start.PID, err)
	}
	if err := syscall.Kill(spawned.MCPPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("the MCP server (pid %d) is still there after the ghost stopped: %v", spawned.MCPPID, err)
	}
	if conn, err := net.DialTimeout("tcp", listening.Addr, time.Second); err == nil {
		conn.Close()
		t.Errorf("something still answers on %s", listening.Addr)
	}
	waitFakeLog(t, fakeLog, time.Second, func(e fakeEntry) bool { return e.Kind == "mcp_stopped" })

	// The store, read directly now the ghost is gone: the errand, and the
	// reasons for both turns.
	s, err := store.Open(filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, ok, err := s.Get(ghost.IntentionsStoreKey)
	if err != nil || !ok {
		t.Fatalf("no intentions in the store (%v)", err)
	}
	list2, err := ghost.DecodeIntentions(raw)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range list2 {
		if strings.HasPrefix(call.Text, it.Name+" ") && it.Text == "check the oven" {
			found = true
			if d := time.Until(it.Due); d < 8*time.Minute || d > 11*time.Minute {
				t.Errorf("due in %s, want about ten minutes", d)
			}
			if !it.Asked {
				t.Error("the errand was not recorded as asked for by the person")
			}
		}
	}
	if !found {
		t.Errorf("intentions = %+v, want %q", list2, call.Text)
	}
	raw, ok, err = s.Get(ghost.WhyStoreKey)
	if err != nil || !ok {
		t.Fatalf("no why-trace in the store (%v)", err)
	}
	whys, err := ghost.DecodeWhy(raw)
	if err != nil {
		t.Fatal(err)
	}
	var sawBoot, sawRemind bool
	for _, w := range whys {
		switch {
		case strings.HasPrefix(w.User, hermes.EnvelopePrefix):
			sawBoot = true
			if !w.Suppressed {
				t.Errorf("the boot turn was not recorded as silent: %+v", w)
			}
		case strings.Contains(w.User, "remind me"):
			sawRemind = true
			if w.Suppressed || !slices.Contains(w.Acted, "mcp__haunt__intend") {
				t.Errorf("the reminder turn = %+v, want spoken and acting through intend", w)
			}
		}
	}
	if !sawBoot || !sawRemind {
		t.Errorf("why-trace = %+v, want both turns", whys)
	}
	if id, ok, _ := s.Get(ghost.HermesSessionStoreKey); !ok || id != "haunt-wisp" {
		t.Errorf("hermes session in the store = %q, %v", id, ok)
	}
}

// hermesRig is what a test needs to run a ghost on the fake hermes
// (testdata/fakehermes): haunt and the fake built, and a config that points
// [hermes] at the fake with a hermes home of the test's own.
type hermesRig struct {
	Dir, Bin, CfgDir, RT, FakeLog, Script string
}

// fakeRule is one scripted answer for the fake (see rule in
// testdata/fakehermes): the first whose Match is in the input answers.
type fakeRule struct {
	Match   string         `json:"match"`
	DelayMS int            `json:"delay_ms,omitempty"`
	Tool    string         `json:"tool,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
	Reply   string         `json:"reply"`
}

// newHermesRig builds into dir. rules nil keeps the fake's defaults (events
// are NO_REPLY, "remind me" calls intend, anything else is echoed). extra
// is appended to the config file, so its first lines are still [hermes]
// keys until it opens a table of its own.
func newHermesRig(t *testing.T, dir string, rules []fakeRule, extra string) hermesRig {
	t.Helper()
	r := hermesRig{
		Dir:     dir,
		Bin:     filepath.Join(dir, "haunt"),
		CfgDir:  filepath.Join(dir, "config"),
		RT:      filepath.Join(dir, "run"),
		FakeLog: filepath.Join(dir, "fakehermes.log"),
	}
	if err := os.MkdirAll(r.CfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	build(t, r.Bin, ".")
	fake := filepath.Join(dir, "fakehermes")
	build(t, fake, "./testdata/fakehermes")
	if rules != nil {
		r.Script = filepath.Join(dir, "fakehermes.json")
		b, err := json.Marshal(rules)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(r.Script, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	config := fmt.Sprintf("[hermes]\ncommand = [%q]\nhome = %q\n%s", fake, filepath.Join(dir, "hermes"), extra)
	if err := os.WriteFile(filepath.Join(r.CfgDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return r
}

// env is what a ghost run, and the fake it starts, need.
func (r hermesRig) env() []string {
	env := []string{
		"HAUNT_RUNTIME_DIR=" + r.RT,
		"HAUNT_CONFIG_DIR=" + r.CfgDir,
		"FAKEHERMES_LOG=" + r.FakeLog,
	}
	if r.Script != "" {
		env = append(env, "FAKEHERMES_SCRIPT="+r.Script)
	}
	return env
}

// say addresses the ghost called wisp.
func (r hermesRig) say(t *testing.T, text string) {
	t.Helper()
	sc := exec.Command(r.Bin, "say", "-ghost", "wisp", text)
	sc.Env = append(os.Environ(), r.env()...)
	if out, err := sc.CombinedOutput(); err != nil {
		t.Fatalf("say %q: %v: %s", text, err, out)
	}
}

// fakeEntries is every line of the fake's log that matches.
func fakeEntries(path string, match func(fakeEntry) bool) []fakeEntry {
	data, _ := os.ReadFile(path)
	var out []fakeEntry
	for _, line := range strings.Split(string(data), "\n") {
		var e fakeEntry
		if json.Unmarshal([]byte(line), &e) == nil && match(e) {
			out = append(out, e)
		}
	}
	return out
}

// turnsAbout matches the turns (answered or not) whose input contains s.
func turnsAbout(s string) func(fakeEntry) bool {
	return func(e fakeEntry) bool { return e.Kind == "chat" && strings.Contains(e.Input, s) }
}

// repliedTo matches the replies to inputs containing s.
func repliedTo(s string) func(fakeEntry) bool {
	return func(e fakeEntry) bool { return e.Kind == "reply" && strings.Contains(e.Input, s) }
}

// fakeEntry is one line of testdata/fakehermes's log.
type fakeEntry struct {
	Kind          string            `json:"kind"`
	PID           int               `json:"pid"`
	Input         string            `json:"input"`
	Reply         string            `json:"reply"`
	SystemMessage string            `json:"system_message"`
	SessionKey    string            `json:"session_key"`
	Tools         []string          `json:"tools"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Env           map[string]string `json:"env"`
	MCPPID        int               `json:"mcp_pid"`
	Addr          string            `json:"addr"`
	Text          string            `json:"text"`
	IsError       bool              `json:"is_error"`
	Error         string            `json:"error"`
	Name          string            `json:"name"`
}

// waitFakeLog returns the first log entry matching, waiting up to d for it.
func waitFakeLog(t *testing.T, path string, d time.Duration, match func(fakeEntry) bool) fakeEntry {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		data, _ := os.ReadFile(path)
		for _, line := range strings.Split(string(data), "\n") {
			var e fakeEntry
			if json.Unmarshal([]byte(line), &e) == nil && match(e) {
				return e
			}
			if e.Kind == "fatal" {
				t.Fatalf("fakehermes failed: %s", e.Error)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("fakehermes never logged what was expected; its log:\n%s", data)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitHermesIntentions polls `haunt journal -intentions` for a line.
func waitHermesIntentions(t *testing.T, bin, state, rt, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		cmd := exec.Command(bin, "journal", "-intentions", "-state", state)
		cmd.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
		out, err := cmd.CombinedOutput()
		if err == nil && strings.Contains(string(out), want) {
			return string(out)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ghost never held %q:\n%s (%v)", want, out, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
