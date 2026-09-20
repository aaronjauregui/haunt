package backend

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// comfyDefaultSize is the per-axis fallback when no size is configured.
const comfyDefaultSize = 512

// comfyWorkflowSize is the per-axis fallback when the picture comes from a
// workflow of the person's own. They export one for the model they run,
// and the models worth exporting a graph for expect 1024, not the modest
// size the built-in graph is tuned to.
const comfyWorkflowSize = 1024

// comfyDefaults resolves the sampler settings the workflow will actually
// use, so the prompt trace can report them rather than guess.
func comfyDefaults(cfg *ImageConfig) (steps int, cfgScale float64, sampler, negative string) {
	steps, cfgScale, sampler, negative = cfg.Steps, cfg.CFG, cfg.Sampler, cfg.Negative
	if steps <= 0 {
		steps = 12
	}
	if cfgScale <= 0 {
		cfgScale = 5.5
	}
	if sampler == "" {
		sampler = "euler"
	}
	if negative == "" {
		negative = "blurry, text, watermark, low quality"
	}
	return steps, cfgScale, sampler, negative
}

// comfyRun is one resolved request: the graph about to be submitted, and
// the values that went into it. The trace reports those values rather than
// the configured ones, because most of them are defaulted or come from the
// workflow and reading back what was configured tells you nothing about
// the picture you got.
type comfyRun struct {
	graph map[string]any
	// workflow is the file the graph came from, empty for the built-in one.
	workflow      string
	width, height int
	negative      string
	// seed is the value put in the graph, or zero when the graph chose its
	// own and there is nothing to report.
	seed     int64
	steps    int
	cfgScale float64
	sampler  string
}

// comfyPlan resolves a request into the graph to submit. The built-in
// graph and a workflow of the person's own differ only here; submitting,
// waiting and fetching the result are the same either way.
func comfyPlan(cfg *ImageConfig, req DrawRequest) (*comfyRun, error) {
	negative := joinNonEmpty(", ", cfg.Negative, req.Negative)
	seed := rand.Int63()

	if cfg.Workflow != "" {
		wf, err := cfg.loadWorkflow()
		if err != nil {
			return nil, err
		}
		width, height := parseSize(cfg.Size, comfyWorkflowSize)
		p := &placeholders{
			prompt: req.Prompt, negative: negative,
			seed: seed, width: width, height: height,
		}
		run := &comfyRun{
			graph: p.apply(wf.graph).(map[string]any), workflow: wf.path,
			width: width, height: height, negative: negative,
		}
		if p.tookSeed {
			run.seed = seed
		}
		return run, nil
	}

	width, height := parseSize(cfg.Size, comfyDefaultSize)
	steps, cfgScale, sampler, builtinNegative := comfyDefaults(cfg)
	if negative != "" {
		builtinNegative = negative
	}
	return &comfyRun{
		graph:    builtinGraph(cfg, req.Prompt, builtinNegative, seed, width, height),
		width:    width,
		height:   height,
		negative: builtinNegative,
		seed:     seed,
		steps:    steps,
		cfgScale: cfgScale,
		sampler:  sampler,
	}, nil
}

// builtinGraph is the minimal txt2img graph haunt draws with when the
// person has not supplied one: load a checkpoint, encode both prompts,
// sample, decode, save.
func builtinGraph(cfg *ImageConfig, prompt, negative string, seed int64, width, height int) map[string]any {
	steps, cfgScale, sampler, _ := comfyDefaults(cfg)
	return map[string]any{
		"1": node("CheckpointLoaderSimple", map[string]any{"ckpt_name": cfg.Model}),
		"2": node("CLIPTextEncode", map[string]any{"clip": ref("1", 1), "text": prompt}),
		"3": node("CLIPTextEncode", map[string]any{"clip": ref("1", 1), "text": negative}),
		"4": node("EmptyLatentImage", map[string]any{"width": width, "height": height, "batch_size": 1}),
		"5": node("KSampler", map[string]any{
			"model": ref("1", 0), "positive": ref("2", 0), "negative": ref("3", 0),
			"latent_image": ref("4", 0), "seed": seed, "steps": steps,
			"cfg": cfgScale, "sampler_name": sampler, "scheduler": "normal", "denoise": 1.0,
		}),
		"6": node("VAEDecode", map[string]any{"samples": ref("5", 0), "vae": ref("1", 2)}),
		"7": node("SaveImage", map[string]any{"images": ref("6", 0), "filename_prefix": "haunt"}),
	}
}

// workflow is a ComfyUI graph the person exported with "Save (API format)"
// and put placeholders in. It replaces the built-in graph entirely: the
// checkpoint, the sampler and everything else about how the picture is
// made belong to the graph, and haunt only fills in the parts that vary
// from picture to picture.
type workflow struct {
	// path is kept for error messages and the trace, which have to name a
	// file the person can go and open.
	path  string
	graph map[string]any
}

// Placeholders, as they are written in the graph's string values.
const (
	phPrompt   = "{{prompt}}"
	phNegative = "{{negative}}"
	phSeed     = "{{seed}}"
	phWidth    = "{{width}}"
	phHeight   = "{{height}}"
)

// loadWorkflow returns the parsed graph, reading it if the config was not
// prepared at startup. The parse is kept on the config so that a picture
// costs no disk read and, more to the point, so that a graph that has been
// edited into nonsense since is not discovered halfway through a turn.
func (c *ImageConfig) loadWorkflow() (*workflow, error) {
	if c.workflow != nil {
		return c.workflow, nil
	}
	return readWorkflow(c.Workflow)
}

// prepare reads and checks what the config points at, so that a mistake in
// it is a message at startup naming the file rather than a ghost that says
// it cannot draw. It is called once, before any ghost is running, and is
// what fills in the parsed workflow the drawing path reads.
func (c *ImageConfig) prepare() error {
	if c.Workflow == "" || c.workflow != nil {
		return nil
	}
	wf, err := readWorkflow(c.Workflow)
	if err != nil {
		return err
	}
	c.workflow = wf
	return nil
}

// readWorkflow parses and checks one workflow file. The checks are the
// three ways an export goes wrong: the wrong export (the editor's format
// rather than the API's), no place to put the prompt, and nothing that
// hands the picture back.
func readWorkflow(path string) (*workflow, error) {
	full, err := expandHome(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("[backend.image] workflow: %w", err)
	}
	var graph map[string]any
	if err := json.Unmarshal(data, &graph); err != nil {
		return nil, fmt.Errorf("[backend.image] workflow %s: %w", full, err)
	}
	var outputs, nodes int
	for id, v := range graph {
		n, ok := v.(map[string]any)
		if !ok {
			continue
		}
		class, ok := n["class_type"].(string)
		if !ok {
			continue
		}
		nodes++
		if _, ok := n["inputs"].(map[string]any); !ok {
			return nil, fmt.Errorf("[backend.image] workflow %s: node %s has no inputs", full, id)
		}
		if class == "SaveImage" || class == "PreviewImage" {
			outputs++
		}
	}
	if nodes == 0 {
		return nil, fmt.Errorf("[backend.image] workflow %s: no nodes with a class_type; "+
			"export it from ComfyUI with Save (API format), not Save", full)
	}
	if !hasPlaceholder(graph, phPrompt) {
		return nil, fmt.Errorf("[backend.image] workflow %s: no %s; "+
			"put it in the text of the positive prompt node", full, phPrompt)
	}
	if outputs == 0 {
		return nil, fmt.Errorf("[backend.image] workflow %s: no SaveImage or PreviewImage node, "+
			"so there is no picture to collect", full)
	}
	return &workflow{path: full, graph: graph}, nil
}

// hasPlaceholder reports whether the placeholder appears anywhere in the
// graph's string values.
func hasPlaceholder(v any, want string) bool {
	switch v := v.(type) {
	case map[string]any:
		for _, val := range v {
			if hasPlaceholder(val, want) {
				return true
			}
		}
	case []any:
		for _, val := range v {
			if hasPlaceholder(val, want) {
				return true
			}
		}
	case string:
		return strings.Contains(v, want)
	}
	return false
}

// placeholders substitutes a picture's varying parts into a copy of the
// graph. Working on the decoded values rather than on the file's text is
// what makes a prompt with quotes and newlines in it safe: it is put in as
// a JSON string, never as text spliced into one.
type placeholders struct {
	prompt, negative string
	seed             int64
	width, height    int
	// tookSeed records whether the graph asked for a seed, since only then
	// is there a seed worth remembering against the picture.
	tookSeed bool
}

// apply returns v with the placeholders substituted, leaving v untouched:
// the parsed workflow is read by every draw and must stay as it was read.
func (p *placeholders) apply(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = p.apply(val)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			out[i] = p.apply(val)
		}
		return out
	case string:
		return p.applyString(v)
	default:
		return v
	}
}

// applyString substitutes one string value, which may stop being a string.
// A placeholder that is the whole value stands for a number: ComfyUI
// type-checks its inputs, and a seed or a width arriving as text is
// refused before anything is drawn.
func (p *placeholders) applyString(s string) any {
	switch strings.TrimSpace(s) {
	case phSeed:
		p.tookSeed = true
		return p.seed
	case phWidth:
		return p.width
	case phHeight:
		return p.height
	}
	s = strings.ReplaceAll(s, phPrompt, p.prompt)
	s = strings.ReplaceAll(s, phNegative, p.negative)
	return s
}

// comfyGenerate drives a ComfyUI server through its workflow-graph API:
// submit the graph to /prompt, poll /history until the run completes, and
// fetch the result via /view.
func comfyGenerate(ctx context.Context, cfg *ImageConfig, graph map[string]any) ([]byte, error) {
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
		// A graph may have several output nodes. The lowest-numbered one
		// wins, so which picture comes back is a property of the graph and
		// not of map iteration order.
		for _, id := range sortedNodeIDs(entry.Outputs) {
			if out := entry.Outputs[id]; len(out.Images) > 0 {
				img.Filename = out.Images[0].Filename
				img.Subfolder = out.Images[0].Subfolder
				img.Type = out.Images[0].Type
				break
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

// sortedNodeIDs orders output node ids the way a person reads them: node
// 9 before node 10, since ComfyUI numbers nodes and does not pad them.
func sortedNodeIDs[T any](outputs map[string]T) []string {
	ids := make([]string, 0, len(outputs))
	for id := range outputs {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		na, erra := strconv.Atoi(a)
		nb, errb := strconv.Atoi(b)
		if erra == nil && errb == nil {
			return cmp.Compare(na, nb)
		}
		return strings.Compare(a, b)
	})
	return ids
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

// joinNonEmpty joins the parts that have something in them, so that an
// unset half of a pair does not leave a dangling separator.
func joinNonEmpty(sep string, parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// expandHome resolves a leading ~/ in a configured path, which is how
// people write paths in a config file.
func expandHome(p string) (string, error) {
	rest, ok := strings.CutPrefix(p, "~/")
	if !ok {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rest), nil
}
