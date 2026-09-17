package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/control"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// mcpFakeTools stands in for the driver: "echo" answers at once, "wait"
// holds until released or cancelled.
type mcpFakeTools struct {
	release chan struct{}
}

func (f *mcpFakeTools) List() []control.ToolSpec {
	return []control.ToolSpec{
		{Name: "echo", Description: "says it back", Schema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)},
		{Name: "wait", Description: "waits"},
	}
}

func (f *mcpFakeTools) Call(ctx context.Context, name string, args json.RawMessage) control.ToolResult {
	switch name {
	case "echo":
		var a struct{ Text string }
		json.Unmarshal(args, &a)
		return control.ToolResult{Text: a.Text}
	case "wait":
		select {
		case <-f.release:
			return control.ToolResult{Text: "released"}
		case <-ctx.Done():
			return control.ToolResult{Text: "cancelled", IsError: true}
		}
	}
	return control.ToolResult{Text: "That did not work: no tool " + name, IsError: true}
}

// mcpSession drives serveMCP over pipes.
type mcpSession struct {
	t    *testing.T
	in   *io.PipeWriter
	out  *bufio.Scanner
	done chan error
}

func startMCP(t *testing.T, ghost string, retry time.Duration) *mcpSession {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &mcpSession{t: t, in: inW, out: bufio.NewScanner(outR), done: make(chan error, 1)}
	go func() {
		s.done <- serveMCP(context.Background(), inR, outW, ghost, retry)
		outW.Close()
	}()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Error("serveMCP did not return after stdin closed")
		}
	})
	return s
}

func (s *mcpSession) send(line string) {
	s.t.Helper()
	if _, err := s.in.Write([]byte(line + "\n")); err != nil {
		s.t.Fatal(err)
	}
}

type mcpTestReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

func (s *mcpSession) recv() mcpTestReply {
	s.t.Helper()
	got := make(chan mcpTestReply, 1)
	go func() {
		if !s.out.Scan() {
			close(got)
			return
		}
		var r mcpTestReply
		if err := json.Unmarshal(s.out.Bytes(), &r); err != nil {
			s.t.Errorf("stdout carried non-protocol line %q", s.out.Text())
		}
		got <- r
	}()
	select {
	case r, ok := <-got:
		if !ok {
			s.t.Fatal("stdout closed")
		}
		return r
	case <-time.After(10 * time.Second):
		s.t.Fatal("no reply")
	}
	return mcpTestReply{}
}

func listenGhost(t *testing.T, name string, tools control.Tools) *control.Server {
	t.Helper()
	srv, err := control.Listen(name, func(proto.Event) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	srv.SetTools(tools)
	return srv
}

type callResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func decodeCall(t *testing.T, r mcpTestReply) callResult {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("tools/call protocol error: %+v", r.Error)
	}
	var c callResult
	if err := json.Unmarshal(r.Result, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Content) != 1 || c.Content[0].Type != "text" {
		t.Fatalf("content = %+v", c.Content)
	}
	return c
}

func TestMCPServeRoundTrip(t *testing.T) {
	t.Setenv("HAUNT_RUNTIME_DIR", t.TempDir())
	listenGhost(t, "spook", &mcpFakeTools{release: make(chan struct{})})
	s := startMCP(t, "spook", time.Second)

	s.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test"}}}`)
	r := s.recv()
	var init struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		ServerInfo      struct{ Name string }      `json:"serverInfo"`
	}
	json.Unmarshal(r.Result, &init)
	if string(r.ID) != "1" || init.ProtocolVersion != "2025-06-18" || init.ServerInfo.Name != "haunt" ||
		string(init.Capabilities["tools"]) != `{"listChanged":false}` {
		t.Errorf("initialize = %s", r.Result)
	}
	s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	s.send(`{"jsonrpc":"2.0","id":"p","method":"ping"}`)
	if r := s.recv(); string(r.ID) != `"p"` || string(r.Result) != `{}` {
		t.Errorf("ping = %+v", r)
	}

	s.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	r = s.recv()
	var list struct{ Tools []mcpTool }
	if err := json.Unmarshal(r.Result, &list); err != nil || len(list.Tools) != 2 {
		t.Fatalf("tools/list = %s (%v)", r.Result, err)
	}
	if list.Tools[0].Name != "echo" || !strings.Contains(string(list.Tools[0].InputSchema), `"properties"`) {
		t.Errorf("echo = %+v", list.Tools[0])
	}
	if string(list.Tools[1].InputSchema) != `{"type":"object"}` {
		t.Errorf("schema-less tool got inputSchema %s", list.Tools[1].InputSchema)
	}

	s.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"boo"}}}`)
	if c := decodeCall(t, s.recv()); c.Content[0].Text != "boo" || c.IsError {
		t.Errorf("echo = %+v", c)
	}

	// A tool's own failure is content, not a protocol error.
	s.send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`)
	if c := decodeCall(t, s.recv()); !c.IsError {
		t.Errorf("failed tool not marked: %+v", c)
	}

	s.send(`{"jsonrpc":"2.0","id":5,"method":"resources/list"}`)
	if r := s.recv(); r.Error == nil || r.Error.Code != -32601 {
		t.Errorf("unknown method = %+v", r)
	}

	s.send(`{"jsonrpc":"2.0","id":6,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	json.Unmarshal(s.recv().Result, &init)
	if init.ProtocolVersion != "2024-11-05" {
		t.Errorf("unknown version answered with %q", init.ProtocolVersion)
	}
}

func TestMCPServeConcurrentCalls(t *testing.T) {
	t.Setenv("HAUNT_RUNTIME_DIR", t.TempDir())
	f := &mcpFakeTools{release: make(chan struct{})}
	listenGhost(t, "spook", f)
	s := startMCP(t, "spook", time.Second)

	s.send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wait"}}`)
	s.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"meanwhile"}}}`)
	r := s.recv()
	if string(r.ID) != "2" || decodeCall(t, r).Content[0].Text != "meanwhile" {
		t.Fatalf("blocked call held up the other: %+v", r)
	}
	close(f.release)
	r = s.recv()
	if string(r.ID) != "1" || decodeCall(t, r).Content[0].Text != "released" {
		t.Errorf("blocked call = %+v", r)
	}

	// A cancelled request is dropped, and the next one still answered.
	f.release = make(chan struct{})
	s.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"wait"}}`)
	time.Sleep(100 * time.Millisecond)
	s.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`)
	s.send(`{"jsonrpc":"2.0","id":4,"method":"ping"}`)
	if r := s.recv(); string(r.ID) != "4" {
		t.Errorf("after cancel got reply to %s", r.ID)
	}
}

func TestMCPServeGhostNotRunning(t *testing.T) {
	t.Setenv("HAUNT_RUNTIME_DIR", t.TempDir())
	s := startMCP(t, "nobody", 200*time.Millisecond)

	s.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if r := s.recv(); r.Error == nil {
		t.Errorf("tools/list with no ghost = %s, want an error", r.Result)
	}
	s.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"say","arguments":{"text":"hi"}}}`)
	c := decodeCall(t, s.recv())
	if !c.IsError || c.Content[0].Text != notRunningText {
		t.Errorf("tools/call with no ghost = %+v", c)
	}
}

func TestMCPServeGhostArrivesLate(t *testing.T) {
	t.Setenv("HAUNT_RUNTIME_DIR", t.TempDir())
	s := startMCP(t, "late", 10*time.Second)

	s.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	time.Sleep(300 * time.Millisecond)
	listenGhost(t, "late", &mcpFakeTools{})
	r := s.recv()
	var list struct{ Tools []mcpTool }
	if r.Error != nil || json.Unmarshal(r.Result, &list) != nil || len(list.Tools) != 2 {
		t.Errorf("tools/list once the ghost arrived = %+v", r)
	}
}
