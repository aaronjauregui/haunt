package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Chat is the Client for one Config: POST <BaseURL>/chat/completions with
// stream on, read as SSE.
//
// A call that the server answers 429 or 5xx is tried again, three
// attempts in all, a second and then two apart, or after the Retry-After
// the server names when it names one. A Retry-After over a minute ends the
// tries at once: a provider that says so is out of quota for longer than a
// person waits for a ghost, and the error says when to come back. Nothing
// else is retried; a request the server refuses is refused again.
//
// Each attempt runs two deadlines, one after the other (Config.FirstToken
// and Config.Stall): the first from sending the request to its first sign
// of life, the second from every sign after it. A sign of life is a word,
// reasoning, a tool call fragment, the finish or the usage; SSE comments
// and a chunk carrying only the role are not, since a server sends those
// before the model has done anything. The call as a whole is bounded only
// by the caller's context.
type Chat struct {
	Config Config
	// HTTP defaults to a client with no overall timeout: the deadlines
	// and the caller's context bound a call, and a long answer is not a
	// slow one.
	HTTP *http.Client

	// wait sleeps between attempts; tests replace it to see the backoff
	// without spending it.
	wait func(ctx context.Context, d time.Duration) error
}

var _ Client = (*Chat)(nil)

// New is a Chat for cfg.
func New(cfg Config) *Chat {
	return &Chat{Config: cfg}
}

// Retry policy.
const (
	attempts      = 3
	backoffBase   = time.Second
	maxRetryAfter = 60 * time.Second
)

// APIError is a non-2xx answer from the server.
type APIError struct {
	Status  int
	Message string
	// RetryAfter is what the server asked for, zero when it did not.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("model: %d %s", e.Status, http.StatusText(e.Status))
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.RetryAfter > 0 {
		msg += fmt.Sprintf(" (retry after %s)", e.RetryAfter)
	}
	return msg
}

// retryable is whether an answer with this status may succeed when sent
// again unchanged.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// DeadlineError ends a call that went quiet.
type DeadlineError struct {
	// First is true when nothing had come back at all, false when the
	// stream had started and then stopped.
	First bool
	After time.Duration
}

func (e *DeadlineError) Error() string {
	if e.First {
		return fmt.Sprintf("model: no answer within %s", e.After)
	}
	return fmt.Sprintf("model: the answer stopped for %s", e.After)
}

// Stream sends r and returns its reply as events. The request is sent,
// and retried, before Stream returns, so an error here is the server's
// refusal or no server at all; an error after that arrives as an
// EventError. The channel ends with exactly one EventFinish or EventError
// and is then closed. The caller reads it to the end or cancels ctx.
func (c *Chat) Stream(ctx context.Context, r Request) (<-chan Event, error) {
	body, err := json.Marshal(c.body(r))
	if err != nil {
		return nil, fmt.Errorf("model: encoding the request: %w", err)
	}
	endpoint := strings.TrimRight(c.Config.BaseURL, "/") + "/chat/completions"
	for attempt := 1; ; attempt++ {
		actx, cancel := context.WithCancelCause(ctx)
		w := watch(cancel, c.Config.FirstToken(), c.Config.Stall())
		req, err := http.NewRequestWithContext(actx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			w.stop()
			cancel(nil)
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		if c.Config.APIKeyEnv != "" {
			if key := os.Getenv(c.Config.APIKeyEnv); key != "" {
				req.Header.Set("Authorization", "Bearer "+key)
			}
		}
		resp, err := c.client().Do(req)
		if err != nil {
			w.stop()
			cause := context.Cause(actx)
			cancel(nil)
			var de *DeadlineError
			if errors.As(cause, &de) {
				return nil, de
			}
			return nil, fmt.Errorf("model: %w", err)
		}
		if resp.StatusCode/100 == 2 {
			ch := make(chan Event, 16)
			go func() {
				defer close(ch)
				defer cancel(nil)
				defer w.stop()
				defer resp.Body.Close()
				read(ctx, actx, resp.Body, w, ch)
			}()
			return ch, nil
		}
		apiErr := readAPIError(resp)
		resp.Body.Close()
		w.stop()
		cancel(nil)
		if !retryable(apiErr.Status) || attempt == attempts || apiErr.RetryAfter > maxRetryAfter {
			return nil, apiErr
		}
		delay := backoffBase << (attempt - 1)
		if apiErr.RetryAfter > 0 {
			delay = apiErr.RetryAfter
		}
		if err := c.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func (c *Chat) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Chat) sleep(ctx context.Context, d time.Duration) error {
	if c.wait != nil {
		return c.wait(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// readAPIError reads a refusal. Providers put the message in more than
// one place; whichever is there is taken, else the body as text.
func readAPIError(resp *http.Response) *APIError {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	e := &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(raw))}
	var body struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(raw, &body) == nil {
		if m := errorMessage(body.Error); m != "" {
			e.Message = m
		} else if body.Message != "" {
			e.Message = body.Message
		}
	}
	if len(e.Message) > 500 {
		e.Message = e.Message[:500] + "…"
	}
	e.RetryAfter = retryAfter(resp.Header.Get("Retry-After"), time.Now())
	return e
}

// errorMessage is the text of an "error" field, a string or an object
// with a message.
func errorMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Message
	}
	return ""
}

// retryAfter reads a Retry-After header, seconds or an HTTP date; zero
// when absent or unreadable.
func retryAfter(h string, now time.Time) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if s, err := strconv.Atoi(h); err == nil {
		if s < 0 {
			return 0
		}
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// body is the request as JSON: the messages, a trailing assistant message
// for the prefill, the tools, sampling and thinking.
func (c *Chat) body(r Request) map[string]any {
	msgs := make([]wireMessage, 0, len(r.Messages)+1)
	for _, m := range r.Messages {
		msgs = append(msgs, toWire(m))
	}
	if r.Prefill != "" {
		p := r.Prefill
		msgs = append(msgs, wireMessage{Role: "assistant", Content: &p})
	}
	b := map[string]any{
		"messages":       msgs,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if c.Config.Name != "" {
		b["model"] = c.Config.Name
	}
	if len(r.Tools) > 0 {
		tools := make([]map[string]any, 0, len(r.Tools))
		for _, t := range r.Tools {
			schema := t.Schema
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  schema,
				},
			})
		}
		b["tools"] = tools
	}
	if len(r.Stop) > 0 {
		b["stop"] = r.Stop
	}
	s := c.Config.Sampling.Over(r.Sampling)
	for key, v := range map[string]*float64{
		"temperature":     s.Temperature,
		"top_p":           s.TopP,
		"min_p":           s.MinP,
		"repeat_penalty":  s.RepeatPenalty,
		"dry_multiplier":  s.DRYMultiplier,
		"xtc_probability": s.XTCProbability,
	} {
		if v != nil {
			b[key] = *v
		}
	}
	// Thinking is switched off two ways at once because providers differ
	// on which they read: OpenAI-style servers take reasoning_effort, and
	// llama.cpp hands chat_template_kwargs to the model's template, which
	// is where Gemma's and Qwen's switches are.
	switch {
	case !r.Thinking || c.Config.Thinking == ThinkingOff:
		b["reasoning_effort"] = "none"
		b["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
	case c.Config.Thinking != "":
		b["reasoning_effort"] = c.Config.Thinking
	}
	return b
}

// wireMessage is a Message as the API spells it.
type wireMessage struct {
	Role string `json:"role"`
	// Content is null on an assistant message that is only tool calls,
	// which is what servers expect there.
	Content    *string    `json:"content"`
	ToolCalls  []wireCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type wireCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func toWire(m Message) wireMessage {
	w := wireMessage{Role: m.Role, ToolCallID: m.ToolCallID, Name: m.Name}
	if m.Content != "" || len(m.ToolCalls) == 0 {
		content := m.Content
		w.Content = &content
	}
	for _, tc := range m.ToolCalls {
		call := wireCall{ID: tc.ID, Type: "function"}
		call.Function.Name = tc.Name
		call.Function.Arguments = string(tc.Args)
		if len(tc.Args) == 0 {
			call.Function.Arguments = "{}"
		}
		w.ToolCalls = append(w.ToolCalls, call)
	}
	return w
}

// chunk is one SSE data frame of a streamed completion.
type chunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          *string `json:"content"`
			ReasoningContent string  `json:"reasoning_content"`
			Reasoning        string  `json:"reasoning"`
			ToolCalls        []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

// pendingCall is a tool call whose fragments are still arriving.
type pendingCall struct {
	id, name string
	args     bytes.Buffer
}

// read turns the SSE body into events. ctx is the caller's, actx the
// attempt's, which the deadlines cancel.
func read(ctx, actx context.Context, body io.Reader, w *watchdog, ch chan<- Event) {
	send := func(ev Event) bool {
		select {
		case ch <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	fail := func(err error) {
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if cause := context.Cause(actx); cause != nil {
			var de *DeadlineError
			if errors.As(cause, &de) {
				err = de
			}
		}
		send(Event{Kind: EventError, Err: err})
	}

	var (
		calls   []*pendingCall
		byIndex = map[int]*pendingCall{}
		usage   *Usage
		finish  *string
		done    bool
		failed  bool
	)
	flushCalls := func() bool {
		for i, pc := range calls {
			args := bytes.TrimSpace(pc.args.Bytes())
			if len(args) == 0 {
				args = []byte("{}")
			}
			id := pc.id
			if id == "" {
				id = "call_" + strconv.Itoa(i)
			}
			tc := &ToolCall{ID: id, Name: pc.name, Args: json.RawMessage(bytes.Clone(args))}
			if !send(Event{Kind: EventToolCall, ToolCall: tc}) {
				return false
			}
		}
		calls = nil
		return true
	}

	err := readSSE(body, func(data []byte) bool {
		if string(bytes.TrimSpace(data)) == "[DONE]" {
			done = true
			return false
		}
		var c chunk
		if err := json.Unmarshal(data, &c); err != nil {
			// A frame that is not a chunk is a server's own notice; the
			// stream goes on.
			return true
		}
		if len(c.Error) > 0 && string(c.Error) != "null" {
			m := errorMessage(c.Error)
			if m == "" {
				m = string(c.Error)
			}
			failed = true
			fail(fmt.Errorf("model: the server ended the answer: %s", m))
			return false
		}
		if c.Usage != nil {
			w.alive()
			usage = &Usage{PromptTokens: c.Usage.PromptTokens, CompletionTokens: c.Usage.CompletionTokens}
		}
		for _, choice := range c.Choices {
			if choice.Index != 0 {
				continue
			}
			d := choice.Delta
			if d.ReasoningContent != "" || d.Reasoning != "" {
				w.alive()
			}
			if d.Content != nil && *d.Content != "" {
				w.alive()
				if !send(Event{Kind: EventDelta, Delta: *d.Content}) {
					return false
				}
			}
			for _, f := range d.ToolCalls {
				w.alive()
				idx := len(calls)
				if f.Index != nil {
					idx = *f.Index
				}
				pc := byIndex[idx]
				// Some servers number every call 0 and tell them apart by
				// id alone.
				if pc == nil || f.ID != "" && pc.id != "" && f.ID != pc.id {
					pc = &pendingCall{}
					byIndex[idx] = pc
					calls = append(calls, pc)
				}
				if f.ID != "" {
					pc.id = f.ID
				}
				if f.Function.Name != "" {
					pc.name = f.Function.Name
				}
				appendArgs(&pc.args, f.Function.Arguments)
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				w.alive()
				reason := *choice.FinishReason
				finish = &reason
				if !flushCalls() {
					return false
				}
			}
		}
		return true
	})
	if ctx.Err() != nil || failed {
		return
	}
	if err != nil {
		fail(fmt.Errorf("model: reading the answer: %w", err))
		return
	}
	if cause := context.Cause(actx); cause != nil {
		fail(cause)
		return
	}
	if !done && finish == nil {
		fail(errors.New("model: the answer ended before it was finished"))
		return
	}
	if !flushCalls() {
		return
	}
	if usage != nil && !send(Event{Kind: EventUsage, Usage: usage}) {
		return
	}
	reason := ""
	if finish != nil {
		reason = *finish
	}
	send(Event{Kind: EventFinish, Finish: reason})
}

// appendArgs adds a fragment of a call's arguments: a string, as the API
// specifies, or the arguments as an object, as some servers send whole.
func appendArgs(buf *bytes.Buffer, raw json.RawMessage) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			buf.WriteString(s)
		}
		return
	}
	buf.Write(raw)
}

// readSSE calls fn with each event's data until fn returns false or the
// stream ends. Event names and comments are skipped: a completion stream
// carries everything in its data.
func readSSE(r io.Reader, fn func(data []byte) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	var data bytes.Buffer
	have := false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if have {
				if !fn(data.Bytes()) {
					return nil
				}
			}
			data.Reset()
			have = false
		case strings.HasPrefix(line, "data:"):
			if have {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			have = true
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if have {
		fn(data.Bytes())
	}
	return nil
}

// watchdog runs an attempt's two deadlines. Signs of life only record the
// time; the timer, when it fires, looks at how long it has been and
// either sleeps the rest of the way or cancels the attempt, so a stream
// of tokens costs no timer work per token.
type watchdog struct {
	cancel       context.CancelCauseFunc
	first, stall time.Duration

	mu      sync.Mutex
	timer   *time.Timer
	start   time.Time
	last    time.Time
	stirred bool
	done    bool
}

// watch starts the first deadline.
func watch(cancel context.CancelCauseFunc, first, stall time.Duration) *watchdog {
	w := &watchdog{cancel: cancel, first: first, stall: stall, start: time.Now()}
	w.mu.Lock()
	w.timer = time.AfterFunc(first, w.check)
	w.mu.Unlock()
	return w
}

// alive records a sign of life.
func (w *watchdog) alive() {
	w.mu.Lock()
	w.stirred = true
	w.last = time.Now()
	w.mu.Unlock()
}

func (w *watchdog) check() {
	w.mu.Lock()
	if w.done {
		w.mu.Unlock()
		return
	}
	limit, from := w.first, w.start
	if w.stirred {
		limit, from = w.stall, w.last
	}
	if rest := limit - time.Since(from); rest > 0 {
		w.timer.Reset(rest)
		w.mu.Unlock()
		return
	}
	w.done = true
	first := !w.stirred
	w.mu.Unlock()
	w.cancel(&DeadlineError{First: first, After: limit})
}

// stop ends both deadlines.
func (w *watchdog) stop() {
	w.mu.Lock()
	w.done = true
	w.timer.Stop()
	w.mu.Unlock()
}
