// Package fake is a scripted chat completions server for tests: it answers
// POST /v1/chat/completions in the OpenAI SSE format the model client
// reads, so anything built on internal/model is tested against the wire
// rather than a mock of the client.
//
// The "model" is a list of rules. The first rule that matches a request
// answers it: Match is looked for in the last user message, and AfterTool
// decides whether the rule answers a fresh turn or the round after a tool
// result, so one script carries a whole decide loop. Every request body is
// kept, in order, for the test to read back.
//
// The package does not import internal/model, so the model package's own
// tests can use it.
package fake

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Rule is one scripted answer.
type Rule struct {
	// Match is looked for, case-insensitively, in the last user message;
	// "" matches any.
	Match string `json:"match"`
	// AfterTool restricts the rule by the request's last message. Empty:
	// the rule answers only when the last message is not a tool result.
	// "*": only when it is one. Anything else: only when it is a tool
	// result containing this text, case-insensitively.
	AfterTool string `json:"after_tool"`
	// Status, when not 0 or 200, answers with this status and an OpenAI
	// error body carrying Reply as its message, and nothing else.
	Status int `json:"status"`
	// RetryAfter is sent as the Retry-After header of a Status answer.
	RetryAfter string `json:"retry_after"`
	// DelayMS is silence after the headers and before the first frame,
	// for a model that takes its time.
	DelayMS int `json:"delay_ms"`
	// GapMS is silence between frames.
	GapMS int `json:"gap_ms"`
	// Reasoning is streamed as reasoning_content before the reply.
	Reasoning string `json:"reasoning"`
	// Reply is the words, streamed a word at a time. With Calls it is the
	// text the model writes beside them.
	Reply string `json:"reply"`
	// Calls are tool calls made after Reply, each one's arguments split
	// over ArgFrames frames.
	Calls []Call `json:"calls"`
	// ArgFrames is how many frames a call's arguments are split over;
	// 0 is 3.
	ArgFrames int `json:"arg_frames"`
	// Finish overrides the finish reason: "stop", or "tool_calls" when
	// there are calls.
	Finish string `json:"finish"`
	// NoUsage leaves out the usage chunk.
	NoUsage bool `json:"no_usage"`
	// Cut ends the stream after the reply and calls with no finish and no
	// [DONE], as a server that died mid-answer.
	Cut bool `json:"cut"`
	// EchoPrefill puts the request's prefill (a trailing assistant
	// message) back at the start of the first content delta, as
	// llama-server does.
	EchoPrefill bool `json:"echo_prefill"`
}

// Call is one scripted tool call.
type Call struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Received is one request as the server saw it.
type Received struct {
	Header http.Header
	Body   []byte
}

// Server is a running fake.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	rules    []Rule
	received []Received
}

// New starts a fake answering with rules. The caller closes it; with a
// test at hand, NewT does that on cleanup.
func New(rules ...Rule) *Server {
	s := &Server{rules: rules}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Cleaner is the part of testing.TB that NewT needs.
type Cleaner interface{ Cleanup(func()) }

// NewT is New closed when the test ends.
func NewT(t Cleaner, rules ...Rule) *Server {
	s := New(rules...)
	t.Cleanup(s.Close)
	return s
}

// BaseURL is what goes in [model] base_url to reach this server.
func (s *Server) BaseURL() string { return s.URL + "/v1" }

// Script replaces the rules.
func (s *Server) Script(rules ...Rule) {
	s.mu.Lock()
	s.rules = rules
	s.mu.Unlock()
}

// Received is every request so far, oldest first.
func (s *Server) Received() []Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Received(nil), s.received...)
}

// Bodies is every request body so far, oldest first.
func (s *Server) Bodies() [][]byte {
	var out [][]byte
	for _, r := range s.Received() {
		out = append(out, r.Body)
	}
	return out
}

// Last is the latest request body decoded, or nil before any.
func (s *Server) Last() map[string]any {
	rs := s.Received()
	if len(rs) == 0 {
		return nil
	}
	var m map[string]any
	json.Unmarshal(rs[len(rs)-1].Body, &m)
	return m
}

// request is the part of a body the rules look at.
type request struct {
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	StreamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// pick is the first rule that answers req, and whether there was one.
func (s *Server) pick(req request) (Rule, bool) {
	msgs := req.Messages
	if n := len(msgs); n > 0 && msgs[n-1].Role == "assistant" {
		msgs = msgs[:n-1] // a prefill
	}
	// The trailing tool results, all of them: a round may call several.
	var tool strings.Builder
	i := len(msgs) - 1
	for ; i >= 0 && msgs[i].Role == "tool"; i-- {
		tool.WriteString(text(msgs[i].Content))
		tool.WriteByte('\n')
	}
	isTool := i < len(msgs)-1
	var user string
	for ; i >= 0; i-- {
		if msgs[i].Role == "user" {
			user = text(msgs[i].Content)
			break
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if !strings.Contains(strings.ToLower(user), strings.ToLower(r.Match)) {
			continue
		}
		switch r.AfterTool {
		case "":
			if isTool {
				continue
			}
		case "*":
			if !isTool {
				continue
			}
		default:
			if !isTool || !strings.Contains(strings.ToLower(tool.String()), strings.ToLower(r.AfterTool)) {
				continue
			}
		}
		return r, true
	}
	return Rule{}, false
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.received = append(s.received, Received{Header: r.Header.Clone(), Body: body})
	s.mu.Unlock()

	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "", "the body is not JSON: "+err.Error())
		return
	}
	rule, _ := s.pick(req)
	if rule.Status != 0 && rule.Status != http.StatusOK {
		writeError(w, rule.Status, rule.RetryAfter, rule.Reply)
		return
	}
	prefill := ""
	if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == "assistant" {
		prefill = text(req.Messages[n-1].Content)
	}
	stream(w, r, rule, req.StreamOptions.IncludeUsage, len(body), prefill)
}

func writeError(w http.ResponseWriter, status int, retryAfter, msg string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "code": status}})
}

// stream writes rule as a chat completions SSE stream.
func stream(w http.ResponseWriter, r *http.Request, rule Rule, usage bool, size int, prefill string) {
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if fl != nil {
		fl.Flush()
	}
	ctx := r.Context()
	pause := func(ms int) bool {
		if ms <= 0 {
			return true
		}
		select {
		case <-time.After(time.Duration(ms) * time.Millisecond):
			return true
		case <-ctx.Done():
			return false
		}
	}
	frames := 0
	send := func(delta map[string]any, finish any) bool {
		if frames > 0 && !pause(rule.GapMS) {
			return false
		}
		frames++
		c := map[string]any{
			"id":      "chatcmpl-fake",
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   "fake",
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		b, _ := json.Marshal(c)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if fl != nil {
			fl.Flush()
		}
		return ctx.Err() == nil
	}
	if !pause(rule.DelayMS) {
		return
	}
	if !send(map[string]any{"role": "assistant", "content": nil}, nil) {
		return
	}
	words := 0
	if rule.Reasoning != "" && !send(map[string]any{"reasoning_content": rule.Reasoning}, nil) {
		return
	}
	pieces := splitWords(rule.Reply)
	if rule.EchoPrefill && prefill != "" {
		if len(pieces) == 0 {
			pieces = []string{""}
		}
		pieces[0] = prefill + pieces[0]
	}
	for _, piece := range pieces {
		words++
		if !send(map[string]any{"content": piece}, nil) {
			return
		}
	}
	for i, call := range rule.Calls {
		id := call.ID
		if id == "" {
			id = fmt.Sprintf("call_fake_%d", i)
		}
		args := string(call.Args)
		if args == "" {
			args = "{}"
		}
		n := rule.ArgFrames
		if n <= 0 {
			n = 3
		}
		parts := splitN(args, n)
		first := map[string]any{"index": i, "id": id, "type": "function",
			"function": map[string]any{"name": call.Name, "arguments": ""}}
		if !send(map[string]any{"tool_calls": []any{first}}, nil) {
			return
		}
		for _, p := range parts {
			frag := map[string]any{"index": i, "function": map[string]any{"arguments": p}}
			if !send(map[string]any{"tool_calls": []any{frag}}, nil) {
				return
			}
		}
	}
	if rule.Cut {
		return
	}
	finish := rule.Finish
	if finish == "" {
		finish = "stop"
		if len(rule.Calls) > 0 {
			finish = "tool_calls"
		}
	}
	if !send(map[string]any{}, finish) {
		return
	}
	if usage && !rule.NoUsage {
		b, _ := json.Marshal(map[string]any{
			"id": "chatcmpl-fake", "object": "chat.completion.chunk", "model": "fake",
			"choices": []any{},
			"usage":   map[string]any{"prompt_tokens": size / 4, "completion_tokens": words, "total_tokens": size/4 + words},
		})
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if fl != nil {
		fl.Flush()
	}
}

// splitWords cuts s into words with their trailing spaces, so the pieces
// join back to s.
func splitWords(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, ' ')
		if i < 0 {
			out = append(out, s)
			break
		}
		j := i
		for j < len(s) && s[j] == ' ' {
			j++
		}
		out = append(out, s[:j])
		s = s[j:]
	}
	return out
}

// splitN cuts s into n pieces of about equal length (fewer when s is
// shorter than n bytes); they join back to s.
func splitN(s string, n int) []string {
	if n > len(s) {
		n = len(s)
	}
	if n <= 1 {
		return []string{s}
	}
	out := make([]string, 0, n)
	size := len(s) / n
	for i := 0; i < n-1; i++ {
		out = append(out, s[:size])
		s = s[size:]
	}
	return append(out, s)
}
