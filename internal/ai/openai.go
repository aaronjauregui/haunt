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
func chatCompletion(ctx context.Context, cfg *LLMConfig, msgs []Message, onDelta func(string), opts Options) (Reply, error) {
	body := map[string]any{
		"model":    cfg.Model,
		"messages": msgs,
		"stream":   true,
	}
	// Left out unless there is something to offer: a backend that has never
	// heard of tool calling rejects the field rather than ignoring it, and
	// most conversations have nothing to offer anyway.
	if len(opts.Tools) > 0 {
		body["tools"] = toolSpecs(opts.Tools)
	}
	// Left out entirely when unconfigured, so the backend's own default
	// applies rather than one invented here.
	if temp, ok := cfg.temperature(opts.Silent); ok {
		body["temperature"] = temp
	}
	reqBody, err := json.Marshal(body)
	if err != nil {
		return Reply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := cfg.apiKey(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return Reply{}, fmt.Errorf("llm backend: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Reply{}, fmt.Errorf("llm backend: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	// SSE: lines of "data: {...}" ending with "data: [DONE]".
	var full strings.Builder
	// Tool calls arrive in pieces, keyed by their position in the list: an
	// id and a name in one chunk, then the arguments a few characters at a
	// time. Nothing here is valid JSON until the stream ends, which is why
	// they are assembled rather than parsed as they arrive.
	calls := map[int]*ToolCall{}
	var order []int
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
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
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
			for _, tc := range ch.Delta.ToolCalls {
				c, seen := calls[tc.Index]
				if !seen {
					c = &ToolCall{}
					calls[tc.Index] = c
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					c.ID = tc.ID
				}
				if tc.Type != "" {
					c.Type = tc.Type
				}
				if tc.Function.Name != "" {
					c.Function.Name = tc.Function.Name
				}
				c.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	reply := Reply{Text: full.String()}
	for _, i := range order {
		c := calls[i]
		if c.Function.Name == "" {
			continue
		}
		if c.Type == "" {
			c.Type = "function"
		}
		// Some backends stream a call with no id at all. The id is only
		// ever used to tie the result back to the call, so inventing a
		// stable one is better than dropping a call the model asked for.
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", i)
		}
		reply.Calls = append(reply.Calls, *c)
	}
	if err := sc.Err(); err != nil {
		return reply, fmt.Errorf("llm stream: %w", err)
	}
	// A reply with tool calls and no words is the ordinary shape of a model
	// that means to do something before it speaks, so emptiness is only a
	// failure when there is nothing at all.
	if full.Len() == 0 && len(reply.Calls) == 0 {
		return Reply{}, fmt.Errorf("llm backend returned an empty reply")
	}
	return reply, nil
}

// toolSpecs renders the tools in the shape the chat completions API wants.
// A tool with no schema is given the empty object schema rather than none:
// backends differ about whether the field may be missing, and every one of
// them accepts an object with no properties.
func toolSpecs(tools []Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  schema,
			},
		})
	}
	return out
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
