package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// The tests need a real MCP server on the far end of a real pipe, because
// the pipe is most of what this package is. Rather than shipping a fixture
// binary, the test re-executes itself: HAUNT_MCP_FAKE picks the server
// behaviour, TestMain runs it, and everything else is an ordinary client
// against an ordinary process.
func TestMain(m *testing.M) {
	if role := os.Getenv("HAUNT_MCP_FAKE"); role != "" {
		fakeServer(role)
		return
	}
	os.Exit(m.Run())
}

// start launches the test binary as a tool server playing the given role.
func start(t *testing.T, role string) *Server {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := Start(ctx, "fake", []string{os.Args[0]},
		append(os.Environ(), "HAUNT_MCP_FAKE="+role), "")
	if err != nil {
		t.Fatalf("starting the %s server: %v", role, err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestHandshakeListsTools(t *testing.T) {
	s := start(t, "ok")
	tools := s.Tools()
	if len(tools) != 2 {
		t.Fatalf("tools = %d, want the two the server declares", len(tools))
	}
	if tools[0].Name != "append" {
		t.Errorf("first tool = %q, want append", tools[0].Name)
	}
	if tools[0].Description == "" {
		t.Error("the description is empty, and it is what the user is asked to consent to")
	}
	// The schema is passed to a language model unchanged, so it has to
	// survive the round trip as JSON rather than as a re-encoding of it.
	if !json.Valid(tools[0].Schema) {
		t.Errorf("schema = %s, want valid JSON", tools[0].Schema)
	}
}

func TestCallReturnsText(t *testing.T) {
	s := start(t, "ok")
	res, err := s.Call(context.Background(), "append", json.RawMessage(`{"text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Error("the call is reported as a failure and the server said it worked")
	}
	if !strings.Contains(res.Text, "hello") {
		t.Errorf("text = %q, want the arguments echoed back", res.Text)
	}
}

// A tool that fails on its own terms is an answer, not a transport error:
// the thing that asked has to hear "the file is not there" in order to say
// anything sensible about it.
func TestToolFailureIsAnAnswer(t *testing.T) {
	s := start(t, "ok")
	res, err := s.Call(context.Background(), "fail", nil)
	if err != nil {
		t.Fatalf("a failing tool came back as a transport error: %v", err)
	}
	if !res.IsError {
		t.Error("the failure was not reported as one")
	}
	if res.Text == "" {
		t.Error("the failure came back with nothing to say about itself")
	}
}

// A JSON-RPC error is a different thing from a tool failure and stays an
// error.
func TestProtocolErrorIsAnError(t *testing.T) {
	s := start(t, "ok")
	if _, err := s.Call(context.Background(), "nosuch", nil); err == nil {
		t.Fatal("calling a tool the server rejects succeeded")
	}
}

// Calls are matched to replies by id, not by order, so a server that
// answers out of order still answers the right caller.
func TestOutOfOrderReplies(t *testing.T) {
	s := start(t, "reorder")
	type got struct {
		text string
		err  error
	}
	results := make(chan got, 2)
	for _, arg := range []string{"first", "second"} {
		go func(arg string) {
			res, err := s.Call(context.Background(), "echo",
				json.RawMessage(`{"text":"`+arg+`"}`))
			results <- got{res.Text, err}
		}(arg)
	}
	seen := map[string]bool{}
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		seen[r.text] = true
	}
	for _, want := range []string{"first", "second"} {
		if !seen[want] {
			t.Errorf("%q never came back to the goroutine that asked for it; got %v", want, seen)
		}
	}
}

// A server that dies mid-call must not leave the ghost waiting on it: the
// balloon is showing a working face and somebody is watching it.
func TestServerDeathUnblocksCallers(t *testing.T) {
	s := start(t, "die")
	done := make(chan error, 1)
	go func() {
		_, err := s.Call(context.Background(), "append", nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a call to a dead server succeeded")
		}
		// The server's own last words, which is the difference between a
		// usable report and "exit status 1".
		if !strings.Contains(err.Error(), "about to fall over") {
			t.Errorf("error = %v, want the server's own complaint in it", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the call never came back after the server died")
	}
}

func TestStartRefusesAnEmptyCommand(t *testing.T) {
	if _, err := Start(context.Background(), "fake", nil, nil, ""); err == nil {
		t.Fatal("a server with no command started")
	}
}

// fakeServer speaks just enough MCP to be a server, in whichever way the
// role asks for.
func fakeServer(role string) {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := dec.Decode(&req); err != nil {
			return
		}
		if req.ID == nil {
			continue // a notification; nothing to answer
		}
		reply := func(result any) {
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
		}
		switch req.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": "2024-11-05"})
		case "tools/list":
			reply(map[string]any{"tools": []map[string]any{
				{
					"name":        "append",
					"description": "Append a line to today's note",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"text": map[string]any{"type": "string"}},
					},
				},
				{"name": "fail", "description": "Always fails"},
			}})
		case "tools/call":
			fakeCall(role, req.Params, *req.ID, enc)
		default:
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID,
				"error": map[string]any{"code": -32601, "message": "no such method"}})
		}
	}
}

func fakeCall(role string, params json.RawMessage, id int, enc *json.Encoder) {
	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	json.Unmarshal(params, &call)
	text, _ := call.Arguments["text"].(string)

	switch {
	case role == "die":
		os.Stderr.WriteString("the fake server is about to fall over\n")
		os.Exit(1)
	case role == "reorder" && text == "first":
		// The point of the role: answer the *second* call first.
		go func() {
			time.Sleep(150 * time.Millisecond)
			enc.Encode(rpcResult(id, text, false))
		}()
		return
	case call.Name == "fail":
		enc.Encode(rpcResult(id, "the note is not there", true))
	case call.Name == "append" || call.Name == "echo":
		enc.Encode(rpcResult(id, text, false))
	default:
		enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id,
			"error": map[string]any{"code": -32602, "message": "no tool called " + call.Name}})
	}
}

func rpcResult(id int, text string, isErr bool) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}}
}
