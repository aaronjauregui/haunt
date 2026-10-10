package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/model/fake"
)

// collect reads a stream to its end.
func collect(t *testing.T, ch <-chan Event) (words string, calls []ToolCall, usage *Usage, finish string, err error) {
	t.Helper()
	var b strings.Builder
	for ev := range ch {
		switch ev.Kind {
		case EventDelta:
			b.WriteString(ev.Delta)
		case EventToolCall:
			calls = append(calls, *ev.ToolCall)
		case EventUsage:
			usage = ev.Usage
		case EventFinish:
			finish = ev.Finish
		case EventError:
			err = ev.Err
		}
	}
	return b.String(), calls, usage, finish, err
}

func user(text string) []Message { return []Message{{Role: "user", Content: text}} }

func stream(t *testing.T, c *Chat, r Request) (string, []ToolCall, *Usage, string) {
	t.Helper()
	ch, err := c.Stream(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	words, calls, usage, finish, err := collect(t, ch)
	if err != nil {
		t.Fatal(err)
	}
	return words, calls, usage, finish
}

func TestStreamToolCall(t *testing.T) {
	srv := fake.NewT(t, fake.Rule{
		Match: "the oven",
		Reply: "Let me note that.",
		Calls: []fake.Call{{ID: "call_1", Name: "intend", Args: json.RawMessage(`{"text":"check the oven","in":"10m"}`)}},
		// After the frame naming the call, its arguments in three
		// (fake's TestArgFrames shows the split).
		ArgFrames: 3,
	})
	c := New(Config{BaseURL: srv.BaseURL()})
	words, calls, usage, finish := stream(t, c, Request{
		Messages: user("remind me about the oven"),
		Tools:    []Tool{{Name: "intend", Description: "keep a reminder", Schema: json.RawMessage(`{"type":"object"}`)}},
	})
	if words != "Let me note that." {
		t.Errorf("words = %q", words)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want one", calls)
	}
	tc := calls[0]
	if tc.ID != "call_1" || tc.Name != "intend" || string(tc.Args) != `{"text":"check the oven","in":"10m"}` {
		t.Errorf("call = %s %s %s", tc.ID, tc.Name, tc.Args)
	}
	if finish != "tool_calls" {
		t.Errorf("finish = %q", finish)
	}
	if usage == nil || usage.PromptTokens == 0 || usage.CompletionTokens == 0 {
		t.Errorf("usage = %+v", usage)
	}
	body := srv.Last()
	fn := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "intend" || fn["parameters"].(map[string]any)["type"] != "object" {
		t.Errorf("tool sent as %v", fn)
	}
}

// The fragments are joined however a server numbers and spells them: by
// index across frames, an id alone telling calls apart, and arguments as
// an object rather than a string.
func TestToolCallShapes(t *testing.T) {
	frames := []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"face","arguments":"{\"po"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"se\":\"happy\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"b","function":{"name":"clip","arguments":{"name":"wave"}}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"c","function":{"name":"stay_silent","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range frames {
			fmt.Fprintf(w, ": keepalive\n\ndata: %s\n\n", f)
		}
	}))
	defer srv.Close()
	_, calls, _, _ := stream(t, New(Config{BaseURL: srv.URL}), Request{Messages: user("hi")})
	want := []string{`a face {"pose":"happy"}`, `b clip {"name":"wave"}`, `c stay_silent {}`}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v", calls)
	}
	for i, tc := range calls {
		if got := tc.ID + " " + tc.Name + " " + string(tc.Args); got != want[i] {
			t.Errorf("call %d = %s, want %s", i, got, want[i])
		}
	}
}

func TestPrefill(t *testing.T) {
	srv := fake.NewT(t, fake.Rule{Reply: "it's raining again."})
	c := New(Config{BaseURL: srv.BaseURL()})
	words, _, _, finish := stream(t, c, Request{
		Messages: []Message{{Role: "system", Content: "You are wisp."}, {Role: "user", Content: "weather?"}},
		Prefill:  "Well, ",
	})
	var body struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(srv.Bodies()[0], &body); err != nil {
		t.Fatal(err)
	}
	last := string(body.Messages[len(body.Messages)-1])
	if last != `{"role":"assistant","content":"Well, "}` {
		t.Errorf("last message = %s", last)
	}
	// The deltas continue the prefill, which the caller already has.
	if got := "Well, " + words; got != "Well, it's raining again." {
		t.Errorf("prefill + reply = %q", got)
	}
	if finish != "stop" {
		t.Errorf("finish = %q", finish)
	}
}

func TestThinkingOff(t *testing.T) {
	srv := fake.NewT(t, fake.Rule{Reply: "ok"})
	for _, tc := range []struct {
		name     string
		cfg      string
		thinking bool
		effort   any // nil: absent
		kwargs   bool
	}{
		{"request off", "", false, "none", true},
		{"config off", ThinkingOff, true, "none", true},
		{"provider default", "", true, nil, false},
		{"level", ThinkingHigh, true, "high", false},
	} {
		c := New(Config{BaseURL: srv.BaseURL(), Thinking: tc.cfg})
		stream(t, c, Request{Messages: user("hi"), Thinking: tc.thinking})
		body := srv.Last()
		if body["reasoning_effort"] != tc.effort {
			t.Errorf("%s: reasoning_effort = %v, want %v", tc.name, body["reasoning_effort"], tc.effort)
		}
		kw, ok := body["chat_template_kwargs"].(map[string]any)
		if ok != tc.kwargs {
			t.Errorf("%s: chat_template_kwargs = %v", tc.name, body["chat_template_kwargs"])
		}
		if ok && kw["enable_thinking"] != false {
			t.Errorf("%s: enable_thinking = %v, want false", tc.name, kw["enable_thinking"])
		}
	}
}

func TestSampling(t *testing.T) {
	srv := fake.NewT(t, fake.Rule{Reply: "ok"})
	f := func(v float64) *float64 { return &v }
	c := New(Config{BaseURL: srv.BaseURL(), Name: "gemma-4", Sampling: Sampling{
		Temperature: f(0.7), TopP: f(0.9), MinP: f(0.05), RepeatPenalty: f(1.1),
		DRYMultiplier: f(0.8), XTCProbability: f(0.5),
	}})
	stream(t, c, Request{Messages: user("hi"), Sampling: Sampling{Temperature: f(1.2)}, Stop: []string{"\n\n"}})
	body := srv.Last()
	want := map[string]any{
		"model": "gemma-4", "temperature": 1.2, "top_p": 0.9, "min_p": 0.05,
		"repeat_penalty": 1.1, "dry_multiplier": 0.8, "xtc_probability": 0.5, "stream": true,
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}
	if stop, _ := body["stop"].([]any); len(stop) != 1 || stop[0] != "\n\n" {
		t.Errorf("stop = %v", body["stop"])
	}
	// Nothing set is nothing sent.
	c = New(Config{BaseURL: srv.BaseURL()})
	stream(t, c, Request{Messages: user("hi")})
	for _, k := range []string{"model", "temperature", "top_p", "min_p", "repeat_penalty", "dry_multiplier", "xtc_probability", "tools", "stop"} {
		if v, ok := srv.Last()[k]; ok {
			t.Errorf("%s sent unset as %v", k, v)
		}
	}
}

// A tool round goes back as the API spells it: the call on an assistant
// message with null content, its arguments a string, the result keyed to
// its id.
func TestToolRoundWire(t *testing.T) {
	srv := fake.NewT(t,
		fake.Rule{Match: "time", AfterTool: "14:02", Reply: "Two o'clock."},
		fake.Rule{Match: "time", Calls: []fake.Call{{Name: "clock"}}},
	)
	c := New(Config{BaseURL: srv.BaseURL()})
	msgs := user("what time is it")
	_, calls, _, _ := stream(t, c, Request{Messages: msgs})
	if len(calls) != 1 || calls[0].Name != "clock" || string(calls[0].Args) != "{}" {
		t.Fatalf("calls = %+v", calls)
	}
	msgs = append(msgs,
		Message{Role: "assistant", ToolCalls: calls},
		Message{Role: "tool", ToolCallID: calls[0].ID, Name: "clock", Content: "14:02"})
	words, _, _, _ := stream(t, c, Request{Messages: msgs})
	if words != "Two o'clock." {
		t.Errorf("words = %q", words)
	}
	var body struct {
		Messages []json.RawMessage `json:"messages"`
	}
	json.Unmarshal(srv.Bodies()[1], &body)
	want := []string{
		`{"role":"user","content":"what time is it"}`,
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call_fake_0","type":"function","function":{"name":"clock","arguments":"{}"}}]}`,
		`{"role":"tool","content":"14:02","tool_call_id":"call_fake_0","name":"clock"}`,
	}
	for i, w := range want {
		if i >= len(body.Messages) || string(body.Messages[i]) != w {
			t.Errorf("message %d = %s\nwant          %s", i, body.Messages[i], w)
		}
	}
}

func TestAPIKey(t *testing.T) {
	srv := fake.NewT(t, fake.Rule{Reply: "ok"})
	t.Setenv("HAUNT_TEST_MODEL_KEY", "sk-test")
	stream(t, New(Config{BaseURL: srv.BaseURL(), APIKeyEnv: "HAUNT_TEST_MODEL_KEY"}), Request{Messages: user("hi")})
	stream(t, New(Config{BaseURL: srv.BaseURL()}), Request{Messages: user("hi")})
	got := srv.Received()
	if got[0].Header.Get("Authorization") != "Bearer sk-test" {
		t.Error("the key was not sent as a bearer token")
	}
	if got[1].Header.Get("Authorization") != "" {
		t.Error("an Authorization header went out with no key configured")
	}
}

// waits records the backoff instead of sleeping it.
type waits struct {
	mu sync.Mutex
	d  []time.Duration
}

func (w *waits) wait(_ context.Context, d time.Duration) error {
	w.mu.Lock()
	w.d = append(w.d, d)
	w.mu.Unlock()
	return nil
}

func TestRetry(t *testing.T) {
	// A server failing the first n requests with status, then answering.
	failing := func(n, status int, retryAfter string) (*httptest.Server, *int) {
		count := new(int)
		ok := fake.New(fake.Rule{Reply: "ok"})
		t.Cleanup(ok.Close)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*count++
			if *count <= n {
				if retryAfter != "" {
					w.Header().Set("Retry-After", retryAfter)
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":{"message":"busy"}}`)
				return
			}
			ok.Config.Handler.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		return srv, count
	}
	for _, tc := range []struct {
		name       string
		fails      int
		status     int
		retryAfter string
		requests   int
		waits      []time.Duration
		ok         bool
	}{
		{"5xx backs off", 2, 503, "", 3, []time.Duration{time.Second, 2 * time.Second}, true},
		{"429 honours Retry-After", 1, 429, "7", 2, []time.Duration{7 * time.Second}, true},
		{"three attempts", 5, 500, "", 3, []time.Duration{time.Second, 2 * time.Second}, false},
		{"4xx is not retried", 1, 400, "", 1, nil, false},
		{"Retry-After over a minute gives up", 1, 429, "3600", 1, nil, false},
	} {
		srv, count := failing(tc.fails, tc.status, tc.retryAfter)
		var w waits
		c := New(Config{BaseURL: srv.URL})
		c.wait = w.wait
		ch, err := c.Stream(context.Background(), Request{Messages: user("hi")})
		if tc.ok {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
				continue
			}
			if words, _, _, _, err := collect(t, ch); err != nil || words != "ok" {
				t.Errorf("%s: %q, %v", tc.name, words, err)
			}
		} else {
			var ae *APIError
			if !errors.As(err, &ae) || ae.Status != tc.status || ae.Message != "busy" {
				t.Errorf("%s: err = %v, want an APIError %d busy", tc.name, err, tc.status)
			}
		}
		if *count != tc.requests {
			t.Errorf("%s: %d requests, want %d", tc.name, *count, tc.requests)
		}
		if fmt.Sprint(w.d) != fmt.Sprint(tc.waits) {
			t.Errorf("%s: waits %v, want %v", tc.name, w.d, tc.waits)
		}
	}
}

func TestRetryAfterDate(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	if got := retryAfter(now.Add(30*time.Second).Format(http.TimeFormat), now); got != 30*time.Second {
		t.Errorf("date = %v", got)
	}
	if got := retryAfter("soon", now); got != 0 {
		t.Errorf("junk = %v", got)
	}
}

func TestFirstTokenDeadline(t *testing.T) {
	t.Parallel()
	srv := fake.NewT(t, fake.Rule{DelayMS: 3000, Reply: "late"})
	c := New(Config{BaseURL: srv.BaseURL(), FirstTokenSeconds: 1})
	start := time.Now()
	ch, err := c.Stream(context.Background(), Request{Messages: user("hi")})
	if err == nil {
		_, _, _, _, err = collect(t, ch)
	}
	var de *DeadlineError
	if !errors.As(err, &de) || !de.First || de.After != time.Second {
		t.Fatalf("err = %v, want the first-token deadline", err)
	}
	if el := time.Since(start); el > 2500*time.Millisecond {
		t.Errorf("gave up after %v", el)
	}
}

func TestStallDeadline(t *testing.T) {
	t.Parallel()
	// The stall deadline is far shorter than the first-token one, and is
	// held to on its own once the stream has started. Reasoning is a sign
	// of life like a word.
	srv := fake.NewT(t, fake.Rule{Reasoning: "hmm", GapMS: 50, Reply: "one two three"})
	c := New(Config{BaseURL: srv.BaseURL()})
	c.firstToken, c.stall = 5*time.Second, 250*time.Millisecond
	words, _, _, _, err := func() (string, []ToolCall, *Usage, string, error) {
		ch, err := c.Stream(context.Background(), Request{Messages: user("hi")})
		if err != nil {
			t.Fatal(err)
		}
		return collect(t, ch)
	}()
	if err != nil || words != "one two three" {
		t.Fatalf("a steady stream under the stall deadline: %q, %v", words, err)
	}

	// The first word comes inside the first deadline, the second only
	// after the stall deadline has run out.
	srv.Script(fake.Rule{GapMS: 500, Reply: "one two"})
	start := time.Now()
	ch, err := c.Stream(context.Background(), Request{Messages: user("hi")})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _, err = collect(t, ch)
	var de *DeadlineError
	if !errors.As(err, &de) || de.First || de.After != 250*time.Millisecond {
		t.Fatalf("err = %v, want the stall deadline", err)
	}
	// "one" at 500 ms, the stall at 750 ms; the first-token clock's 5 s
	// must not be what ended it.
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("the stall was caught after %v", el)
	}
}

func TestCutStream(t *testing.T) {
	srv := fake.NewT(t, fake.Rule{Reply: "half a", Cut: true})
	ch, err := New(Config{BaseURL: srv.BaseURL()}).Stream(context.Background(), Request{Messages: user("hi")})
	if err != nil {
		t.Fatal(err)
	}
	words, _, _, _, err := collect(t, ch)
	if err == nil {
		t.Fatal("a stream cut short ended without an error")
	}
	if words != "half a" {
		t.Errorf("words before the cut = %q", words)
	}
}

func TestStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"context overflow\"}}\n\n")
	}))
	defer srv.Close()
	ch, err := New(Config{BaseURL: srv.URL}).Stream(context.Background(), Request{Messages: user("hi")})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _, err = collect(t, ch)
	if err == nil || !strings.Contains(err.Error(), "context overflow") {
		t.Errorf("err = %v", err)
	}
}

func TestCancel(t *testing.T) {
	srv := fake.NewT(t, fake.Rule{GapMS: 200, Reply: "a b c d e f g h i j"})
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := New(Config{BaseURL: srv.BaseURL()}).Stream(ctx, Request{Messages: user("hi")})
	if err != nil {
		t.Fatal(err)
	}
	<-ch
	cancel()
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream did not close after cancel")
	}
}

// deltas is a server streaming these content deltas, then the finish and
// usage chunks given.
func deltas(t *testing.T, tail []string, pieces ...string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, p := range pieces {
			b, _ := json.Marshal(p)
			fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%s}}]}\n\n", b)
		}
		for _, f := range tail {
			fmt.Fprintf(w, "data: %s\n\n", f)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

var finishStop = []string{`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `[DONE]`}

// A server that streams the prefill back (llama-server does) and one that
// does not give the caller the same deltas: the continuation alone.
func TestPrefillEcho(t *testing.T) {
	for _, echo := range []bool{true, false} {
		srv := fake.NewT(t, fake.Rule{Reply: "🍬 sweets again.", EchoPrefill: echo})
		words, _, _, _ := stream(t, New(Config{BaseURL: srv.BaseURL()}), Request{Messages: user("hi"), Prefill: "Well, "})
		if words != "🍬 sweets again." {
			t.Errorf("echo %v: deltas = %q", echo, words)
		}
	}
	for _, tc := range []struct {
		name   string
		pieces []string
		want   string
	}{
		{"echo split over two deltas", []string{"Wel", "l, rain", " today"}, "rain today"},
		{"echo exactly the first delta", []string{"Well, ", "rain"}, "rain"},
		{"no echo, words that start like it", []string{"We", "ird weather"}, "Weird weather"},
		{"ends before the hold fills", []string{"Wel"}, "Wel"},
		{"nothing at all", nil, ""},
	} {
		url := deltas(t, finishStop, tc.pieces...)
		words, _, _, finish := stream(t, New(Config{BaseURL: url}), Request{Messages: user("hi"), Prefill: "Well, "})
		if words != tc.want || finish != "stop" {
			t.Errorf("%s: deltas = %q finish %q, want %q", tc.name, words, finish, tc.want)
		}
	}
	// Without a prefill nothing is held or stripped.
	url := deltas(t, finishStop, "Well, ", "rain")
	if words, _, _, _ := stream(t, New(Config{BaseURL: url}), Request{Messages: user("hi")}); words != "Well, rain" {
		t.Errorf("no prefill: %q", words)
	}
	// Held words still arrive before an error ends the stream.
	ch, err := New(Config{BaseURL: deltas(t, nil, "Wel")}).Stream(context.Background(), Request{Messages: user("hi"), Prefill: "Well, "})
	if err != nil {
		t.Fatal(err)
	}
	if words, _, _, _, err := collect(t, ch); words != "Wel" || err == nil {
		t.Errorf("cut while holding: %q, %v", words, err)
	}
}

func TestCachedTokens(t *testing.T) {
	url := deltas(t, []string{
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1169,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":1121}}}`,
		`[DONE]`,
	}, "hi")
	_, _, usage, _ := stream(t, New(Config{BaseURL: url}), Request{Messages: user("hi")})
	if usage == nil || *usage != (Usage{PromptTokens: 1169, CompletionTokens: 9, CachedTokens: 1121}) {
		t.Errorf("usage = %+v", usage)
	}
}
