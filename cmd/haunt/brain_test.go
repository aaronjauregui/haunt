package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/control"
	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/store"
	"github.com/aaronjauregui/haunt/internal/testenv"
)

// The model brain end to end: a real ghost in headless sway, thinking with
// a scripted chat completions server (internal/model/fake) named in its
// config as [model]. The fake runs in the test; a logging proxy in front
// of it writes what each call asked and answered to a file the tests read
// (fakeEntry), so a test can wait for "the model was asked about X" or
// "the model answered X" the way a person would see it.

// speakCue opens the result of speak, which a rule matches to answer the
// speak call (ghost's decisions_speech.go).
const speakCue = "Now say it"

// says scripts a turn whose input contains match: decide to speak, then
// the words.
func says(match, words string) []fake.Rule {
	return []fake.Rule{
		{Match: match, AfterTool: speakCue, Reply: words},
		{Match: match, Calls: []fake.Call{{Name: "speak"}}},
	}
}

// silentOn scripts a turn whose input contains match as stay_silent.
func silentOn(match string) fake.Rule {
	return fake.Rule{Match: match, Calls: []fake.Call{{Name: "stay_silent"}}}
}

// acts scripts a turn whose input contains match: the calls, then speak,
// then the words.
func acts(match string, calls []fake.Call, words string) []fake.Rule {
	return []fake.Rule{
		{Match: match, AfterTool: speakCue, Reply: words},
		{Match: match, AfterTool: "*", Calls: []fake.Call{{Name: "speak"}}},
		{Match: match, Calls: calls},
	}
}

// decision is one scripted call.
func decision(name string, args map[string]any) fake.Call {
	b, _ := json.Marshal(args)
	return fake.Call{Name: name, Args: b}
}

// rules joins rule lists in order: the first rule that matches answers.
func rules(lists ...[]fake.Rule) []fake.Rule {
	var out []fake.Rule
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// defaultRules: events are silent, a reminder is filed with intend, and
// anything else is answered.
func defaultRules() []fake.Rule {
	return rules(
		[]fake.Rule{silentOn("[haunt event]")},
		acts("remind me", []fake.Call{decision("intend",
			map[string]any{"text": "check the oven", "delay_ms": 600000})}, "Done, I will."),
		says("", "I hear you."),
	)
}

// brainRig is what a test needs to run a ghost on the fake model: haunt
// built, the fake and its log, and a config that names it as [model].
type brainRig struct {
	Dir, Bin, CfgDir, RT, FakeLog string
	Fake                          *fake.Server
}

// newBrainRig builds into dir. rules nil is defaultRules. extra is
// appended to the config file, after the [model] table.
func newBrainRig(t *testing.T, dir string, rs []fake.Rule, extra string) brainRig {
	t.Helper()
	if rs == nil {
		rs = defaultRules()
	}
	r := brainRig{
		Dir:     dir,
		Bin:     filepath.Join(dir, "haunt"),
		CfgDir:  filepath.Join(dir, "config"),
		RT:      filepath.Join(dir, "run"),
		FakeLog: filepath.Join(dir, "fakemodel.log"),
		Fake:    fake.NewT(t, rs...),
	}
	if err := os.MkdirAll(r.CfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	build(t, r.Bin, ".")
	proxy := httptest.NewServer(&logProxy{to: r.Fake.URL, log: r.FakeLog})
	t.Cleanup(proxy.Close)
	config := fmt.Sprintf("[model]\nbase_url = %q\nname = \"fake\"\n%s", proxy.URL+"/v1", extra)
	if err := os.WriteFile(filepath.Join(r.CfgDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return r
}

// env is what a ghost run needs.
func (r brainRig) env() []string {
	return []string{"HAUNT_RUNTIME_DIR=" + r.RT, "HAUNT_CONFIG_DIR=" + r.CfgDir}
}

// say addresses the ghost called wisp.
func (r brainRig) say(t *testing.T, text string) {
	t.Helper()
	sc := exec.Command(r.Bin, "say", "-ghost", "wisp", text)
	sc.Env = append(os.Environ(), r.env()...)
	if out, err := sc.CombinedOutput(); err != nil {
		t.Fatalf("say %q: %v: %s", text, err, out)
	}
}

// start runs the ghost in gDir with this rig, waiting for READY. Its
// stderr is collected and shown if the test fails.
func (r brainRig) start(t *testing.T, c *testenv.Compositor, gDir, stateDir string, args ...string) (*exec.Cmd, *syncBuffer) {
	t.Helper()
	all := append([]string{"run", "-x", "100", "-y", "100", "-state", stateDir}, args...)
	cmd := exec.Command(r.Bin, append(all, gDir)...)
	cmd.Env = append(c.Env(), r.env()...)
	cmd.Env = append(cmd.Env, "HAUNT_DEBUG=brain,balloon")
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
	go io.Copy(io.Discard, stdout)
	return cmd, stderr
}

// fakeEntry is one line of the fake's log.
type fakeEntry struct {
	// Kind is "chat" for the first call of a turn, "tool_call" for a
	// decision's result as the next call carries it, "reply" for a call
	// that answered with words or silence, and "error" for one the fake
	// refused.
	Kind  string `json:"kind"`
	Input string `json:"input"`
	// Context is a chat's last system message: the turn's context.
	Context string `json:"context,omitempty"`
	// Tools are the decisions a chat offered.
	Tools []string `json:"tools,omitempty"`
	// Name and Text are a tool_call's decision and result.
	Name    string `json:"name,omitempty"`
	Text    string `json:"text,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
	// Reply is a reply's words; Silent a reply that was stay_silent.
	Reply  string `json:"reply,omitempty"`
	Silent bool   `json:"silent,omitempty"`
	Status int    `json:"status,omitempty"`
}

// logProxy passes chat completions calls to the fake and logs them.
type logProxy struct {
	to, log string
	mu      sync.Mutex
}

type proxyRequest struct {
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Name    string          `json:"name"`
	} `json:"messages"`
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

func (p *logProxy) write(e fakeEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f, err := os.OpenFile(p.log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	f.Write(append(b, '\n'))
}

func (p *logProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req proxyRequest
	json.Unmarshal(body, &req)
	msgs := req.Messages
	if n := len(msgs); n > 0 && msgs[n-1].Role == "assistant" {
		msgs = msgs[:n-1]
	}
	input := ""
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			input = contentText(msgs[i].Content)
			break
		}
	}
	n := len(msgs)
	switch {
	case n > 0 && msgs[n-1].Role == "system" && len(req.Tools) > 0:
		e := fakeEntry{Kind: "chat", Input: input, Context: contentText(msgs[n-1].Content)}
		for _, t := range req.Tools {
			e.Tools = append(e.Tools, t.Function.Name)
		}
		p.write(e)
	case n > 0 && msgs[n-1].Role == "tool":
		i := n - 1
		for i >= 0 && msgs[i].Role == "tool" {
			i--
		}
		for _, m := range msgs[i+1:] {
			if m.Name == "speak" || m.Name == "stay_silent" {
				continue
			}
			text := contentText(m.Content)
			p.write(fakeEntry{Kind: "tool_call", Input: input, Name: m.Name, Text: text,
				IsError: strings.HasPrefix(text, "That was not allowed") || strings.HasPrefix(text, "That did not work")})
		}
	}

	out, err := http.NewRequestWithContext(r.Context(), r.Method, p.to+r.URL.Path, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	out.Header = r.Header.Clone()
	resp, err := http.DefaultClient.Do(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		io.Copy(w, resp.Body)
		p.write(fakeEntry{Kind: "error", Input: input, Status: resp.StatusCode})
		return
	}
	fl, _ := w.(http.Flusher)
	var words strings.Builder
	var calls []string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		fmt.Fprintln(w, line)
		if fl != nil {
			fl.Flush()
		}
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		for _, c := range chunk.Choices {
			words.WriteString(c.Delta.Content)
			for _, tc := range c.Delta.ToolCalls {
				if tc.Function.Name != "" {
					calls = append(calls, tc.Function.Name)
				}
			}
		}
	}
	switch {
	case slices.Contains(calls, "stay_silent"):
		p.write(fakeEntry{Kind: "reply", Input: input, Silent: true})
	case len(calls) == 0 && len(req.Tools) == 0 && input != "" && strings.Contains(string(body), speakCue):
		// The speak call: its words, after the prefill.
		prefill := ""
		if m := req.Messages; len(m) > 0 && m[len(m)-1].Role == "assistant" {
			prefill = contentText(m[len(m)-1].Content)
		}
		p.write(fakeEntry{Kind: "reply", Input: input, Reply: prefill + words.String()})
	case len(calls) == 0 && len(req.Tools) > 0:
		p.write(fakeEntry{Kind: "reply", Input: input, Reply: words.String(), Silent: strings.TrimSpace(words.String()) == ""})
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

// repliedTo matches the answers to inputs containing s, words or silence.
func repliedTo(s string) func(fakeEntry) bool {
	return func(e fakeEntry) bool { return e.Kind == "reply" && strings.Contains(e.Input, s) }
}

// decided matches the results of a decision.
func decided(name string) func(fakeEntry) bool {
	return func(e fakeEntry) bool { return e.Kind == "tool_call" && e.Name == name }
}

// waitFakeLog returns the first log entry matching, waiting up to d for it.
func waitFakeLog(t *testing.T, path string, d time.Duration, match func(fakeEntry) bool) fakeEntry {
	t.Helper()
	return waitFake(t, path, 1, d, match)[0]
}

// waitHistory is `haunt history` with args for the ghost in stateDir.
func (r brainRig) history(t *testing.T, stateDir string, args ...string) string {
	t.Helper()
	hc := exec.Command(r.Bin, append(append([]string{"history"}, args...), "-state", stateDir, "wisp")...)
	hc.Env = append(os.Environ(), r.env()...)
	out, err := hc.CombinedOutput()
	if err != nil {
		t.Fatalf("haunt history %v: %v: %s", args, err, out)
	}
	return string(out)
}

// historyJSON is `haunt history -json` for the ghost in stateDir, every
// turn the transcript keeps, each record decoded strictly so a field the
// command grows or renames shows up here.
func historyJSON(t *testing.T, r brainRig, stateDir string) []traceRecord {
	t.Helper()
	out := r.history(t, stateDir, "-json", "-n", "0")
	var turns []traceRecord
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	for dec.More() {
		var tr traceRecord
		if err := dec.Decode(&tr); err != nil {
			t.Fatalf("history -json: %v:\n%s", err, out)
		}
		turns = append(turns, tr)
	}
	return turns
}

// wisp is a persona ghost that thinks with the model.
const wisp = `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[persona]
system = "You are Wisp, a wry desktop spirit who keeps an eye on the kitchen."
`

// TestBrainBootGreeting: a ghost with no [brain] wakes up on screen, is
// asked about its boot with its persona in front of the model, and greets
// the person; the greeting is in the balloon and in the transcript, and
// `haunt history` reads it back with the ghost still running.
func TestBrainBootGreeting(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rig := newBrainRig(t, dir, rules(says("kind=boot", "Oh. You're back.")), "")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, wisp)
	state := filepath.Join(dir, "state")
	rig.start(t, c, gDir, state)

	chat := waitFakeLog(t, rig.FakeLog, 30*time.Second, turnsAbout("kind=boot"))
	if !strings.Contains(chat.Input, "first_time=true") || !strings.HasPrefix(chat.Context, "Now: ") {
		t.Errorf("boot chat = %+v", chat)
	}
	if !slices.Contains(chat.Tools, "speak") || !slices.Contains(chat.Tools, "stay_silent") || !slices.Contains(chat.Tools, "intend") {
		t.Errorf("the boot turn offered %v", chat.Tools)
	}
	reply := waitFakeLog(t, rig.FakeLog, 10*time.Second, repliedTo("kind=boot"))
	if reply.Reply != "Oh. You're back." {
		t.Errorf("greeting = %+v", reply)
	}
	waitBalloon(t, c, dir, 100, 100, true)
	first := rig.Fake.Bodies()[0]
	if !bytes.Contains(first, []byte("wry desktop spirit")) || !bytes.Contains(first, []byte("stay_silent")) {
		t.Errorf("the persona and the rules are not in the prompt:\n%s", first)
	}

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(rig.history(t, state), "ghost: Oh. You're back.") {
		if time.Now().After(deadline) {
			t.Fatalf("history lacks the greeting:\n%s", rig.history(t, state))
		}
		wait100ms()
	}
	if out := rig.history(t, state); !strings.Contains(out, "event: kind=boot first_time=true") {
		t.Errorf("history:\n%s", out)
	}
}

// TestBrainPersonTurnWithATool: the person asks for a reminder; the model
// decides intend, the driver files it in the store as asked for by the
// person, the model speaks with the result in front of it, and history
// and -json read the turn back with the call, its arguments and result.
func TestBrainPersonTurnWithATool(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rig := newBrainRig(t, dir, nil, "")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, wisp)
	state := filepath.Join(dir, "state")
	cmd, _ := rig.start(t, c, gDir, state)
	waitFakeLog(t, rig.FakeLog, 30*time.Second, repliedTo("kind=boot"))

	rig.say(t, "remind me in ten minutes to check the oven")
	call := waitFakeLog(t, rig.FakeLog, 30*time.Second, decided("intend"))
	if call.IsError || !strings.HasPrefix(call.Text, "model/") {
		t.Fatalf("intend answered %+v", call)
	}
	waitFakeLog(t, rig.FakeLog, 10*time.Second, repliedTo("remind me"))
	waitIntentions(t, rig.Bin, state, rig.RT, "check the oven")
	if out := readIntentions(t, rig.Bin, state, rig.RT); !strings.Contains(out, "(due ") {
		t.Errorf("the reminder was filed without a due time:\n%s", out)
	}
	waitBalloon(t, c, dir, 100, 100, true)

	out := rig.history(t, state, "-why")
	for _, want := range []string{"event: kind=boot", "ghost: (said nothing)", "woke, and chose not to speak",
		"you:   remind me in ten minutes", "ghost: Done, I will.", "did something first: intend"} {
		if !strings.Contains(out, want) {
			t.Errorf("history -why lacks %q:\n%s", want, out)
		}
	}
	turns := historyJSON(t, rig, state)
	ri := slices.IndexFunc(turns, func(t traceRecord) bool { return strings.HasPrefix(t.Input, "remind me") })
	if ri < 0 {
		t.Fatalf("history -json lacks the reminder: %+v", turns)
	}
	r := turns[ri]
	if r.ProvokedBy != "person" || r.Silent || r.SelfPrompt || r.Reply != "Done, I will." || r.Time.IsZero() {
		t.Errorf("reminder turn %+v", r)
	}
	var args map[string]any
	if len(r.Acted) != 1 || r.Acted[0].Tool != "intend" || json.Unmarshal([]byte(r.Acted[0].Args), &args) != nil ||
		args["text"] != "check the oven" || r.Acted[0].Result != call.Text {
		t.Errorf("reminder turn acted %+v, want intend with its args and result", r.Acted)
	}

	stopGhost(t, cmd)
	s, err := store.Open(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, _, _ := s.Get(ghost.IntentionsStoreKey)
	list, err := ghost.DecodeIntentions(raw)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range list {
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
		t.Errorf("intentions = %+v, want %q", list, call.Text)
	}
}

// TestBrainSilentEventTurnRefunds: an idle wake the model answers with
// stay_silent shows nothing, is recorded as silence, and costs none of
// the day's curiosity, so the next idle is woken about too.
func TestBrainSilentEventTurnRefunds(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rig := newBrainRig(t, dir, rules(
		[]fake.Rule{silentOn("kind=idle")},
		[]fake.Rule{silentOn("[haunt event]")},
	), "[curiosity]\nactions_per_day = 1\n")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, wisp)
	state := filepath.Join(dir, "state")
	record := filepath.Join(dir, "session.jsonl")
	_, stderr := rig.start(t, c, gDir, state, "-idle-ms", "2500", "-record", record)

	waitFakeLog(t, rig.FakeLog, 30*time.Second, repliedTo("kind=idle"))
	time.Sleep(time.Second)
	if balloonVisible(t, c, dir, 100, 100) {
		t.Error("a balloon is up after a silent wake")
	}
	// One wake a day, and the silence gave it back: the next idle is woken
	// about too.
	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	ptr.MoveTo(600, 400)
	ptr.Close()
	waitFake(t, rig.FakeLog, 2, 30*time.Second, repliedTo("kind=idle"))
	if strings.Contains(stderr.String(), "daily curiosity spent") {
		t.Error("a silent wake spent the day's curiosity")
	}
	turns := historyJSON(t, rig, state)
	idle := slices.IndexFunc(turns, func(t traceRecord) bool { return strings.HasPrefix(t.Input, "kind=idle") })
	if idle < 0 || !turns[idle].Silent || turns[idle].ProvokedBy != "event" || turns[idle].Reply != "" {
		t.Errorf("idle turn %+v", turns)
	}
}

// TestBrainDeferredEvent: another ghost speaking to this one while the
// person's turn is still being answered waits for that turn, and is sent
// after it rather than dropped.
func TestBrainDeferredEvent(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rig := newBrainRig(t, dir, rules(
		[]fake.Rule{{Match: "slow thought", AfterTool: speakCue, Reply: "There.", DelayMS: 3000}},
		[]fake.Rule{{Match: "slow thought", Calls: []fake.Call{{Name: "speak"}}}},
		says("kind=told", "Moth says boo."),
		[]fake.Rule{silentOn("[haunt event]")},
	), "")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, wisp)
	state := filepath.Join(dir, "state")
	_, stderr := rig.start(t, c, gDir, state)
	waitFakeLog(t, rig.FakeLog, 30*time.Second, repliedTo("kind=boot"))

	rig.say(t, "a slow thought")
	waitFakeLog(t, rig.FakeLog, 10*time.Second, turnsAbout("slow thought"))
	t.Setenv("HAUNT_RUNTIME_DIR", rig.RT)
	if err := control.Send("wisp", proto.Event{Type: proto.EventMessage, From: "moth", Text: "boo"}); err != nil {
		t.Fatal(err)
	}
	waitLog(t, stderr, "waits for the turn in flight")
	told := waitFakeLog(t, rig.FakeLog, 30*time.Second, turnsAbout("kind=told"))
	if told.Input != "[haunt event] kind=told from=moth text=boo" {
		t.Errorf("told envelope %q", told.Input)
	}
	var order []string
	for _, e := range fakeEntries(rig.FakeLog, func(e fakeEntry) bool { return e.Kind == "reply" || e.Kind == "chat" }) {
		order = append(order, e.Kind+" "+e.Input)
	}
	reply := slices.IndexFunc(order, func(s string) bool { return strings.HasPrefix(s, "reply a slow thought") })
	chat := slices.IndexFunc(order, func(s string) bool { return strings.HasPrefix(s, "chat "+told.Input) })
	if reply < 0 || chat < reply {
		t.Errorf("the event did not wait for the turn in flight:\n%s", strings.Join(order, "\n"))
	}
	waitFakeLog(t, rig.FakeLog, 10*time.Second, repliedTo("kind=told"))
}

// TestBrainFailedTurn: a model that refuses the call fails the turn; the
// person is told why in the balloon, history says the turn failed, and
// the next turn goes through.
func TestBrainFailedTurn(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rig := newBrainRig(t, dir, rules(
		[]fake.Rule{{Match: "break", Status: 400, Reply: "the context is full"}},
		[]fake.Rule{silentOn("[haunt event]")},
		says("", "Better now."),
	), "")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, wisp)
	state := filepath.Join(dir, "state")
	_, stderr := rig.start(t, c, gDir, state)
	waitFakeLog(t, rig.FakeLog, 30*time.Second, repliedTo("kind=boot"))

	rig.say(t, "break, please")
	waitFakeLog(t, rig.FakeLog, 10*time.Second, func(e fakeEntry) bool { return e.Kind == "error" })
	waitLog(t, stderr, "the context is full")
	waitBalloon(t, c, dir, 100, 100, true)
	turns := historyJSON(t, rig, state)
	bi := slices.IndexFunc(turns, func(t traceRecord) bool { return strings.HasPrefix(t.Input, "break") })
	if bi < 0 || !turns[bi].Failed || turns[bi].Silent || turns[bi].Reply != "" {
		t.Errorf("failed turn %+v", turns)
	}
	if out := rig.history(t, state); !strings.Contains(out, "ghost: (the turn failed)") {
		t.Errorf("history:\n%s", out)
	}

	rig.say(t, "and again")
	waitFakeLog(t, rig.FakeLog, 10*time.Second, repliedTo("and again"))
	// The failed turn is not replayed to the model: its question would
	// read as still open.
	last := rig.Fake.Bodies()[len(rig.Fake.Bodies())-1]
	if bytes.Contains(last, []byte("break, please")) {
		t.Error("the failed turn's input went back to the model")
	}
}

// TestBrainReminderDischargedOnceShown: a reminder comes due, the model
// raises it, and the driver discharges it once the words reached the
// screen — not when it came due.
func TestBrainReminderDischargedOnceShown(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rig := newBrainRig(t, dir, rules(
		acts("remind me", []fake.Call{decision("intend",
			map[string]any{"text": "stretch", "delay_ms": 3000})}, "I'll tell you."),
		says("kind=intention_due", "Time to stretch."),
		[]fake.Rule{silentOn("[haunt event]")},
	), "")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, wisp)
	state := filepath.Join(dir, "state")
	rig.start(t, c, gDir, state)
	waitFakeLog(t, rig.FakeLog, 30*time.Second, repliedTo("kind=boot"))

	rig.say(t, "remind me to stretch in three seconds")
	waitFakeLog(t, rig.FakeLog, 10*time.Second, repliedTo("remind me"))
	waitIntentions(t, rig.Bin, state, rig.RT, "stretch")
	due := waitFakeLog(t, rig.FakeLog, 30*time.Second, turnsAbout("kind=intention_due"))
	if !strings.Contains(due.Input, `text=stretch`) || !strings.Contains(due.Input, "asked=true") {
		t.Errorf("due envelope %q", due.Input)
	}
	waitFakeLog(t, rig.FakeLog, 10*time.Second, repliedTo("kind=intention_due"))
	deadline := time.Now().Add(10 * time.Second)
	for {
		out := readIntentions(t, rig.Bin, state, rig.RT)
		if !strings.Contains(out, "stretch") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the errand is still held after it was raised:\n%s", out)
		}
		wait100ms()
	}
}

// TestBrainQuestionAnsweredByClick: the model asks with choices; the
// person clicks one; the choice is the decision's result, and the model
// answers with it in front of it.
func TestBrainQuestionAnsweredByClick(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rig := newBrainRig(t, dir, rules(
		acts("tea or coffee", []fake.Call{decision("ask",
			map[string]any{"text": "Which?", "choices": []string{"Tea", "Coffee"}})}, "Good choice."),
		[]fake.Rule{silentOn("[haunt event]")},
	), "")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, wisp)
	state := filepath.Join(dir, "state")
	_, stderr := rig.start(t, c, gDir, state)
	waitFakeLog(t, rig.FakeLog, 30*time.Second, repliedTo("kind=boot"))

	rig.say(t, "tea or coffee?")
	waitLog(t, stderr, "choices offered")
	waitBalloon(t, c, dir, 100, 100, true)
	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	defer ptr.Close()
	picked := false
	for dy := 40; dy < 140 && !picked; dy += 10 {
		ptr.MoveTo(300, 100+dy)
		ptr.Press()
		ptr.Release()
		wait100ms()
		picked = len(fakeEntries(rig.FakeLog, decided("ask"))) > 0
	}
	if !picked {
		t.Fatal("clicking the question never answered it")
	}
	ask := waitFakeLog(t, rig.FakeLog, time.Second, decided("ask"))
	if ask.Text != "Tea" && ask.Text != "Coffee" {
		t.Errorf("ask answered %q, want a choice", ask.Text)
	}
	waitFakeLog(t, rig.FakeLog, 10*time.Second, repliedTo("tea or coffee"))
}

// A manifest naming a section ghost.toml does not have is refused when the
// ghost starts, by name.
func TestBrainRefusesAForeignSection(t *testing.T) {
	dir := t.TempDir()
	rig := newBrainRig(t, dir, nil, "")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, wisp+"\n[agent]\nskills = [\"x\"]\n")
	cmd := exec.Command(rig.Bin, "run", "-state", filepath.Join(dir, "state"), gDir)
	cmd.Env = append(os.Environ(), rig.env()...)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "[agent] is not a section of ghost.toml") {
		t.Errorf("haunt run = %v:\n%s", err, out)
	}
}
