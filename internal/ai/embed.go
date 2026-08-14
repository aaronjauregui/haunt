package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// EmbedConfig points at an OpenAI-compatible /embeddings endpoint. Leaving
// Model empty disables long-term memory entirely: the embedding model has
// to be pulled separately (`ollama pull nomic-embed-text`), so this stays
// opt-in rather than failing on first use.
type EmbedConfig struct {
	// BaseURL defaults to the LLM base URL — usually the same server.
	BaseURL string `toml:"base_url"`
	// Model like "nomic-embed-text"; required to enable memory.
	Model     string `toml:"model"`
	APIKeyEnv string `toml:"api_key_env"`
}

func (c *EmbedConfig) apiKey() string {
	if c.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(c.APIKeyEnv)
}

// EmbedEnabled reports whether the user configured an embedding model.
func (s *Service) EmbedEnabled() bool {
	return s.cfg.Embed.Model != "" && s.embedBaseURL() != ""
}

func (s *Service) embedBaseURL() string {
	if s.cfg.Embed.BaseURL != "" {
		return s.cfg.Embed.BaseURL
	}
	return s.cfg.LLM.BaseURL
}

// EmbedModel names the model in use, for cache keys and traces.
func (s *Service) EmbedModel() string { return s.cfg.Embed.Model }

// Embed returns the embedding vector for text. Budget is charged per call
// from the embed allowance, which is separate from dialogue: memory work
// happens alongside chat and must not eat the conversation's budget.
func (s *Service) Embed(ctx context.Context, text string) ([]float32, error) {
	if !s.EmbedEnabled() {
		return nil, fmt.Errorf("no embedding model configured (set [ai.embed] model in the haunt config)")
	}
	if err := s.embedBudget.spend(); err != nil {
		return nil, fmt.Errorf("embed budget: %w", err)
	}
	s.trace("embed → %s model=%s\n  [text] %s", s.embedBaseURL(), s.cfg.Embed.Model, text)

	reqBody, err := json.Marshal(map[string]any{
		"model": s.cfg.Embed.Model,
		"input": text,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(s.embedBaseURL(), "/")+"/embeddings", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := s.cfg.Embed.apiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed backend: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("embed backend: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embed backend response: %w", err)
	}
	if len(out.Data) == 0 || len(out.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embed backend returned no vector")
	}
	return out.Data[0].Embedding, nil
}
