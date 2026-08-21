package ai

import (
	"github.com/aaronjauregui/haunt/internal/hlog"

	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeLLM serves an OpenAI-style streaming chat completion that echoes the
// last user message word by word.
func fakeLLM(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Messages []Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		last := req.Messages[len(req.Messages)-1].Content
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, word := range strings.Fields("echo: " + last) {
			chunk := map[string]any{"choices": []map[string]any{
				{"delta": map[string]string{"content": word + " "}},
			}}
			b, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestChatStreamsAndAssembles(t *testing.T) {
	srv := fakeLLM(t)
	defer srv.Close()

	s := NewService(&Config{
		LLM:    LLMConfig{BaseURL: srv.URL + "/v1", Model: "fake"},
		Budget: BudgetConfig{LLMRequestsPerHour: 10, ImageRequestsPerHour: 10},
	}, nil)
	var deltas []string
	full, err := s.Chat(context.Background(), []Message{
		{Role: "system", Content: "persona"},
		{Role: "user", Content: "hello ghost"},
	}, func(d string) { deltas = append(deltas, d) }, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(full) != "echo: hello ghost" {
		t.Errorf("full = %q", full)
	}
	if len(deltas) != 3 {
		t.Errorf("deltas = %q, want 3 fragments", deltas)
	}
}

func TestBudgetExhaustion(t *testing.T) {
	srv := fakeLLM(t)
	defer srv.Close()
	s := NewService(&Config{
		LLM:    LLMConfig{BaseURL: srv.URL + "/v1", Model: "fake"},
		Budget: BudgetConfig{LLMRequestsPerHour: 1, ImageRequestsPerHour: 1},
	}, nil)
	if _, err := s.Chat(context.Background(), []Message{{Role: "user", Content: "one"}}, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chat(context.Background(), []Message{{Role: "user", Content: "two"}}, nil, Options{}); err == nil {
		t.Fatal("second request should exceed budget")
	}
}

func TestBudgetWindowSlides(t *testing.T) {
	b := newBudget(2)
	now := time.Now()
	b.now = func() time.Time { return now }
	if err := b.spend(); err != nil {
		t.Fatal(err)
	}
	if err := b.spend(); err != nil {
		t.Fatal(err)
	}
	if err := b.spend(); err == nil {
		t.Fatal("third spend should fail")
	}
	now = now.Add(61 * time.Minute)
	if err := b.spend(); err != nil {
		t.Fatalf("spend after window slid: %v", err)
	}
}

func TestUnconfiguredBackendsError(t *testing.T) {
	s := NewService(&Config{Budget: BudgetConfig{LLMRequestsPerHour: 1, ImageRequestsPerHour: 1}}, nil)
	if _, err := s.Chat(context.Background(), nil, nil, Options{}); err == nil {
		t.Error("chat without backend should fail")
	}
	if _, err := s.GenerateImage(context.Background(), "x"); err == nil {
		t.Error("image without backend should fail")
	}
}

// fakeImage serves an OpenAI-style image generation endpoint returning a
// fixed byte payload.
func TestGenerateImage(t *testing.T) {
	payload := []byte("fake-png-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(payload)}},
		})
	}))
	defer srv.Close()

	s := NewService(&Config{
		Image:  ImageConfig{BaseURL: srv.URL + "/v1", Model: "fake"},
		Budget: BudgetConfig{LLMRequestsPerHour: 1, ImageRequestsPerHour: 5},
	}, nil)
	got, err := s.GenerateImage(context.Background(), "a ghost")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("image bytes = %q", got)
	}
}

// capturingLLM records the raw request bodies it is sent.
func capturingLLM(t *testing.T, bodies *[]map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		*bodies = append(*bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
}

// TestTemperature: dialogue inherits the backend default unless configured,
// while silent brain-internal calls sample cold so their answers repeat.
func TestTemperature(t *testing.T) {
	var bodies []map[string]any
	srv := capturingLLM(t, &bodies)
	defer srv.Close()

	warm := 0.9
	for _, tc := range []struct {
		name   string
		cfg    LLMConfig
		opts   Options
		want   float64
		wantOK bool
	}{
		{"dialogue defaults to the backend's own", LLMConfig{}, Options{}, 0, false},
		{"dialogue honors config", LLMConfig{Temperature: &warm}, Options{}, 0.9, true},
		{"silent defaults cold", LLMConfig{Temperature: &warm}, Options{Silent: true}, defaultSilentTemperature, true},
		{"silent honors config", LLMConfig{SilentTemperature: &warm}, Options{Silent: true}, 0.9, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bodies = nil
			cfg := tc.cfg
			cfg.BaseURL, cfg.Model = srv.URL+"/v1", "fake"
			s := NewService(&Config{LLM: cfg, Budget: BudgetConfig{LLMRequestsPerHour: 5}}, nil)
			if _, err := s.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, tc.opts); err != nil {
				t.Fatal(err)
			}
			got, ok := bodies[0]["temperature"]
			if ok != tc.wantOK {
				t.Fatalf("temperature present = %v, want %v (body %v)", ok, tc.wantOK, bodies[0])
			}
			if ok && got.(float64) != tc.want {
				t.Errorf("temperature = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPromptTracing: prompts stay out of the log until asked for, and then
// show the whole request.
func TestPromptTracing(t *testing.T) {
	srv := fakeLLM(t)
	defer srv.Close()
	msgs := []Message{
		{Role: "system", Content: "you are a wry spirit"},
		{Role: "user", Content: "hello ghost"},
	}
	newSvc := func(logPrompts bool) (*Service, func() []string) {
		// With the prompt topic off unless the switch is on, so this also
		// checks that tracing stays off in the shipping configuration.
		rec, lines := hlog.Recorder()
		if !logPrompts {
			rec, lines = hlog.RecorderAt(hlog.LevelInfo)
		}
		return NewService(&Config{
			LLM:        LLMConfig{BaseURL: srv.URL + "/v1", Model: "fake"},
			Budget:     BudgetConfig{LLMRequestsPerHour: 5, ImageRequestsPerHour: 5},
			LogPrompts: logPrompts,
		}, rec), lines
	}

	s, quiet := newSvc(false)
	if _, err := s.Chat(context.Background(), msgs, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := quiet(); len(got) != 0 {
		t.Errorf("tracing off still logged: %q", got)
	}

	s, loud := newSvc(true)
	if _, err := s.Chat(context.Background(), msgs, nil, Options{Silent: true, Tag: "imgprompt"}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(loud(), "\n")
	for _, want := range []string{
		"model=fake", "silent", "tag=imgprompt", "temperature=0.2",
		"[system] you are a wry spirit", "[user] hello ghost", "echo: hello ghost",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("trace missing %q; got:\n%s", want, joined)
		}
	}

	// Asking for the prompt topic turns tracing on without touching the
	// config. (That HAUNT_AI_DEBUG=1 still selects that topic is
	// internal/hlog's business, and tested there.)
	byTopic, viaTopic := hlog.RecorderAt(hlog.LevelInfo, hlog.Prompt)
	s = NewService(&Config{
		LLM:    LLMConfig{BaseURL: srv.URL + "/v1", Model: "fake"},
		Budget: BudgetConfig{LLMRequestsPerHour: 5},
	}, byTopic)
	if _, err := s.Chat(context.Background(), msgs, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	if len(viaTopic()) == 0 {
		t.Error("the prompt topic produced no trace")
	}
}

// fakeToolLLM streams a tool call the way the API does: the id and name in
// one chunk, the arguments a few characters at a time, none of it valid
// JSON until the stream ends. Assembling that is most of what the tool
// path in this package does.
func fakeToolLLM(t *testing.T, wantTools bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Tools []struct {
				Type     string `json:"type"`
				Function struct {
					Name       string          `json:"name"`
					Parameters json.RawMessage `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if wantTools && len(req.Tools) == 0 {
			http.Error(w, "the tools were not offered", 400)
			return
		}
		if !wantTools && len(req.Tools) > 0 {
			http.Error(w, "tools were offered when there were none", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		send := func(delta map[string]any) {
			b, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": delta}},
			})
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
		if wantTools {
			send(map[string]any{"tool_calls": []map[string]any{{
				"index": 0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": "notes__append"},
			}}})
			for _, frag := range []string{`{"te`, `xt":"h`, `i"}`} {
				send(map[string]any{"tool_calls": []map[string]any{{
					"index": 0, "function": map[string]any{"arguments": frag},
				}}})
			}
		} else {
			send(map[string]any{"content": "done"})
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func toolService(t *testing.T, srv *httptest.Server) *Service {
	t.Helper()
	return NewService(&Config{
		LLM:    LLMConfig{BaseURL: srv.URL + "/v1", Model: "fake"},
		Budget: BudgetConfig{LLMRequestsPerHour: 10},
	}, nil)
}

func TestToolCallsAreAssembledFromTheStream(t *testing.T) {
	srv := fakeToolLLM(t, true)
	defer srv.Close()

	reply, err := toolService(t, srv).ChatWithTools(context.Background(),
		[]Message{{Role: "user", Content: "note this"}}, nil,
		Options{Tools: []Tool{{Name: "notes__append", Description: "Append"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.Calls) != 1 {
		t.Fatalf("calls = %d, want the one the model asked for", len(reply.Calls))
	}
	c := reply.Calls[0]
	if c.ID != "call_1" || c.Function.Name != "notes__append" {
		t.Errorf("call = %+v, want the id and name from the first chunk", c)
	}
	// The whole point of assembling rather than parsing as it arrives.
	if c.Function.Arguments != `{"text":"hi"}` {
		t.Errorf("arguments = %q, want the fragments joined", c.Function.Arguments)
	}
	if !json.Valid([]byte(c.Function.Arguments)) {
		t.Error("the assembled arguments are not valid JSON")
	}
}

// A reply that is only tool calls has no words in it, and that is the
// ordinary shape of a model meaning to do something before it speaks —
// not the empty reply the plain chat path treats as a failure.
func TestToolCallWithNoWordsIsNotEmpty(t *testing.T) {
	srv := fakeToolLLM(t, true)
	defer srv.Close()

	reply, err := toolService(t, srv).ChatWithTools(context.Background(),
		[]Message{{Role: "user", Content: "note this"}}, nil,
		Options{Tools: []Tool{{Name: "notes__append"}}})
	if err != nil {
		t.Fatalf("a wordless tool call was treated as a failure: %v", err)
	}
	if reply.Text != "" {
		t.Errorf("text = %q, want none", reply.Text)
	}
}

// With nothing to offer, the field is left out of the request entirely: a
// backend that has never heard of tool calling rejects it rather than
// ignoring it, and most conversations have nothing to offer anyway.
func TestNoToolsMeansNoToolsField(t *testing.T) {
	srv := fakeToolLLM(t, false)
	defer srv.Close()

	full, err := toolService(t, srv).Chat(context.Background(),
		[]Message{{Role: "user", Content: "hello"}}, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if full != "done" {
		t.Errorf("reply = %q", full)
	}
}

// A tool with no schema still has to be offered with one: backends differ
// about whether the field may be missing and all of them take an object
// with no properties.
func TestToolSpecsFillInAMissingSchema(t *testing.T) {
	specs := toolSpecs([]Tool{{Name: "t"}})
	fn := specs[0]["function"].(map[string]any)
	params, ok := fn["parameters"].(json.RawMessage)
	if !ok || !json.Valid(params) {
		t.Fatalf("parameters = %v, want a valid schema", fn["parameters"])
	}
	if !strings.Contains(string(params), "object") {
		t.Errorf("parameters = %s, want an object schema", params)
	}
}

// The tool allowance is separate from the conversation's: acting rations
// something else, and one exchange can contain several actions.
func TestToolBudgetIsItsOwn(t *testing.T) {
	s := NewService(&Config{
		Budget: BudgetConfig{LLMRequestsPerHour: 10, ToolRequestsPerHour: 2},
	}, nil)
	for i := range 2 {
		if err := s.SpendToolCall(); err != nil {
			t.Fatalf("call %d was refused: %v", i+1, err)
		}
	}
	if err := s.SpendToolCall(); err == nil {
		t.Error("a third call went through an allowance of two")
	}
}
