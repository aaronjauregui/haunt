// Package mcp is a small client for the Model Context Protocol over stdio:
// enough of it to start a tool server, ask what tools it has, and call one.
//
// It is deliberately not a general MCP implementation. Haunt uses tool
// servers for one thing — letting a ghost do something in the world — and
// the parts of the protocol that serve other purposes (resources, prompts,
// sampling, server-initiated requests) are not wired to anything here. A
// server that offers them is not refused; the offers are ignored, which is
// what a client that cannot use them is supposed to do.
//
// Only the stdio transport exists. That is the transport whose credentials
// are environment variables the driver puts there, which is the whole
// reason the driver runs these and brains do not: a brain in the WASM
// sandbox cannot spawn a process, and a brain that could would be holding
// the user's keys.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// protocolVersion is what the client asks for. The oldest widely deployed
// version rather than the newest: nothing here uses anything added since,
// and a server that only speaks the old one is a server that works.
const protocolVersion = "2024-11-05"

// Tool is one thing a server can do. Schema is the JSON Schema for the
// call's arguments, passed to the language model unchanged — validating it
// here would be re-implementing the server's own contract badly, and the
// server rejects a bad call anyway.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"inputSchema"`
}

// Result is what a call produced: the text blocks joined, and whether the
// server called it a failure. A tool failure is not a transport error —
// "the file does not exist" is an answer, and the model is the thing that
// should hear it — so both come back here rather than as an error.
type Result struct {
	Text    string
	IsError bool
}

// Server is one running tool server.
type Server struct {
	name string
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Scanner

	// tools is what the server said it had at initialize time. Read-only
	// afterwards: a server that changes its tool list mid-session is
	// within its rights and this client ignores the notification, because
	// a tool that appears after the user approved a list is a tool nobody
	// approved.
	tools []Tool

	// writeMu serialises requests onto the pipe; pending matches replies
	// to them. One server may be called from several goroutines — the
	// model's tool loop and a brain's own act intent are different
	// callers — so the id, not the order, is what pairs them up.
	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage
	closed  bool
	// stderr keeps the tail of what the server complained about, so a
	// server that dies has something to say beyond its exit status.
	stderr *tail
}

// Name is the name the manifest gave this server.
func (s *Server) Name() string { return s.name }

// Tools is what the server declared it can do.
func (s *Server) Tools() []Tool { return s.tools }

// Start launches a server and completes the MCP handshake. argv[0] is
// resolved on PATH or as a path; env is added to the driver's own
// environment, which is how a server gets a credential without one ever
// being written into a ghost package.
func Start(ctx context.Context, name string, argv, env []string, dir string) (*Server, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("tool server %q has no command", name)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting tool server %q: %w", name, err)
	}

	s := &Server{
		name:    name,
		cmd:     cmd,
		in:      in,
		out:     bufio.NewScanner(out),
		pending: map[int]chan json.RawMessage{},
		stderr:  &tail{},
	}
	// MCP messages are one JSON object per line, and a tool result can be
	// a whole file, so the line limit is generous.
	s.out.Buffer(make([]byte, 0, 64*1024), 8<<20)
	go s.stderr.drain(errPipe)
	go s.read()

	if err := s.handshake(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("tool server %q: %w", name, err)
	}
	return s, nil
}

// handshake performs initialize, the initialized notification, and the
// tools/list that tells the driver what this server is for.
func (s *Server) handshake(ctx context.Context) error {
	raw, err := s.request(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		// No capabilities of our own: this client neither samples nor
		// serves roots, and saying so is how a server knows not to ask.
		"capabilities": map[string]any{},
		"clientInfo":   map[string]any{"name": "haunt", "version": "1"},
	})
	if err != nil {
		return err
	}
	// The result is not inspected. A server may answer with a protocol
	// version other than the one asked for, and the only parts of it this
	// client would act on are parts it does not use.
	_ = raw
	if err := s.notify("notifications/initialized", map[string]any{}); err != nil {
		return err
	}
	raw, err = s.request(ctx, "tools/list", map[string]any{})
	if err != nil {
		return err
	}
	var list struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("tools/list: %w", err)
	}
	for _, t := range list.Tools {
		if t.Name == "" {
			continue
		}
		s.tools = append(s.tools, t)
	}
	return nil
}

// Call runs one tool. args is the JSON object of arguments; empty means no
// arguments, which some tools take.
func (s *Server) Call(ctx context.Context, tool string, args json.RawMessage) (Result, error) {
	params := map[string]any{"name": tool}
	if len(args) > 0 {
		params["arguments"] = args
	} else {
		params["arguments"] = map[string]any{}
	}
	raw, err := s.request(ctx, "tools/call", params)
	if err != nil {
		return Result{}, err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return Result{}, fmt.Errorf("tools/call %s: %w", tool, err)
	}
	// Text blocks only. A tool that answers with an image or an embedded
	// resource has answered something this client has nowhere to put — the
	// ghost's balloon draws pictures it generated, not pictures a tool
	// server handed it — so it is reported as such rather than dropped
	// silently.
	var parts []string
	var other int
	for _, c := range res.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
			continue
		}
		other++
	}
	text := strings.Join(parts, "\n")
	if text == "" && other > 0 {
		text = fmt.Sprintf("(the tool answered with %d thing(s) that are not text)", other)
	}
	return Result{Text: text, IsError: res.IsError}, nil
}

// request sends a call and waits for its reply.
func (s *Server) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("tool server has stopped")
	}
	s.nextID++
	id := s.nextID
	reply := make(chan json.RawMessage, 1)
	s.pending[id] = reply
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	if err := s.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		return nil, err
	}
	select {
	case raw := <-reply:
		var env struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, err
		}
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, env.Error.Message)
		}
		return env.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Server) notify(method string, params any) error {
	return s.write(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (s *Server) write(msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.in.Write(b); err != nil {
		return fmt.Errorf("tool server %q: %w", s.name, err)
	}
	return nil
}

// read fans replies out to whoever is waiting for them, until the server's
// stdout closes.
func (s *Server) read() {
	for s.out.Scan() {
		line := s.out.Bytes()
		if len(line) == 0 {
			continue
		}
		var env struct {
			ID *int `json:"id"`
		}
		if json.Unmarshal(line, &env) != nil || env.ID == nil {
			// A notification, or something unparseable. Neither is
			// anything this client acts on.
			continue
		}
		// Copied: the scanner reuses its buffer under the next line.
		raw := make(json.RawMessage, len(line))
		copy(raw, line)
		s.mu.Lock()
		ch, ok := s.pending[*env.ID]
		s.mu.Unlock()
		if ok {
			ch <- raw
		}
	}
	// Stdout is gone, so nothing will ever answer the calls still waiting.
	// Unblocking them with an error beats leaving a ghost mid-action until
	// its context expires.
	s.fail()
}

// fail marks the server dead and releases everyone waiting on it.
func (s *Server) fail() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for id, ch := range s.pending {
		ch <- json.RawMessage(fmt.Sprintf(
			`{"id":%d,"error":{"code":-32000,"message":%q}}`,
			id, "the tool server stopped: "+s.stderr.String()))
		delete(s.pending, id)
	}
}

// Close stops the server. Closing stdin is the polite request; the kill is
// for one that will not take the hint, because a ghost shutting down must
// not be held open by somebody else's process.
func (s *Server) Close() error {
	s.fail()
	s.in.Close()
	done := make(chan struct{})
	go func() {
		s.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		s.cmd.Process.Kill()
		<-done
	}
	return nil
}

// tail keeps the last few lines a server wrote to stderr, so a crash can be
// reported with the server's own account of it rather than "exit status 1".
type tail struct {
	mu    sync.Mutex
	lines []string
}

const tailLines = 5

func (t *tail) drain(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 256*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		t.mu.Lock()
		t.lines = append(t.lines, line)
		if len(t.lines) > tailLines {
			t.lines = t.lines[len(t.lines)-tailLines:]
		}
		t.mu.Unlock()
	}
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == 0 {
		return "it said nothing about why"
	}
	return strings.Join(t.lines, "; ")
}
