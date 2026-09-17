package main

// haunt mcp-serve is the ghost's verbs as an MCP tool server. hermes (the
// brain) spawns it from the profile's config.yaml and talks MCP to it over
// stdio; every call is forwarded over the ghost's control socket to the
// running driver, which does the gating and the effect. This process holds
// no state and makes no decisions: it is a translator, so that the control
// socket stays the one door into a running ghost.
//
// It is the server half of what internal/mcp is the client half of, and as
// deliberately small: initialize, ping, tools/list, tools/call. Nothing
// else a client might ask for is offered, and the capabilities say so.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aaronjauregui/haunt/internal/control"
)

// mcpVersions are the protocol versions this server will agree to. Nothing
// here depends on what changed between them — tools/list and tools/call
// are the same in all three — so the client's choice is echoed if it is one
// of these, and the oldest offered otherwise, which is what the spec says a
// server that does not support the requested version should do.
var mcpVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

const mcpFallbackVersion = "2024-11-05"

// ghostRetry is how long a request waits for the ghost to appear. hermes
// starts this before, or alongside, the driver, so the first tools/list may
// well arrive before the socket exists; a ghost that is not there after
// this is not starting.
const ghostRetry = 20 * time.Second

// notRunningText is the tool result for a call the ghost was not there to
// take. It is content, not a protocol error, because the model is what has
// to hear it.
const notRunningText = "That did not work: the ghost is not running."

// JSON-RPC error codes used here.
const (
	rpcParseError     = -32700
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603
)

func cmdMCPServe(args []string) error {
	fs := flag.NewFlagSet("mcp-serve", flag.ExitOnError)
	ghost := fs.String("ghost", "", "the ghost whose tools to serve")
	fs.Parse(args)
	if *ghost == "" {
		return fmt.Errorf("mcp-serve: -ghost is required")
	}
	// stdout is the protocol; anything else written there corrupts it.
	// The package log already goes to stderr, which is where it belongs.
	log.SetOutput(os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveMCP(ctx, os.Stdin, os.Stdout, *ghost, ghostRetry)
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// mcpServer is one stdio session.
type mcpServer struct {
	ghost string
	retry time.Duration

	wmu sync.Mutex // one reply on stdout at a time
	out io.Writer

	// inflight maps a request id to its cancel, for
	// notifications/cancelled. Keyed by the id's JSON text, since an id
	// may be a number or a string and the two are different ids.
	mu       sync.Mutex
	inflight map[string]context.CancelFunc
}

// serveMCP runs the protocol on in/out until in ends or ctx is cancelled.
// Requests are handled concurrently: `ask` waits for a person, and a
// `say` in the meantime should not wait behind it. When in ends — the
// client closing stdin is how an MCP stdio session ends — calls still
// running are cancelled and waited for, so none is left acting for a
// client that has gone.
func serveMCP(ctx context.Context, in io.Reader, out io.Writer, ghost string, retry time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &mcpServer{ghost: ghost, retry: retry, out: out, inflight: map[string]context.CancelFunc{}}

	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel() // before Wait: stop what is running, then wait for it

	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
		for sc.Scan() {
			line := append([]byte(nil), sc.Bytes()...)
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
		readErr <- sc.Err()
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return err
		case line := <-lines:
			if len(line) == 0 {
				continue
			}
			var msg rpcMessage
			if err := json.Unmarshal(line, &msg); err != nil {
				s.reply(json.RawMessage("null"), nil, &rpcError{rpcParseError, "parse error: " + err.Error()})
				continue
			}
			if len(msg.ID) == 0 {
				s.notification(msg)
				continue
			}
			if msg.Method == "" {
				// A response to a request we never sent; nothing to do.
				continue
			}
			callCtx, callCancel := context.WithCancel(ctx)
			key := string(msg.ID)
			s.mu.Lock()
			s.inflight[key] = callCancel
			s.mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					s.mu.Lock()
					delete(s.inflight, key)
					s.mu.Unlock()
					callCancel()
				}()
				result, rerr := s.handle(callCtx, msg)
				if callCtx.Err() != nil {
					// Cancelled by the client or by the session ending:
					// the spec says a cancelled request gets no answer.
					return
				}
				s.reply(msg.ID, result, rerr)
			}()
		}
	}
}

// notification handles a message with no id. Only cancellation means
// anything here; the rest (notifications/initialized among them) are
// acknowledgements this server has no use for.
func (s *mcpServer) notification(msg rpcMessage) {
	if msg.Method != "notifications/cancelled" {
		return
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(msg.Params, &p) != nil || len(p.RequestID) == 0 {
		return
	}
	s.mu.Lock()
	cancel := s.inflight[string(p.RequestID)]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *mcpServer) handle(ctx context.Context, msg rpcMessage) (any, *rpcError) {
	switch msg.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(msg.Params, &p)
		version := p.ProtocolVersion
		if !mcpVersions[version] {
			version = mcpFallbackVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "haunt", "version": "1"},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return s.toolsList(ctx)
	case "tools/call":
		return s.toolsCall(ctx, msg.Params)
	}
	return nil, &rpcError{rpcMethodNotFound, "method not found: " + msg.Method}
}

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// toolsList is a protocol error when the ghost is not there, not an empty
// list: a client that got an empty list would believe it and offer the
// model nothing, where an error is something it retries.
func (s *mcpServer) toolsList(ctx context.Context) (any, *rpcError) {
	var specs []control.ToolSpec
	err := s.withGhost(ctx, func() error {
		var err error
		specs, err = control.ListTools(s.ghost)
		return err
	})
	if err != nil {
		log.Printf("mcp-serve: tools/list: %v", err)
		return nil, &rpcError{rpcInternalError, err.Error()}
	}
	tools := make([]mcpTool, 0, len(specs))
	for _, t := range specs {
		schema := t.Schema
		if len(schema) == 0 || string(schema) == "null" {
			// inputSchema is required; a tool without arguments still
			// takes an object.
			schema = json.RawMessage(`{"type":"object"}`)
		}
		tools = append(tools, mcpTool{Name: t.Name, Description: t.Description, InputSchema: schema})
	}
	return map[string]any{"tools": tools}, nil
}

// toolsCall forwards one call. Every failure past a malformed request is
// content with isError set, never a protocol error: the model has to read
// "the ghost is not running" to say something sensible about it.
func (s *mcpServer) toolsCall(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return nil, &rpcError{rpcInvalidParams, "tools/call needs a tool name"}
	}
	var res control.ToolResult
	err := s.withGhost(ctx, func() error {
		var err error
		res, err = control.CallTool(ctx, s.ghost, p.Name, p.Arguments)
		return err
	})
	switch {
	case err == nil:
	case control.IsNotRunning(err):
		res = control.ToolResult{Text: notRunningText, IsError: true}
	default:
		log.Printf("mcp-serve: tools/call %s: %v", p.Name, err)
		res = control.ToolResult{Text: "That did not work: " + err.Error(), IsError: true}
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": res.Text}},
		"isError": res.IsError,
	}, nil
}

// withGhost runs f, retrying with backoff while the ghost is not there yet,
// for up to s.retry. Only a failure to connect is retried: the request was
// never delivered, so trying again cannot do anything twice.
func (s *mcpServer) withGhost(ctx context.Context, f func() error) error {
	deadline := time.Now().Add(s.retry)
	wait := 50 * time.Millisecond
	for {
		err := f()
		if err == nil || !control.IsNotRunning(err) {
			return err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return err
		}
		if wait > left {
			wait = left
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return errors.Join(err, ctx.Err())
		case <-t.C:
		}
		if wait *= 2; wait > 2*time.Second {
			wait = 2 * time.Second
		}
	}
}

func (s *mcpServer) reply(id json.RawMessage, result any, rerr *rpcError) {
	r := rpcReply{JSONRPC: "2.0", ID: id, Error: rerr}
	if rerr == nil {
		r.Result = result
	}
	b, err := json.Marshal(r)
	if err != nil {
		b, _ = json.Marshal(rpcReply{JSONRPC: "2.0", ID: id, Error: &rpcError{rpcInternalError, err.Error()}})
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.out.Write(append(b, '\n'))
}
