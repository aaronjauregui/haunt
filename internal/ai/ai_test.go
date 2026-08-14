package ai

import (
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
	newSvc := func(logPrompts bool, lines *[]string) *Service {
		return NewService(&Config{
			LLM:        LLMConfig{BaseURL: srv.URL + "/v1", Model: "fake"},
			Budget:     BudgetConfig{LLMRequestsPerHour: 5, ImageRequestsPerHour: 5},
			LogPrompts: logPrompts,
		}, func(s string) { *lines = append(*lines, s) })
	}

	var quiet []string
	s := newSvc(false, &quiet)
	if _, err := s.Chat(context.Background(), msgs, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	if len(quiet) != 0 {
		t.Errorf("tracing off still logged: %q", quiet)
	}

	var loud []string
	s = newSvc(true, &loud)
	if _, err := s.Chat(context.Background(), msgs, nil, Options{Silent: true, Tag: "imgprompt"}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(loud, "\n")
	for _, want := range []string{
		"model=fake", "silent", "tag=imgprompt", "temperature=0.2",
		"[system] you are a wry spirit", "[user] hello ghost", "echo: hello ghost",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("trace missing %q; got:\n%s", want, joined)
		}
	}

	// HAUNT_AI_DEBUG turns it on without touching the config.
	var env []string
	t.Setenv("HAUNT_AI_DEBUG", "1")
	s = newSvc(false, &env)
	if _, err := s.Chat(context.Background(), msgs, nil, Options{}); err != nil {
		t.Fatal(err)
	}
	if len(env) == 0 {
		t.Error("HAUNT_AI_DEBUG=1 produced no trace")
	}
}
