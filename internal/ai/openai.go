package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 5 * time.Minute}

// chatCompletion calls an OpenAI-compatible /chat/completions endpoint with
// streaming, invoking onDelta per content fragment.
func chatCompletion(ctx context.Context, cfg *LLMConfig, msgs []Message, onDelta func(string)) (string, error) {
	reqBody, err := json.Marshal(map[string]any{
		"model":    cfg.Model,
		"messages": msgs,
		"stream":   true,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := cfg.apiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm backend: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("llm backend: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	// SSE: lines of "data: {...}" ending with "data: [DONE]".
	var full strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // tolerate keep-alives and vendor extras
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				full.WriteString(ch.Delta.Content)
				if onDelta != nil {
					onDelta(ch.Delta.Content)
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return full.String(), fmt.Errorf("llm stream: %w", err)
	}
	if full.Len() == 0 {
		return "", fmt.Errorf("llm backend returned an empty reply")
	}
	return full.String(), nil
}

// generateImage calls an OpenAI-compatible /images/generations endpoint and
// returns PNG bytes.
func generateImage(ctx context.Context, cfg *ImageConfig, prompt string) ([]byte, error) {
	body := map[string]any{
		"model":           cfg.Model,
		"prompt":          prompt,
		"response_format": "b64_json",
		"n":               1,
	}
	if cfg.Size != "" {
		body["size"] = cfg.Size
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(cfg.BaseURL, "/")+"/images/generations", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := cfg.apiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("image backend: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("image backend: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("image backend response: %w", err)
	}
	if len(out.Data) == 0 || out.Data[0].B64JSON == "" {
		return nil, fmt.Errorf("image backend returned no image")
	}
	png, err := base64.StdEncoding.DecodeString(out.Data[0].B64JSON)
	if err != nil {
		return nil, fmt.Errorf("image backend b64: %w", err)
	}
	return png, nil
}
