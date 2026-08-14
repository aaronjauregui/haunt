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
	})
	var deltas []string
	full, err := s.Chat(context.Background(), []Message{
		{Role: "system", Content: "persona"},
		{Role: "user", Content: "hello ghost"},
	}, func(d string) { deltas = append(deltas, d) })
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
	})
	if _, err := s.Chat(context.Background(), []Message{{Role: "user", Content: "one"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chat(context.Background(), []Message{{Role: "user", Content: "two"}}, nil); err == nil {
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
	s := NewService(&Config{Budget: BudgetConfig{LLMRequestsPerHour: 1, ImageRequestsPerHour: 1}})
	if _, err := s.Chat(context.Background(), nil, nil); err == nil {
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
	})
	got, err := s.GenerateImage(context.Background(), "a ghost")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("image bytes = %q", got)
	}
}
