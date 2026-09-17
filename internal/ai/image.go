package ai

import (
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
