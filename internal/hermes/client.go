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
// grows, in place by default; with compression.in_place off, compression
// ends the session and carries on in a child with a new id, and the
// client follows that by taking the id each finished turn
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
	// Logf reports what goes wrong out of the caller's sight, such as a
	// run that could not be stopped. Nil is silent.
	Logf func(format string, a ...any)

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
		said      string // the provider's own words on a turn that did not finish
		run       string // the run's id, which every event carries
		over      bool   // whether hermes has said the run is over
	)
	err = readSSE(resp.Body, func(event string, data []byte) bool {
		if req.OnEvent != nil {
			req.OnEvent(event)
		}
		var ev sseEvent
		if len(data) > 0 {
			if err := json.Unmarshal(data, &ev); err != nil {
				return true
			}
		}
		if ev.RunID != "" {
			run = ev.RunID
		}
		if ev.SessionID != "" && ev.SessionID != id && (event == "assistant.completed" || strings.HasPrefix(event, "run.")) {
			// Compression moved the conversation to a child session.
			id = ev.SessionID
			c.setSession(id)
		}
		switch event {
		case "run.completed", "run.failed", "run.cancelled", "error":
			over = true
		}
		switch event {
		case "assistant.delta":
			streamed.WriteString(ev.Delta)
			if req.OnDelta != nil && ev.Delta != "" {
				req.OnDelta(ev.Delta)
			}
		case "assistant.commentary":
			// The words streamed since the last mark were written alongside
			// tool calls, not as the reply, which starts afresh after them.
			streamed.Reset()
			if req.OnInterim != nil {
				req.OnInterim(ev.Text)
			}
		case "tool.started", "tool.completed", "tool.failed":
			// tool.progress is hermes thinking aloud, and not a tool.
			if ev.ToolName == "" || strings.HasPrefix(ev.ToolName, "_") {
				return true
			}
			te := ToolEvent{Name: ev.ToolName, Done: event != "tool.started", Failed: event == "tool.failed"}
			if !te.Done {
				reply.Tools = append(reply.Tools, ev.ToolName)
				// Words streamed before a call were written alongside it.
				// Commentary has already said so when hermes sends it; a
				// profile with display.interim_assistant_messages off sends
				// none, and the call is the only mark there is.
				if text := streamed.String(); strings.TrimSpace(text) != "" {
					streamed.Reset()
					if req.OnInterim != nil {
						req.OnInterim(text)
					}
				}
			}
			if req.OnTool != nil {
				req.OnTool(te)
			}
		case "assistant.completed":
			if ev.Unfinished {
				// The turn did not finish, and content is hermes's notice
				// to the person, never the ghost's words: run.failed or
				// run.cancelled follows.
				said = providerSaid(ev.Content)
				return true
			}
			reply.Text = ev.Content
			completed = true
		case "run.failed":
			failure = fmt.Errorf("hermes: the turn failed%s", ev.reason())
			if said != "" {
				failure = fmt.Errorf("%w: %s", failure, said)
			}
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
		if run != "" && !over {
			c.stopRun(run)
		}
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

// How long a run given up on may take to stop: the stop request, then the
// wait for the run to be over.
var (
	stopTimeout   = 5 * time.Second
	settleTimeout = 10 * time.Second
	settlePoll    = 200 * time.Millisecond
)

// stopRun stops a run its caller has given up on, and waits for it to be
// over, so that the next turn on the session does not start beside it.
//
// Closing the stream alone stops the run only when hermes next writes to
// it, a keepalive up to 10 s later, and hermes does not refuse a second
// turn on a session whose first is still running. POST
// /v1/runs/{run_id}/stop (gateway/platforms/api_server_runs.py,
// _handle_stop_run) interrupts the agent at once, as an explicit stop,
// but answers "stopping" without waiting for it: the agent's thread winds
// down on its own, a recovery wait within 200 ms and a model call when its
// request is cut. The run is over when GET /v1/runs/{run_id} answers one
// of TERMINAL_STATUSES (api_server_run_idempotency.py), which the session
// stream's run sets once the agent has returned and its last rows are
// stored (api_server.py, _handle_session_chat_stream); a run hermes has
// already forgotten is over too. The wait is bounded, and gives up
// quietly: a run that will not stop is hermes's to finish.
//
// The requests are on a context of their own, the caller's having been
// cancelled. A stop that fails is logged and changes nothing for the
// caller, whose turn has failed already.
func (c *Client) stopRun(run string) {
	path := "/v1/runs/" + url.PathEscape(run)
	status := func(resp *http.Response) string {
		defer resp.Body.Close()
		var out struct {
			Status string `json:"status"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		return out.Status
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	resp, err := c.request(ctx, http.MethodPost, path+"/stop", nil)
	cancel()
	if isCode(err, "run_not_found") || isCode(err, "run_not_active") {
		// Forgotten, or with no agent to interrupt: not started on one
		// yet, which the closed stream ends at hermes's next write, or
		// just finished with one.
		return
	}
	if err != nil {
		c.logf("hermes: stopping run %s: %v", run, err)
		return
	}
	if runOver(status(resp)) {
		return
	}
	ctx, cancel = context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()
	tick := time.NewTicker(settlePoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			c.logf("hermes: run %s was still stopping after %s", run, settleTimeout)
			return
		case <-tick.C:
		}
		resp, err := c.request(ctx, http.MethodGet, path, nil)
		if isCode(err, "run_not_found") {
			return
		}
		if err != nil {
			if ctx.Err() == nil {
				c.logf("hermes: waiting for run %s to stop: %v", run, err)
			}
			return
		}
		if runOver(status(resp)) {
			return
		}
	}
}

// runOver is whether a run's status is one hermes's runs end in.
func runOver(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "interrupted":
		return true
	}
	return false
}

func (c *Client) logf(format string, a ...any) {
	if c.Logf != nil {
		c.Logf(format, a...)
	}
}

// sseEvent is the union of the payload fields haunt reads.
type sseEvent struct {
	SessionID      string `json:"session_id"`
	RunID          string `json:"run_id"`
	Delta          string `json:"delta"`
	Text           string `json:"text"`
	ToolName       string `json:"tool_name"`
	Content        string `json:"content"`
	Message        string `json:"message"`
	TurnExitReason string `json:"turn_exit_reason"`
	// Unfinished is completed: false on assistant.completed or a run.*
	// event; an event without the field is not unfinished.
	Unfinished bool
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
		RunID:          str("run_id"),
		Delta:          str("delta"),
		Text:           str("text"),
		ToolName:       str("tool_name"),
		Content:        str("content"),
		Message:        str("message"),
		TurnExitReason: str("turn_exit_reason"),
	}
	var done *bool
	if json.Unmarshal(raw["completed"], &done) == nil && done != nil {
		e.Unfinished = !*done
	}
	return nil
}

// providerSaidMark opens the line hermes ends a failed turn's notice with,
// the provider's own error, summarised: "Provider said: Connection
// error." (agent/turn_failure_copy.py, exhausted_copy, nonretryable_copy
// and content_policy_copy, each f"…\n\nProvider said: {summary}"; the
// summary is agent/api_error_summary.py _summarize_api_error). Hermes may
// add a paragraph of advice after it.
const providerSaidMark = "\n\nProvider said: "

// providerSaid is the provider's error from a failed turn's notice, or ""
// when the notice has none. The rest of the notice is advice for hermes's
// own chat (send /retry, switch with /model, `hermes fallback add`) and
// means nothing in haunt.
func providerSaid(notice string) string {
	_, said, ok := strings.Cut(notice, providerSaidMark)
	if !ok {
		return ""
	}
	said, _, _ = strings.Cut(said, "\n")
	said = strings.TrimSuffix(strings.TrimSpace(said), ".")
	return clip(said, 200)
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
// first, for `haunt history`. The session is read whole: the turns hermes
// compacted out of the model's working context are part of what was said,
// and so are the sessions a compression ended before this one; hermes
// gives back both. Its compaction handoffs are not, and come back as
// hidden rows that are skipped. Tool traffic is not an entry of its own: it
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
	// include_compacted adds the turns an in-place compaction archived,
	// which is how hermes compresses by default; without it the history
	// begins at the last compaction.
	q := url.Values{"order": {"latest"}, "include_compacted": {"true"}}
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
			Timestamp   float64 `json:"timestamp"`
			DisplayKind string  `json:"display_kind"`
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
	prev := "" // the role of the last row not hidden
	for _, m := range out.Data {
		if m.DisplayKind == "hidden" {
			// A compaction handoff or a control row, said by nobody. A
			// handoff is a user row and can fall inside a turn, between a
			// lookup and the reply it led to, so it must not end the turn
			// as the person's words would.
			continue
		}
		afterTool := prev == "tool"
		prev = m.Role
		text := rawText(m.Content)
		interrupted := afterTool && m.Role == "assistant" && m.DisplayKind == "" && len(m.ToolCalls) == 0 && interruptNotice(text)
		if m.DisplayKind == "failed_turn" || interrupted {
			// Hermes's notice, "Your request was not processed…", written
			// in the ghost's place when a turn failed, or the interrupt's
			// own words closing a turn stopped after a tool. The turn's
			// calls go on it, as they would on a reply.
			last = len(msgs)
			msgs = append(msgs, Message{Role: "assistant", Failed: true, Time: unixTime(m.Timestamp)})
			continue
		}
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
		if m.Role == "assistant" {
			// The pose a reply opened with was worn, not said.
			_, text = CutPose(text)
		}
		msg := Message{Role: m.Role, Text: text, Time: unixTime(m.Timestamp)}
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

// interruptNotices open the rows hermes closes an interrupted turn with
// when its last row is a tool result: a plain assistant row, with no
// display_kind, whose content is the interrupt's own text
// (agent/message_sanitization.py, close_interrupted_tool_sequence, called
// from agent/turn_recovery.py abort_turn_on_interrupt and from
// agent/turn_finalizer.py _close_transcript_tail). The texts, by where
// hermes writes them:
//
//   - "Operation interrupted: waiting for the provider to recover (cycle
//     1/2)." agent/turn_recovery_autorecover.py
//   - "Operation interrupted: handling API error (…)." and "Operation
//     interrupted: retrying API call after error (retry 1/3)."
//     agent/turn_api_error.py
//   - "Operation interrupted: retrying empty response from model (retry
//     1/3)." agent/turn_empty_response.py
//   - "Operation interrupted: waiting for model response (4.2s
//     elapsed)." INTERRUPT_WAITING_FOR_MODEL_PREFIX in
//     agent/conversation_loop.py, used by agent/turn_api_call.py
//     handle_api_interrupt and agent/turn_finalizer.py
//   - "Operation interrupted during retry (…, attempt 1/3)."
//     agent/turn_response_check.py
//
// An interrupt the closed stream causes and one POST
// /v1/runs/{run_id}/stop causes write the same rows. A turn interrupted
// before any tool ran gets a failed_turn row instead, one with no text
// at all gets a hidden row, and one interrupted while the model was
// writing keeps the words it had written, which are the ghost's.
//
// The match is on hermes's wording, and is rechecked when the pin moves
// (docs/dev/hermes.md, Moving the pin).
var interruptNotices = []string{
	"Operation interrupted: ",
	"Operation interrupted during retry (",
}

// interruptNotice is whether an assistant row's text is hermes's
// interrupt notice rather than the ghost's words. Only a row straight
// after a tool result can be one, which keeps a reply that happens to
// open the same way the ghost's.
func interruptNotice(text string) bool {
	for _, p := range interruptNotices {
		if strings.HasPrefix(strings.TrimSpace(text), p) {
			return true
		}
	}
	return false
}

// unixTime is a hermes timestamp, seconds since the epoch as a float.
func unixTime(ts float64) time.Time {
	sec := int64(ts)
	return time.Unix(sec, int64((ts-float64(sec))*1e9))
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
