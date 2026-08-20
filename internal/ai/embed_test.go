package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeEmbedder returns a vector derived from the input so tests can tell
// two embeddings apart without a model.
func fakeEmbedder(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if seen != nil {
			*seen = append(*seen, req.Input)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{float32(len(req.Input)), 1, 0}}},
		})
	}))
}

func TestEmbed(t *testing.T) {
	var seen []string
	srv := fakeEmbedder(t, &seen)
	defer srv.Close()

	s := NewService(&Config{
		Embed: EmbedConfig{BaseURL: srv.URL + "/v1", Model: "fake-embed"},
	}, nil)
	if !s.EmbedEnabled() {
		t.Fatal("EmbedEnabled = false with a model configured")
	}
	vec, err := s.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(vec) != 3 || vec[0] != 5 {
		t.Errorf("vector = %v", vec)
	}
	if len(seen) != 1 || seen[0] != "hello" {
		t.Errorf("backend saw %q", seen)
	}
}

// TestEmbedDefaultsToLLMEndpoint: one Ollama server usually serves both, so
// [ai.embed] needs only a model name.
func TestEmbedDefaultsToLLMEndpoint(t *testing.T) {
	srv := fakeEmbedder(t, nil)
	defer srv.Close()

	s := NewService(&Config{
		LLM:   LLMConfig{BaseURL: srv.URL + "/v1", Model: "fake"},
		Embed: EmbedConfig{Model: "fake-embed"},
	}, nil)
	if _, err := s.Embed(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
}

// TestEmbedDisabled: memory stays off until a model is named, and says so.
func TestEmbedDisabled(t *testing.T) {
	s := NewService(&Config{LLM: LLMConfig{BaseURL: "http://example.invalid/v1"}}, nil)
	if s.EmbedEnabled() {
		t.Error("EmbedEnabled = true with no model configured")
	}
	_, err := s.Embed(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "[ai.embed]") {
		t.Errorf("err = %v, want a pointer at the config", err)
	}
}

// TestEmbedBudgetIsSeparate: memory work must not consume the dialogue
// allowance, or a chatty session would silence itself.
func TestEmbedBudgetIsSeparate(t *testing.T) {
	srv := fakeEmbedder(t, nil)
	defer srv.Close()

	s := NewService(&Config{
		LLM:    LLMConfig{BaseURL: srv.URL + "/v1", Model: "fake"},
		Embed:  EmbedConfig{Model: "fake-embed"},
		Budget: BudgetConfig{LLMRequestsPerHour: 1, EmbedRequestsPerHour: 3},
	}, nil)
	for i := 0; i < 3; i++ {
		if _, err := s.Embed(context.Background(), "hello"); err != nil {
			t.Fatalf("embed %d: %v", i, err)
		}
	}
	if _, err := s.Embed(context.Background(), "hello"); err == nil {
		t.Error("embed budget never ran out")
	}
	// The LLM allowance is untouched by all that memory work.
	if got := s.budgets.llm.limit - len(s.budgets.llm.spends); got != 1 {
		t.Errorf("llm budget remaining = %d, want 1", got)
	}
}
