package hermes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client is one ghost's line to its hermes gateway's API server.
//
// A ghost has one session for its whole life. Hermes compresses it as it
// grows, and compression may end the session and carry on in a child with
// a new id; the client follows that by taking the id each finished turn
// reports, and hands it to SaveSession so the next start resumes the same
// conversation.
type Client struct {
	// BaseURL is the API server, "http://127.0.0.1:<port>".
	BaseURL string
	// Key is the bearer key the API server requires.
	Key string
	// Ghost scopes hermes's long-term memory and prompt cache
	// (X-Hermes-Session-Key), and is the session id asked for on first
	// run.
	Ghost string
	// LoadSession and SaveSession keep the session id across restarts.
	LoadSession func() string
	SaveSession func(id string)
	// HTTP defaults to a client with no overall timeout: a turn lasts as
	// long as its tool calls do, and the caller's context is the bound.
	HTTP *http.Client

	mu      sync.Mutex
	session string
}

var _ Turner = (*Client)(nil)

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// apiError is a non-2xx answer from the API server.
type apiError struct {
	Status int
	Code   string
	Msg    string
}

func (e *apiError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("hermes: %d %s: %s", e.Status, e.Code, e.Msg)
	}
	return fmt.Sprintf("hermes: %d: %s", e.Status, e.Msg)
}

func (c *Client) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	if c.Ghost != "" {
		req.Header.Set("X-Hermes-Session-Key", c.Ghost)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, readAPIError(resp)
	}
	return resp, nil
}

func readAPIError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	e := &apiError{Status: resp.StatusCode, Msg: strings.TrimSpace(string(raw))}
	// Hermes answers errors in more than one shape; take whichever is there.
	var body struct {
		Code  string `json:"code"`
		Error json.RawMessage
	}
	if json.Unmarshal(raw, &body) == nil {
		e.Code = body.Code
		var inner struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		var s string
		switch {
		case json.Unmarshal(body.Error, &inner) == nil && inner.Message != "":
			e.Msg = inner.Message
			if inner.Code != "" {
				e.Code = inner.Code
			}
		case json.Unmarshal(body.Error, &s) == nil && s != "":
			e.Msg = s
		}
	}
	if e.Code == "" && strings.Contains(e.Msg, "session_exists") {
		e.Code = "session_exists"
	}
	if e.Code == "" && strings.Contains(e.Msg, "session_not_found") {
		e.Code = "session_not_found"
	}
	return e
}

func isCode(err error, code string) bool {
	var e *apiError
	return errors.As(err, &e) && e.Code == code
}

// Health reports whether the API server is answering.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hermes health: %s", resp.Status)
	}
	return nil
}

// Session returns the ghost's session id, creating the session the first
// time. The id asked for is the ghost's name, so a lost store finds the
// same conversation again rather than starting a second one.
func (c *Client) Session(ctx context.Context) (string, error) {
	c.mu.Lock()
	id := c.session
	c.mu.Unlock()
	if id != "" {
		return id, nil
	}
	if c.LoadSession != nil {
		id = c.LoadSession()
	}
	if id == "" {
		var err error
		if id, err = c.create(ctx, "haunt-"+c.Ghost); err != nil {
			return "", err
		}
	}
	c.setSession(id)
	return id, nil
}

func (c *Client) create(ctx context.Context, want string) (string, error) {
	resp, err := c.request(ctx, http.MethodPost, "/api/sessions", map[string]string{
		"id":     want,
		"title":  c.Ghost,
		"source": "haunt",
	})
	if isCode(err, "session_exists") {
		return want, nil
	}
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("hermes: reading the new session: %w", err)
	}
	if out.Session.ID == "" {
		return want, nil
	}
	return out.Session.ID, nil
}

func (c *Client) setSession(id string) {
	c.mu.Lock()
	changed := c.session != id
	c.session = id
	c.mu.Unlock()
	if changed && c.SaveSession != nil {
		c.SaveSession(id)
	}
}

// Turn sends one turn and streams its answer.
func (c *Client) Turn(ctx context.Context, req TurnRequest) (Reply, error) {
	id, err := c.Session(ctx)
	if err != nil {
		return Reply{}, err
	}
	reply, err := c.turn(ctx, id, req)
	if isCode(err, "session_not_found") {
		// The session went away under us — a hermes home that was reset,
		// say. A ghost's life starts again rather than stopping.
		c.setSession("")
		if id, err = c.create(ctx, "haunt-"+c.Ghost); err != nil {
			return Reply{}, err
		}
		c.setSession(id)
		reply, err = c.turn(ctx, id, req)
	}
	return reply, err
}

func (c *Client) turn(ctx context.Context, id string, req TurnRequest) (Reply, error) {
	body := map[string]any{"input": req.Input}
	if req.Context != "" {
		// Layered on top of the system prompt for this turn only, and not
		// persisted, so what the ghost can see never enters the record.
		body["system_message"] = req.Context
	}
	resp, err := c.request(ctx, http.MethodPost, "/api/sessions/"+url.PathEscape(id)+"/chat/stream", body)
	if err != nil {
		return Reply{}, err
	}
	defer resp.Body.Close()

	var (
		reply     Reply
		streamed  strings.Builder
		completed bool
		failure   error
	)
	err = readSSE(resp.Body, func(event string, data []byte) bool {
		var ev sseEvent
		if len(data) > 0 {
			if err := json.Unmarshal(data, &ev); err != nil {
				return true
			}
		}
		if ev.SessionID != "" && ev.SessionID != id && (event == "assistant.completed" || strings.HasPrefix(event, "run.")) {
			// Compression moved the conversation to a child session.
			id = ev.SessionID
			c.setSession(id)
		}
		switch event {
		case "assistant.delta":
			streamed.WriteString(ev.Delta)
			if req.OnDelta != nil && ev.Delta != "" {
				req.OnDelta(ev.Delta)
			}
		case "tool.started", "tool.completed", "tool.failed":
			// tool.progress is hermes thinking aloud, and not a tool.
			if ev.ToolName == "" || strings.HasPrefix(ev.ToolName, "_") {
				return true
			}
			te := ToolEvent{Name: ev.ToolName, Done: event != "tool.started", Failed: event == "tool.failed"}
			if !te.Done {
				reply.Tools = append(reply.Tools, ev.ToolName)
			}
			if req.OnTool != nil {
				req.OnTool(te)
			}
		case "assistant.completed":
			reply.Text = ev.Content
			completed = true
		case "run.failed":
			failure = fmt.Errorf("hermes: the turn failed%s", ev.reason())
		case "run.cancelled":
			failure = fmt.Errorf("hermes: the turn was cancelled")
		case "error":
			failure = fmt.Errorf("hermes: %s", ev.Message)
		case "done":
			return false
		}
		return true
	})
	if ctx.Err() != nil {
		return reply, ctx.Err()
	}
	if failure != nil {
		return reply, failure
	}
	if err != nil {
		return reply, fmt.Errorf("hermes: reading the stream: %w", err)
	}
	if !completed {
		if streamed.Len() == 0 {
			return reply, fmt.Errorf("hermes: the stream ended without an answer")
		}
		reply.Text = streamed.String()
	}
	return reply, nil
}

// sseEvent is the union of the payload fields haunt reads.
type sseEvent struct {
	SessionID      string `json:"session_id"`
	Delta          string `json:"delta"`
	ToolName       string `json:"tool_name"`
	Content        string `json:"content"`
	Message        string `json:"message"`
	TurnExitReason string `json:"turn_exit_reason"`
}

func (e sseEvent) reason() string {
	if e.TurnExitReason != "" {
		return ": " + e.TurnExitReason
	}
	return ""
}

// UnmarshalJSON tolerates the fields whose shape varies by event (message
// is an object on message.started and a string on error).
func (e *sseEvent) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	str := func(k string) string {
		var s string
		json.Unmarshal(raw[k], &s)
		return s
	}
	*e = sseEvent{
		SessionID:      str("session_id"),
		Delta:          str("delta"),
		ToolName:       str("tool_name"),
		Content:        str("content"),
		Message:        str("message"),
		TurnExitReason: str("turn_exit_reason"),
	}
	return nil
}

// readSSE calls fn for each event until fn returns false or the stream
// ends.
func readSSE(r io.Reader, fn func(event string, data []byte) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	var event string
	var data bytes.Buffer
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if event != "" || data.Len() > 0 {
				if event == "" {
					event = "message"
				}
				if !fn(event, data.Bytes()) {
					return nil
				}
			}
			event = ""
			data.Reset()
		case strings.HasPrefix(line, ":"):
			// keepalive
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	return sc.Err()
}

// Messages reads back the last n entries of the ghost's session, oldest
// first, for `haunt history`. Tool traffic is not an entry of its own: it
// is the record of how an answer was reached, and the history is what was
// said. The calls ride on the reply they led to: hermes's own in
// Message.Looked, the ghost's in Message.Acted. Their arguments and results
// are cut to a line each, which is what a person reading the history wants.
func (c *Client) Messages(ctx context.Context, n int) ([]Message, error) {
	return c.messages(ctx, n, lookupArgs, lookupResult)
}

// Transcript is Messages with every call's arguments and result whole, for
// a reader that matches on them (`haunt history -json`) rather than a
// person skimming.
func (c *Client) Transcript(ctx context.Context, n int) ([]Message, error) {
	return c.messages(ctx, n, 0, 0)
}

// messages reads the session, cutting calls' arguments and results to
// argsMax and resultMax runes; 0 leaves them as hermes stored them.
func (c *Client) messages(ctx context.Context, n, argsMax, resultMax int) ([]Message, error) {
	id, err := c.Session(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{"order": {"latest"}}
	if n > 0 {
		q.Set("limit", strconv.Itoa(n))
	}
	resp, err := c.request(ctx, http.MethodGet, "/api/sessions/"+url.PathEscape(id)+"/messages?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			Timestamp float64 `json:"timestamp"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("hermes: reading the history: %w", err)
	}
	cut := func(s string, n int) string {
		if n == 0 {
			return s
		}
		return clip(s, n)
	}
	// A call's result is a message of its own, after the call; byCall
	// finds the call it answers, in whichever list that call went on.
	type callAt struct {
		acted bool
		i     int
	}
	msgs := make([]Message, 0, len(out.Data))
	var looked, acted []Lookup
	byCall := map[string]callAt{}
	last := -1 // the turn's latest reply, which its calls go on
	endTurn := func() {
		if last >= 0 {
			msgs[last].Looked, msgs[last].Acted = looked, acted
		}
		looked, acted, byCall, last = nil, nil, map[string]callAt{}, -1
	}
	for _, m := range out.Data {
		text := rawText(m.Content)
		switch m.Role {
		case "tool":
			if at, ok := byCall[m.ToolCallID]; ok {
				if at.acted {
					acted[at.i].Result = cut(text, resultMax)
				} else {
					looked[at.i].Result = cut(text, resultMax)
				}
			}
			continue
		case "user":
			endTurn()
		case "assistant":
			for _, tc := range m.ToolCalls {
				name := tc.Function.Name
				if name == "" {
					continue
				}
				call := Lookup{Tool: name, Args: cut(rawText(tc.Function.Arguments), argsMax)}
				if own, ok := strings.CutPrefix(name, HauntToolPrefix); ok {
					call.Tool = own
					byCall[tc.ID] = callAt{acted: true, i: len(acted)}
					acted = append(acted, call)
					continue
				}
				byCall[tc.ID] = callAt{i: len(looked)}
				looked = append(looked, call)
			}
		default:
			continue
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		sec := int64(m.Timestamp)
		msg := Message{
			Role: m.Role,
			Text: text,
			Time: time.Unix(sec, int64((m.Timestamp-float64(sec))*1e9)),
		}
		if m.Role == "assistant" {
			msg.Interim = len(m.ToolCalls) > 0
			last = len(msgs)
		}
		msgs = append(msgs, msg)
	}
	endTurn()
	// "latest" pages back from the newest and still answers oldest first.
	return msgs, nil
}

// HauntToolPrefix is how hermes names the tools haunt's MCP server offers
// it: the server is registered as "haunt", and hermes puts mcp__<server>__
// before each of its tools.
const HauntToolPrefix = "mcp__haunt__"

// How much of a call the history shows.
const (
	lookupArgs   = 120
	lookupResult = 160
)

// rawText is a JSON value as text: a string's contents, or anything else
// as written.
func rawText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	if string(raw) == "null" {
		return ""
	}
	return string(raw)
}

// clip shortens s to one line of at most n runes.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
