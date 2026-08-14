package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// comfyGenerate drives a ComfyUI server through its workflow-graph API:
// submit a minimal txt2img graph to /prompt, poll /history until the run
// completes, and fetch the result via /view.
func comfyGenerate(ctx context.Context, cfg *ImageConfig, prompt string) ([]byte, error) {
	width, height := parseSize(cfg.Size, 512)
	steps := cfg.Steps
	if steps <= 0 {
		steps = 12
	}
	cfgScale := cfg.CFG
	if cfgScale <= 0 {
		cfgScale = 5.5
	}
	negative := cfg.Negative
	if negative == "" {
		negative = "blurry, text, watermark, low quality"
	}
	sampler := cfg.Sampler
	if sampler == "" {
		sampler = "euler"
	}

	graph := map[string]any{
		"1": node("CheckpointLoaderSimple", map[string]any{"ckpt_name": cfg.Model}),
		"2": node("CLIPTextEncode", map[string]any{"clip": ref("1", 1), "text": prompt}),
		"3": node("CLIPTextEncode", map[string]any{"clip": ref("1", 1), "text": negative}),
		"4": node("EmptyLatentImage", map[string]any{"width": width, "height": height, "batch_size": 1}),
		"5": node("KSampler", map[string]any{
			"model": ref("1", 0), "positive": ref("2", 0), "negative": ref("3", 0),
			"latent_image": ref("4", 0), "seed": rand.Int63(), "steps": steps,
			"cfg": cfgScale, "sampler_name": sampler, "scheduler": "normal", "denoise": 1.0,
		}),
		"6": node("VAEDecode", map[string]any{"samples": ref("5", 0), "vae": ref("1", 2)}),
		"7": node("SaveImage", map[string]any{"images": ref("6", 0), "filename_prefix": "haunt"}),
	}
	body, err := json.Marshal(map[string]any{"client_id": "haunt", "prompt": graph})
	if err != nil {
		return nil, err
	}

	base := strings.TrimRight(cfg.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/prompt", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("comfyui: %w", err)
	}
	var submit struct {
		PromptID   string          `json:"prompt_id"`
		NodeErrors json.RawMessage `json:"node_errors"`
		Error      json.RawMessage `json:"error"`
	}
	err = json.NewDecoder(resp.Body).Decode(&submit)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("comfyui submit: %w", err)
	}
	if submit.PromptID == "" {
		return nil, fmt.Errorf("comfyui rejected the workflow: %s %s", submit.Error, submit.NodeErrors)
	}

	// Poll history until outputs appear.
	var img struct{ Filename, Subfolder, Type string }
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
		hreq, err := http.NewRequestWithContext(ctx, "GET", base+"/history/"+submit.PromptID, nil)
		if err != nil {
			return nil, err
		}
		hresp, err := httpClient.Do(hreq)
		if err != nil {
			return nil, fmt.Errorf("comfyui history: %w", err)
		}
		var history map[string]struct {
			Status struct {
				Completed bool   `json:"completed"`
				StatusStr string `json:"status_str"`
			} `json:"status"`
			Outputs map[string]struct {
				Images []struct {
					Filename  string `json:"filename"`
					Subfolder string `json:"subfolder"`
					Type      string `json:"type"`
				} `json:"images"`
			} `json:"outputs"`
		}
		err = json.NewDecoder(hresp.Body).Decode(&history)
		hresp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("comfyui history: %w", err)
		}
		entry, ok := history[submit.PromptID]
		if !ok {
			continue // still queued
		}
		if entry.Status.StatusStr == "error" {
			return nil, fmt.Errorf("comfyui run failed (see the ComfyUI console)")
		}
		for _, out := range entry.Outputs {
			if len(out.Images) > 0 {
				img.Filename = out.Images[0].Filename
				img.Subfolder = out.Images[0].Subfolder
				img.Type = out.Images[0].Type
			}
		}
		if img.Filename != "" {
			break
		}
	}

	q := url.Values{"filename": {img.Filename}, "subfolder": {img.Subfolder}, "type": {img.Type}}
	vreq, err := http.NewRequestWithContext(ctx, "GET", base+"/view?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	vresp, err := httpClient.Do(vreq)
	if err != nil {
		return nil, fmt.Errorf("comfyui view: %w", err)
	}
	defer vresp.Body.Close()
	if vresp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("comfyui view: %s", vresp.Status)
	}
	return io.ReadAll(io.LimitReader(vresp.Body, 64<<20))
}

func node(class string, inputs map[string]any) map[string]any {
	return map[string]any{"class_type": class, "inputs": inputs}
}

func ref(id string, output int) []any { return []any{id, output} }

// parseSize turns "WxH" into dimensions, defaulting both to def.
func parseSize(s string, def int) (w, h int) {
	w, h = def, def
	if s == "" {
		return
	}
	var pw, ph int
	if _, err := fmt.Sscanf(s, "%dx%d", &pw, &ph); err == nil && pw > 0 && ph > 0 {
		w, h = pw, ph
	}
	return
}
