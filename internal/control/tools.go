package control

// Tool requests: the half of the control socket that asks something of a
// running ghost and waits for the answer, rather than dropping an event on
// it. It exists for `haunt mcp-serve`, which is how the brain (hermes)
// reaches the ghost's verbs; keeping it on this socket keeps the socket the
// one door into a running ghost, and so the driver the one writer of its
// store.
//
// A request is a line whose "type" is tool_list or tool_call. Everything
// else on the line is read as an event, exactly as before, so `haunt say`
// and anything scripted against the socket see no difference.
//
// A connection that sends a request gets one answer and is closed. One
// request per connection is what makes cancellation simple: the client
// hanging up is the cancellation, with no request ids to route and no
// half-cancelled multiplexed stream to reason about. Dialing a Unix socket
// is cheap enough that nothing is saved by reusing one.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// ToolSpec describes one thing a ghost can be asked to do. Name is
// wire-safe ([A-Za-z0-9_-]); the driver guarantees that, not this package,
// because the driver is what knows how its names are built.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"input_schema"` // JSON Schema object
}

// ToolResult is what a call produced. A refusal or a failure is still a
// result — "That was not allowed: ..." is something the model has to read
// to say something sensible next — so IsError marks it rather than an
// error return.
type ToolResult struct {
	Text    string `json:"text"`
	IsError bool   `json:"is_error,omitempty"`
}

// Tools is what a running ghost offers its brain. Implemented by the driver
// (internal/ghost), which applies consent, budgets and the visible marks
// inside Call.
//
// Call may block for as long as the tool takes — `ask` waits for a person
// to pick from a menu — and must return promptly once ctx is cancelled:
// that is how the caller hanging up, or the ghost shutting down, reaches it.
type Tools interface {
	List() []ToolSpec
	Call(ctx context.Context, name string, args json.RawMessage) ToolResult
}

// Request and response types on the wire.
const (
	reqToolList    = "tool_list"
	reqToolCall    = "tool_call"
	respToolList   = "tool_list_result"
	respToolResult = "tool_result"
	respError      = "error"
)

type request struct {
	Type string          `json:"type"`
	ID   string          `json:"id"`
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
}

// The responses are separate types rather than one with omitempty
// everywhere, because a tool_result says is_error:false out loud and a
// tool_list_result with no tools says tools:[] — a reader should not have
// to know which absent field means what.
type listResponse struct {
	Type  string     `json:"type"`
	ID    string     `json:"id"`
	Tools []ToolSpec `json:"tools"`
}

type callResponse struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Text    string `json:"text"`
	IsError bool   `json:"is_error"`
}

type errorResponse struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Error string `json:"error"`
}

// response is the client's view: one struct that any of the above decodes
// into.
type response struct {
	Type    string     `json:"type"`
	ID      string     `json:"id"`
	Tools   []ToolSpec `json:"tools"`
	Text    string     `json:"text"`
	IsError bool       `json:"is_error"`
	Error   string     `json:"error"`
}

// isRequest reports whether a line is a tool request rather than an event.
// A line that does not parse is not a request; the event path answers it
// the way it always has.
func isRequest(line []byte) bool {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(line, &head) != nil {
		return false
	}
	return head.Type == reqToolList || head.Type == reqToolCall
}

// SetTools installs what the ghost offers. It may be called at any point,
// including while requests are being served: the driver listens before it
// has finished building its tool surface, and a request that arrives in
// between is told so rather than left hanging. nil withdraws the tools.
func (s *Server) SetTools(t Tools) {
	s.mu.Lock()
	s.tools = t
	s.mu.Unlock()
}

func (s *Server) currentTools() Tools {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tools
}

// replyTimeout bounds writing an answer, so a client that stopped reading
// cannot pin the goroutine.
const replyTimeout = 10 * time.Second

// serveRequest answers one request. The caller closes conn afterwards.
func (s *Server) serveRequest(conn net.Conn, line []byte) {
	var req request
	var reply any
	if err := json.Unmarshal(line, &req); err != nil {
		reply = errorResponse{Type: respError, Error: "bad request json: " + err.Error()}
	} else if tools := s.currentTools(); tools == nil {
		reply = errorResponse{Type: respError, ID: req.ID, Error: "this ghost offers no tools"}
	} else if req.Type == reqToolList {
		list := tools.List()
		if list == nil {
			list = []ToolSpec{}
		}
		reply = listResponse{Type: respToolList, ID: req.ID, Tools: list}
	} else if req.Name == "" {
		reply = errorResponse{Type: respError, ID: req.ID, Error: "tool_call without a name"}
	} else {
		args := req.Args
		if len(args) == 0 || string(args) == "null" {
			// Tools are written against an object; an argument-less call
			// arrives as one rather than as a special case each must handle.
			args = json.RawMessage("{}")
		}
		ctx, cancel := context.WithCancel(s.ctx)
		// The idle deadline was for a client that never writes. This one
		// has written and is waiting, possibly for minutes, so it goes.
		conn.SetReadDeadline(time.Time{})
		// Nothing more is expected from the client; the next thing a read
		// sees is it hanging up, which is the cancellation.
		go func() {
			io.Copy(io.Discard, conn)
			cancel()
		}()
		res := tools.Call(ctx, req.Name, args)
		cancelled := ctx.Err() != nil
		cancel()
		if cancelled {
			return // nobody to answer
		}
		reply = callResponse{Type: respToolResult, ID: req.ID, Text: res.Text, IsError: res.IsError}
	}
	if e, ok := reply.(errorResponse); ok && s.log != nil {
		s.log.Warn(hlog.Control, "tool request: %s", e.Error)
	}
	b, err := json.Marshal(reply)
	if err != nil {
		return
	}
	conn.SetWriteDeadline(time.Now().Add(replyTimeout))
	conn.Write(append(b, '\n'))
}

// NotRunningError is what the clients return when nothing answers on the
// ghost's socket. It is a type so that a caller which can wait for the
// ghost (mcp-serve, started alongside the driver) can tell "not up yet"
// from "up and said no".
type NotRunningError struct {
	Ghost string
	Err   error
}

func (e *NotRunningError) Error() string {
	return fmt.Sprintf("ghost %q does not appear to be running: %v", e.Ghost, e.Err)
}

func (e *NotRunningError) Unwrap() error { return e.Err }

// IsNotRunning reports whether err means the ghost was not there to ask.
func IsNotRunning(err error) bool {
	var nr *NotRunningError
	return errors.As(err, &nr)
}

// listTimeout bounds ListTools. Listing is answered from memory; a ghost
// that takes longer than this is wedged, and saying so beats hanging the
// brain.
const listTimeout = 10 * time.Second

// ListTools asks the named ghost what tools it offers.
func ListTools(ghost string) ([]ToolSpec, error) {
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	resp, err := roundTrip(ctx, ghost, request{Type: reqToolList, ID: "1"})
	if err != nil {
		return nil, err
	}
	if resp.Type != respToolList {
		return nil, fmt.Errorf("unexpected reply %q to tool_list", resp.Type)
	}
	return resp.Tools, nil
}

// CallTool runs one tool on the named ghost and waits for its result, for
// as long as the tool takes. Cancelling ctx hangs up, which the ghost sees
// and passes on to the tool.
func CallTool(ctx context.Context, ghost, name string, args json.RawMessage) (ToolResult, error) {
	resp, err := roundTrip(ctx, ghost, request{Type: reqToolCall, ID: "1", Name: name, Args: args})
	if err != nil {
		return ToolResult{}, err
	}
	if resp.Type != respToolResult {
		return ToolResult{}, fmt.Errorf("unexpected reply %q to tool_call", resp.Type)
	}
	return ToolResult{Text: resp.Text, IsError: resp.IsError}, nil
}

// roundTrip sends one request on a fresh connection and reads the one
// answer. The id is fixed: one request per connection means there is
// nothing to match it against, and it is sent only so the wire format
// reads the same in both directions.
func roundTrip(ctx context.Context, ghost string, req request) (response, error) {
	path, err := SocketPath(ghost)
	if err != nil {
		return response{}, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		if ctx.Err() != nil {
			return response{}, ctx.Err()
		}
		return response{}, &NotRunningError{Ghost: ghost, Err: err}
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	b, err := json.Marshal(req)
	if err != nil {
		return response{}, err
	}
	if _, err := conn.Write(append(b, '\n')); err != nil {
		if ctx.Err() != nil {
			return response{}, ctx.Err()
		}
		return response{}, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		if ctx.Err() != nil {
			return response{}, ctx.Err()
		}
		return response{}, fmt.Errorf("no answer from ghost %q: %w", ghost, err)
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return response{}, fmt.Errorf("bad answer from ghost %q: %w", ghost, err)
	}
	if resp.Type == respError {
		return response{}, fmt.Errorf("ghost %q: %s", ghost, resp.Error)
	}
	return resp, nil
}
