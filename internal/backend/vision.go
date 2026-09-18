package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Look shows a PNG to the vision model and returns what it says.
//
// The question is the caller's, and it should be a specific one. "Comment
// on this screenshot" gets fluent, generic observations that grow tiresome
// by the third; "what is this person working on, in one sentence" gets
// something a ghost can actually use.
//
// Charged against its own hourly budget: a look costs about what a reply
// costs, and the model the ghost talks through is billed by hermes, not
// here.
func (s *Service) Look(ctx context.Context, question string, png []byte) (string, error) {
	if !s.VisionEnabled() {
		return "", fmt.Errorf("no vision model configured (set [backend.vision] base_url and model in the haunt config)")
	}
	if err := s.budgets.vision.spend(); err != nil {
		return "", fmt.Errorf("vision budget: %w", err)
	}
	s.trace("look → %s model=%s (%d KiB image)\n  [question] %s",
		s.cfg.Vision.BaseURL, s.cfg.Vision.Model, len(png)/1024, question)

	// OpenAI-compatible multimodal content: text plus an inline data URI.
	content := []map[string]any{
		{"type": "text", "text": question},
		{"type": "image_url", "image_url": map[string]string{
			"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		}},
	}
	reqBody, err := json.Marshal(map[string]any{
		"model": s.cfg.Vision.Model,
		"messages": []map[string]any{
			{"role": "user", "content": content},
		},
		"max_tokens": 300,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(s.cfg.Vision.BaseURL, "/")+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := s.cfg.Vision.apiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("vision backend: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("vision backend: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("vision backend response: %w", err)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("vision backend returned nothing")
	}
	answer := strings.TrimSpace(out.Choices[0].Message.Content)
	s.trace("look: %s", answer)
	return answer, nil
}
