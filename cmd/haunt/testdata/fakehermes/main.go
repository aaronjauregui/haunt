// fakehermes stands in for `hermes gateway run` in the end-to-end test of
// the hermes brain (cmd/haunt/hermes_test.go). It does the parts of hermes
// haunt depends on and nothing else: it reads the profile haunt wrote,
// spawns the haunt MCP server from it and talks MCP to it over stdio, and
// answers the API server's session and chat endpoints in hermes's own SSE
// shape (internal/hermes/testdata/remind.sse). The "model" is a script
// keyed on the input: $FAKEHERMES_SCRIPT names a JSON file of rules, and
// without one the default rules below apply.
//
// Everything it sees is appended to $FAKEHERMES_LOG as JSON lines, so the
// test can assert on what reached the brain.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(); err != nil {
		logEntry(map[string]any{"kind": "fatal", "error": err.Error()})
		fmt.Fprintln(os.Stderr, "fakehermes:", err)
		os.Exit(1)
	}
}

// --- logging ---------------------------------------------------------------

var logMu sync.Mutex

func logEntry(e map[string]any) {
	path := os.Getenv("FAKEHERMES_LOG")
	if path == "" {
		return
	}
	e["pid"] = os.Getpid()
	b, _ := json.Marshal(e)
	logMu.Lock()
	defer logMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

// --- the model ---------------------------------------------------------------

// rule is one scripted answer. The first rule whose Match is contained in
// the input (case-insensitively; "" matches anything) answers the turn.
type rule struct {
	Match string `json:"match"`
	// DelayMS is silence before anything is said or done, for a model that
	// takes its time.
	DelayMS int `json:"delay_ms"`
	// Tool, if set, is a haunt tool called before replying, with Args.
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
	// Then are more haunt tools called after Tool, in order, as a model
	// makes several calls in one turn: each is made whatever the one
	// before it answered, as a model reads a refusal and carries on.
	Then []step `json:"then"`
	// Look, if set, is one of hermes's own tools (skill_view, memory, …)
	// called first, with LookArgs. Nothing runs: the call and LookResult
	// are only stored in the session, as hermes stores its own calls.
	Look       string         `json:"look"`
	LookArgs   map[string]any `json:"look_args"`
	LookResult string         `json:"look_result"`
	// Interim, if set, is what the model writes alongside its first call:
	// streamed, then sent as assistant.commentary before the call starts,
	// and kept on the call's message, as hermes does.
	Interim string `json:"interim"`
	// StallMS is silence after the tools and before the reply, as while
	// hermes waits out a provider outage: nothing on the stream but
	// keepalive comments, unless Reasoning.
	StallMS int `json:"stall_ms"`
	// Reasoning fills the stall with the model's reasoning instead, a
	// tool.progress for `_thinking` every 100 ms.
	Reasoning bool `json:"reasoning"`
	// Reply is what the model says. {tool} is replaced by the last
	// tool's result and {input} by the input.
	Reply string `json:"reply"`
}

// step is one more haunt tool call in a rule's turn.
type step struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
	// DelayMS is how long the call takes before the tool is reached, as
	// one that waits on an image backend does: tool.started goes out
	// first, and nothing else until the call is done.
	DelayMS int `json:"delay_ms"`
}

// pause waits d, sending a keepalive comment every 100 ms, or a reasoning
// event when reasoning; false if the client went away meanwhile.
func (e *sse) pause(ctx context.Context, d time.Duration, reasoning bool, msgID string) bool {
	end := time.After(d)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-end:
			return true
		case <-ctx.Done():
			return false
		case <-tick.C:
			if reasoning {
				e.send("tool.progress", map[string]any{"message_id": msgID, "tool_name": "_thinking", "delta": "hmm"})
			} else {
				fmt.Fprint(e.w, ": keepalive\n\n")
				e.fl.Flush()
			}
		}
	}
}

var defaultRules = []rule{
	{Match: "[haunt event]", Reply: "NO_REPLY"},
	{Match: "remind me", Look: "session_search", LookArgs: map[string]any{"query": "oven"},
		LookResult: `{"results": []}`, Interim: "Let me look.", Tool: "intend",
		Args:  map[string]any{"text": "check the oven", "delay_ms": 600000},
		Reply: "Done: {tool}"},
	{Match: "", Reply: "echo: {input}"},
}

func loadRules() ([]rule, error) {
	path := os.Getenv("FAKEHERMES_SCRIPT")
	if path == "" {
		return defaultRules, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rules []rule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return rules, nil
}

func pick(rules []rule, input string) rule {
	lower := strings.ToLower(input)
	for _, r := range rules {
		if strings.Contains(lower, strings.ToLower(r.Match)) {
			return r
		}
	}
	return rule{Reply: "NO_REPLY"}
}

// --- profile ---------------------------------------------------------------

type serverEntry struct {
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
}

type profileConfig struct {
	MCPServers map[string]serverEntry `yaml:"mcp_servers"`
	Skills     struct {
		ExternalDirs []string `yaml:"external_dirs"`
	} `yaml:"skills"`
}

// findSkills does what hermes's index does with skills.external_dirs: each
// entry is resolved against HERMES_HOME, and every SKILL.md under it is a
// skill, named category/skill as the index groups them. A directory that
// does not exist is skipped, and reported.
func findSkills(home string, dirs []string) (skills, missing []string) {
	for _, d := range dirs {
		root := d
		if !filepath.IsAbs(root) {
			root = filepath.Join(home, d)
		}
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			missing = append(missing, d)
			continue
		}
		filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
			if err == nil && e.Name() == "SKILL.md" {
				rel, _ := filepath.Rel(root, filepath.Dir(p))
				skills = append(skills, rel)
			}
			return nil
		})
	}
	return skills, missing
}

func readDotenv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	env := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		env[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return env, sc.Err()
}

func run() error {
	home := os.Getenv("HERMES_HOME")
	if home == "" {
		return errors.New("HERMES_HOME is not set")
	}
	logEntry(map[string]any{"kind": "start", "args": os.Args[1:], "home": home})

	env, err := readDotenv(filepath.Join(home, ".env"))
	if err != nil {
		return fmt.Errorf("reading .env: %w", err)
	}
	key, port, host := env["API_SERVER_KEY"], env["API_SERVER_PORT"], env["API_SERVER_HOST"]
	if key == "" || port == "" {
		return errors.New(".env has no API_SERVER_KEY or API_SERVER_PORT")
	}
	if host == "" {
		host = "127.0.0.1"
	}

	raw, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil {
		return fmt.Errorf("reading config.yaml: %w", err)
	}
	var cfg profileConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parsing config.yaml: %w", err)
	}
	entry, ok := cfg.MCPServers["haunt"]
	if !ok || entry.Command == "" {
		return errors.New("config.yaml has no mcp_servers.haunt")
	}

	skills, missing := findSkills(home, cfg.Skills.ExternalDirs)
	logEntry(map[string]any{"kind": "skills", "dirs": cfg.Skills.ExternalDirs, "skills": skills, "missing": missing})

	rules, err := loadRules()
	if err != nil {
		return fmt.Errorf("reading the script: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	mcp, err := startMCP(entry)
	if err != nil {
		return fmt.Errorf("starting the haunt MCP server: %w", err)
	}
	defer mcp.close()

	// Hermes connects its MCP servers alongside serving; turns wait for it.
	ready := make(chan struct{})
	var tools []string
	var mcpErr error
	go func() {
		defer close(ready)
		tools, mcpErr = mcp.handshake(ctx)
		if mcpErr != nil {
			logEntry(map[string]any{"kind": "mcp_error", "error": mcpErr.Error()})
			return
		}
		logEntry(map[string]any{"kind": "tools_list", "tools": tools})
	}()

	s := &server{key: key, mcp: mcp, ready: ready, rules: rules, mcpErr: func() error { return mcpErr }, sessions: map[string]bool{}, runs: map[string]*fakeRun{}}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.mux()}
	go srv.Serve(ln)
	logEntry(map[string]any{"kind": "listening", "addr": ln.Addr().String()})

	<-ctx.Done()
	logEntry(map[string]any{"kind": "stopping"})
	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	srv.Shutdown(sctx)
	return nil
}

// --- MCP client --------------------------------------------------------------

type mcpClient struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan struct{}

	wmu     sync.Mutex
	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcResponse
}

type rpcResponse struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// filteredEnv is what hermes hands an MCP server: a few basics and the
// XDG directories, then the entry's own env.
func filteredEnv(extra map[string]string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PATH" || k == "HOME" || k == "USER" || k == "LANG" || strings.HasPrefix(k, "XDG_") {
			out = append(out, kv)
		}
	}
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

func startMCP(e serverEntry) (*mcpClient, error) {
	cmd := exec.Command(e.Command, e.Args...)
	cmd.Env = filteredEnv(e.Env)
	cmd.Stderr = os.Stderr
	// Killed with us even if we are killed hard.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	logEntry(map[string]any{"kind": "mcp_spawned", "command": e.Command, "args": e.Args, "env": e.Env, "mcp_pid": cmd.Process.Pid})
	c := &mcpClient{cmd: cmd, stdin: stdin, done: make(chan struct{}), pending: map[int]chan rpcResponse{}}
	go c.read(stdout)
	return c, nil
}

func (c *mcpClient) read(r io.Reader) {
	defer close(c.done)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		var resp rpcResponse
		if json.Unmarshal(sc.Bytes(), &resp) != nil || resp.ID == nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[*resp.ID]
		delete(c.pending, *resp.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
}

func (c *mcpClient) send(msg map[string]any) error {
	msg["jsonrpc"] = "2.0"
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

func (c *mcpClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return nil, fmt.Errorf("%s: %d %s", method, resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	case <-c.done:
		return nil, fmt.Errorf("%s: the MCP server exited", method)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *mcpClient) handshake(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "fakehermes", "version": "0"},
	})
	if err != nil {
		return nil, err
	}
	logEntry(map[string]any{"kind": "initialized", "result": res})
	if err := c.send(map[string]any{"method": "notifications/initialized"}); err != nil {
		return nil, err
	}
	res, err = c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(res, &list); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list.Tools))
	for _, t := range list.Tools {
		names = append(names, t.Name)
	}
	return names, nil
}

type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func (c *mcpClient) callTool(ctx context.Context, name string, args any) (toolResult, error) {
	var out toolResult
	res, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(res, &out)
	return out, err
}

func (c *mcpClient) close() {
	c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
	}
	c.cmd.Process.Signal(syscall.SIGTERM)
	exited := make(chan struct{})
	go func() { c.cmd.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		c.cmd.Process.Kill()
		<-exited
	}
	logEntry(map[string]any{"kind": "mcp_stopped"})
}

// --- API server --------------------------------------------------------------

type server struct {
	key    string
	mcp    *mcpClient
	ready  chan struct{}
	mcpErr func() error
	rules  []rule

	mu       sync.Mutex
	sessions map[string]bool
	runN     int
	// transcript is every stored message, oldest first, as hermes keeps
	// them: the reply is stored even when it is NO_REPLY.
	transcript []map[string]any
	// runs is every turn's run by id, for /v1/runs.
	runs map[string]*fakeRun
}

func (s *server) mux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","platform":"hermes-agent"}`)
	})
	m.HandleFunc("POST /api/sessions", s.auth(s.createSession))
	m.HandleFunc("POST /api/sessions/{id}/chat/stream", s.auth(s.chat))
	m.HandleFunc("GET /api/sessions/{id}/messages", s.auth(s.messages))
	m.HandleFunc("POST /v1/runs/{run_id}/stop", s.auth(s.stopRun))
	m.HandleFunc("GET /v1/runs/{run_id}", s.auth(s.getRun))
	return m
}

func apiError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func (s *server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.key {
			logEntry(map[string]any{"kind": "unauthorized", "path": r.URL.Path})
			apiError(w, http.StatusUnauthorized, "unauthorized", "invalid API key")
			return
		}
		h(w, r)
	}
}

func (s *server) createSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Source string `json:"source"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	logEntry(map[string]any{"kind": "create_session", "id": body.ID, "title": body.Title, "source": body.Source,
		"session_key": r.Header.Get("X-Hermes-Session-Key")})
	if body.ID == "" {
		body.ID = fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	s.mu.Lock()
	exists := s.sessions[body.ID]
	s.sessions[body.ID] = true
	s.mu.Unlock()
	if exists {
		apiError(w, http.StatusConflict, "session_exists", "session "+body.ID+" already exists")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{"session": map[string]any{"id": body.ID, "title": body.Title}})
}

// sse writes events in hermes's framing: event line, one data line, blank
// line, with session_id, run_id, seq and ts on every payload.
type sse struct {
	w       http.ResponseWriter
	fl      http.Flusher
	session string
	run     string
	seq     int
}

func (e *sse) send(event string, payload map[string]any) {
	e.seq++
	payload["session_id"] = e.session
	payload["run_id"] = e.run
	payload["seq"] = e.seq
	payload["ts"] = float64(time.Now().UnixNano()) / 1e9
	b, _ := json.Marshal(payload)
	fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", event, b)
	e.fl.Flush()
}

func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Input         string `json:"input"`
		SystemMessage string `json:"system_message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	s.mu.Lock()
	known := s.sessions[id]
	s.runN++
	n := s.runN
	s.mu.Unlock()
	runID := fmt.Sprintf("run_fake%d", n)
	logEntry(map[string]any{"kind": "chat", "session": id, "input": body.Input, "system_message": body.SystemMessage,
		"session_key": r.Header.Get("X-Hermes-Session-Key"), "run_id": runID})
	if !known {
		apiError(w, http.StatusNotFound, "session_not_found", "no session "+id)
		return
	}

	<-s.ready
	// The run outlives the request, as hermes's does: it ends when it is
	// stopped, or once hermes notices the client gone (watch).
	run := &fakeRun{stop: make(chan struct{}), status: "running"}
	s.mu.Lock()
	s.runs[runID] = run
	s.mu.Unlock()
	runCtx, endRun := context.WithCancel(context.Background())
	defer endRun()
	finished := make(chan struct{})
	defer close(finished)
	go run.watch(r.Context(), finished, endRun)
	abandon := func() {
		logEntry(map[string]any{"kind": "abandoned", "session": id, "input": body.Input, "run_id": runID})
		s.setStatus(run, "cancelled")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	e := &sse{w: w, fl: w.(http.Flusher), session: id, run: runID}
	msgID := fmt.Sprintf("msg_fake%d", n)

	e.send("run.started", map[string]any{
		"user_message": map[string]any{"role": "user", "content": body.Input},
		"runtime":      map[string]any{"provider": "", "model": ""},
	})
	e.send("message.started", map[string]any{"message": map[string]any{"id": msgID, "role": "assistant"}})

	rl := pick(s.rules, body.Input)
	if rl.DelayMS > 0 {
		select {
		case <-time.After(time.Duration(rl.DelayMS) * time.Millisecond):
		case <-runCtx.Done():
			abandon()
			return
		}
	}
	reply := strings.ReplaceAll(rl.Reply, "{input}", body.Input)
	// The session keeps the turn as hermes does: the input, then the calls
	// the model made with their results, then the reply.
	now := float64(time.Now().UnixNano()) / 1e9
	stored := []map[string]any{{"role": "user", "content": body.Input, "timestamp": now}}
	var calls, results []map[string]any
	record := func(name string, args map[string]any, result string) {
		callID := fmt.Sprintf("call_fake%d_%d", n, len(calls))
		argText, _ := json.Marshal(args)
		calls = append(calls, map[string]any{"id": callID, "type": "function",
			"function": map[string]any{"name": name, "arguments": string(argText)}})
		results = append(results, map[string]any{"role": "tool", "tool_call_id": callID, "content": result, "timestamp": now})
	}
	said := false
	interim := func() {
		if rl.Interim == "" || said {
			return
		}
		said = true
		for i := 0; i < len(rl.Interim); i += 4 {
			e.send("assistant.delta", map[string]any{"message_id": msgID, "delta": rl.Interim[i:min(i+4, len(rl.Interim))]})
		}
		e.send("assistant.commentary", map[string]any{"message_id": msgID, "text": rl.Interim, "already_streamed": true})
	}
	if rl.Look != "" {
		interim()
		e.send("tool.started", map[string]any{"message_id": msgID, "tool_name": rl.Look, "preview": "", "args": rl.LookArgs})
		e.send("tool.completed", map[string]any{"message_id": msgID, "tool_name": rl.Look, "preview": nil, "args": nil})
		record(rl.Look, rl.LookArgs, rl.LookResult)
	}
	var steps []step
	if rl.Tool != "" {
		steps = append(steps, step{Tool: rl.Tool, Args: rl.Args})
	}
	steps = append(steps, rl.Then...)
	last := ""
	for _, st := range steps {
		tool := "mcp__haunt__" + st.Tool
		interim()
		e.send("tool.started", map[string]any{"message_id": msgID, "tool_name": tool, "preview": "", "args": st.Args})
		if st.DelayMS > 0 && !e.pause(runCtx, time.Duration(st.DelayMS)*time.Millisecond, false, msgID) {
			abandon()
			return
		}
		text, err := s.callTool(r.Context(), st.Tool, st.Args)
		if err != nil {
			e.send("tool.failed", map[string]any{"message_id": msgID, "tool_name": tool, "error": err.Error()})
			text = "the tool failed: " + err.Error()
		} else {
			e.send("tool.completed", map[string]any{"message_id": msgID, "tool_name": tool, "preview": nil, "args": nil})
		}
		last = text
		record(tool, st.Args, text)
	}
	if len(steps) > 0 {
		reply = strings.ReplaceAll(reply, "{tool}", last)
	}
	if rl.StallMS > 0 && !e.pause(runCtx, time.Duration(rl.StallMS)*time.Millisecond, rl.Reasoning, msgID) {
		abandon()
		return
	}
	if len(calls) > 0 {
		stored = append(stored, map[string]any{"role": "assistant", "content": rl.Interim, "tool_calls": calls, "timestamp": now})
		stored = append(stored, results...)
	}

	for i := 0; i < len(reply); i += 4 {
		e.send("assistant.delta", map[string]any{"message_id": msgID, "delta": reply[i:min(i+4, len(reply))]})
	}
	logEntry(map[string]any{"kind": "reply", "session": id, "input": body.Input, "reply": reply})
	stored = append(stored, map[string]any{"role": "assistant", "content": reply, "timestamp": now})
	s.mu.Lock()
	s.transcript = append(s.transcript, stored...)
	s.mu.Unlock()
	e.send("assistant.completed", map[string]any{
		"message_id": msgID, "content": reply, "completed": true, "partial": false, "interrupted": false,
	})
	e.send("run.completed", map[string]any{
		"message_id": msgID, "completed": true, "partial": false, "interrupted": false,
	})
	e.send("done", map[string]any{})
	s.setStatus(run, "completed")
}

// fakeRun is one turn's run, for the /v1/runs endpoints.
type fakeRun struct {
	stop   chan struct{}
	once   sync.Once
	status string // guarded by server.mu
}

// How hermes ends a run whose client has gone. It notices the stream
// closed only at its next write, a keepalive within 10 s
// (_handle_session_chat_stream), shortened here; a stop interrupts the
// agent at once, and the run is over once the agent's thread has wound
// down, a moment later.
const (
	noticeLag  = 3 * time.Second
	stopSettle = 300 * time.Millisecond
)

// watch ends the run, by calling end, once it is stopped or hermes would
// have noticed the client gone, unless it finishes first.
func (run *fakeRun) watch(client context.Context, finished <-chan struct{}, end func()) {
	select {
	case <-finished:
		return
	case <-run.stop:
		time.Sleep(stopSettle)
	case <-client.Done():
		select {
		case <-finished:
			return
		case <-run.stop:
			time.Sleep(stopSettle)
		case <-time.After(noticeLag):
		}
	}
	end()
}

func (s *server) setStatus(run *fakeRun, status string) {
	s.mu.Lock()
	run.status = status
	s.mu.Unlock()
}

func runStatus(w http.ResponseWriter, id, status string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"object": "hermes.run", "run_id": id, "status": status})
}

// stopRun answers POST /v1/runs/{run_id}/stop as hermes does: it
// interrupts the run and answers "stopping" without waiting for it to be
// over.
func (s *server) stopRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run_id")
	s.mu.Lock()
	run := s.runs[id]
	status := ""
	if run != nil {
		status = run.status
		if status == "running" {
			run.status = "stopping"
		}
	}
	s.mu.Unlock()
	logEntry(map[string]any{"kind": "stop", "run_id": id, "status": status})
	switch status {
	case "":
		apiError(w, http.StatusNotFound, "run_not_found", "Run not found: "+id)
	case "completed", "cancelled":
		runStatus(w, id, status)
	default:
		run.once.Do(func() { close(run.stop) })
		runStatus(w, id, "stopping")
	}
}

// getRun answers GET /v1/runs/{run_id} with the run's status.
func (s *server) getRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run_id")
	s.mu.Lock()
	run := s.runs[id]
	status := ""
	if run != nil {
		status = run.status
	}
	s.mu.Unlock()
	if run == nil {
		apiError(w, http.StatusNotFound, "run_not_found", "Run not found: "+id)
		return
	}
	runStatus(w, id, status)
}

// callTool calls one haunt tool through the MCP server and logs what came
// back.
func (s *server) callTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if err := s.mcpErr(); err != nil {
		logEntry(map[string]any{"kind": "tool_call", "name": name, "error": err.Error()})
		return "", err
	}
	if args == nil {
		args = map[string]any{}
	}
	res, err := s.mcp.callTool(ctx, name, args)
	text := ""
	if len(res.Content) > 0 {
		text = res.Content[0].Text
	}
	logEntry(map[string]any{"kind": "tool_call", "name": name, "text": text, "is_error": res.IsError, "error": errString(err)})
	return text, err
}

// messages answers `order=latest&limit=N` as hermes does: the newest N,
// oldest first.
func (s *server) messages(w http.ResponseWriter, r *http.Request) {
	var limit int
	fmt.Sscan(r.URL.Query().Get("limit"), &limit)
	s.mu.Lock()
	data := s.transcript
	if limit > 0 && len(data) > limit {
		data = data[len(data)-limit:]
	}
	b, _ := json.Marshal(map[string]any{"object": "list", "data": data})
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
