package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeComfy implements the three-endpoint ComfyUI flow: /prompt accepts a
// graph, /history reports completion after one poll, /view serves bytes.
func fakeComfy(t *testing.T, imageBytes []byte) *httptest.Server {
	t.Helper()
	polled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Prompt map[string]struct {
				ClassType string         `json:"class_type"`
				Inputs    map[string]any `json:"inputs"`
			} `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		// The graph must carry our checkpoint and prompt text.
		if req.Prompt["1"].Inputs["ckpt_name"] != "test.safetensors" {
			http.Error(w, "wrong checkpoint", 400)
			return
		}
		if req.Prompt["2"].Inputs["text"] != "a ghost" {
			http.Error(w, "wrong prompt", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"prompt_id": "p1", "node_errors": map[string]any{}})
	})
	mux.HandleFunc("/history/p1", func(w http.ResponseWriter, r *http.Request) {
		if !polled {
			// First poll: still running.
			polled = true
			w.Write([]byte("{}"))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"p1": map[string]any{
				"status": map[string]any{"completed": true, "status_str": "success"},
				"outputs": map[string]any{
					"7": map[string]any{"images": []map[string]string{
						{"filename": "haunt_00001_.png", "subfolder": "", "type": "output"},
					}},
				},
			},
		})
	})
	mux.HandleFunc("/view", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filename") != "haunt_00001_.png" {
			http.NotFound(w, r)
			return
		}
		w.Write(imageBytes)
	})
	return httptest.NewServer(mux)
}

func TestComfyGenerate(t *testing.T) {
	payload := []byte("png-bytes")
	srv := fakeComfy(t, payload)
	defer srv.Close()

	s := NewService(&Config{
		Image: ImageConfig{
			Backend: "comfyui",
			BaseURL: srv.URL,
			Model:   "test.safetensors",
		},
		Budget: BudgetConfig{ImageRequestsPerHour: 5},
	}, nil)
	got, err := s.GenerateImage(context.Background(), "a ghost")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("image bytes = %q", got)
	}
}

func TestUnknownBackendRejected(t *testing.T) {
	s := NewService(&Config{
		Image:  ImageConfig{Backend: "banana", BaseURL: "http://x"},
		Budget: BudgetConfig{ImageRequestsPerHour: 1},
	}, nil)
	if _, err := s.GenerateImage(context.Background(), "x"); err == nil {
		t.Fatal("unknown backend should error")
	}
}

func TestParseSize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		w, h int
	}{
		{"", 512, 512}, {"640x480", 640, 480}, {"junk", 512, 512}, {"0x10", 512, 512},
	} {
		if w, h := parseSize(tc.in, 512); w != tc.w || h != tc.h {
			t.Errorf("parseSize(%q) = %dx%d, want %dx%d", tc.in, w, h, tc.w, tc.h)
		}
	}
}

// A minimal workflow in the shape ComfyUI's "Save (API format)" produces:
// an object of nodes, each with a class_type and inputs, with the
// placeholders where the varying parts go.
const sampleWorkflow = `{
  "4": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": "sdxl.safetensors"}},
  "6": {"class_type": "CLIPTextEncode", "inputs": {"clip": ["4", 1], "text": "{{prompt}}"}},
  "7": {"class_type": "CLIPTextEncode", "inputs": {"clip": ["4", 1], "text": "ugly, {{negative}}"}},
  "5": {"class_type": "EmptyLatentImage",
        "inputs": {"width": "{{width}}", "height": "{{height}}", "batch_size": 1}},
  "3": {"class_type": "KSampler",
        "inputs": {"model": ["4", 0], "positive": ["6", 0], "negative": ["7", 0],
                   "latent_image": ["5", 0], "seed": "{{seed}}", "steps": 20}},
  "8": {"class_type": "VAEDecode", "inputs": {"samples": ["3", 0], "vae": ["4", 2]}},
  "9": {"class_type": "SaveImage", "inputs": {"images": ["8", 0]}}
}`

// writeWorkflow puts a graph in a file and returns the path.
func writeWorkflow(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "draw.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// A workflow is checked when the config is read, so the ways an export
// goes wrong are all startup errors naming the file.
func TestWorkflowValidation(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"bad JSON", "{not json", "draw.json"},
		{"editor format", `{"nodes": [], "links": []}`, "Save (API format)"},
		{"no prompt", `{"9": {"class_type": "SaveImage", "inputs": {"images": ["8", 0]}}}`, "{{prompt}}"},
		{"no output", `{"6": {"class_type": "CLIPTextEncode", "inputs": {"text": "{{prompt}}"}}}`, "SaveImage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Image: ImageConfig{
				Backend: "comfyui", BaseURL: "http://x", Workflow: writeWorkflow(t, tc.body),
			}}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("a broken workflow was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}

	missing := &Config{Image: ImageConfig{Workflow: filepath.Join(t.TempDir(), "absent.json")}}
	if err := missing.Validate(); err == nil {
		t.Error("a workflow that is not there was accepted")
	}
	if err := (&Config{}).Validate(); err != nil {
		t.Errorf("no workflow should be no error: %v", err)
	}
}

// Substitution happens on the decoded graph, so a placeholder standing
// alone becomes a number and a prompt full of punctuation stays one
// string.
func TestWorkflowSubstitution(t *testing.T) {
	cfg := &ImageConfig{
		Backend: "comfyui", BaseURL: "http://x", Size: "768x512",
		Negative: "watermark", Workflow: writeWorkflow(t, sampleWorkflow),
	}
	prompt := "a \"whale\", reading\na book"
	run, err := comfyPlan(cfg, DrawRequest{Prompt: prompt, Negative: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if run.seed == 0 {
		t.Error("a graph using {{seed}} should report the seed it was given")
	}
	if run.width != 768 || run.height != 512 {
		t.Errorf("size = %dx%d", run.width, run.height)
	}
	if run.negative != "watermark, text" {
		t.Errorf("negative = %q, want the configured one joined to the ghost's", run.negative)
	}

	// The graph goes over the wire as JSON, so check it the way ComfyUI
	// sees it: numbers where numbers belong.
	encoded, err := json.Marshal(run.graph)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]struct {
		Inputs map[string]any `json:"inputs"`
	}
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	if got := back["6"].Inputs["text"]; got != prompt {
		t.Errorf("prompt = %#v, want the prompt verbatim", got)
	}
	if got := back["7"].Inputs["text"]; got != "ugly, watermark, text" {
		t.Errorf("negative text = %#v", got)
	}
	for node, field := range map[string]string{"5": "width", "3": "seed"} {
		if _, ok := back[node].Inputs[field].(float64); !ok {
			t.Errorf("%s is %#v, want a JSON number", field, back[node].Inputs[field])
		}
	}

	// Reading the workflow leaves it as it was read: the next picture gets
	// its own substitution, not the last one's.
	wf, err := cfg.loadWorkflow()
	if err != nil {
		t.Fatal(err)
	}
	if got := wf.graph["6"].(map[string]any)["inputs"].(map[string]any)["text"]; got != "{{prompt}}" {
		t.Errorf("the parsed workflow was substituted in place: %#v", got)
	}
}

// A graph with no {{seed}} in it has no seed to report: the picture is the
// workflow's to reproduce, not haunt's.
func TestWorkflowWithoutSeed(t *testing.T) {
	body := `{"6": {"class_type": "CLIPTextEncode", "inputs": {"text": "{{prompt}}"}},
	          "9": {"class_type": "PreviewImage", "inputs": {"images": ["8", 0]}}}`
	cfg := &ImageConfig{Backend: "comfyui", BaseURL: "http://x", Workflow: writeWorkflow(t, body)}
	run, err := comfyPlan(cfg, DrawRequest{Prompt: "a ghost"})
	if err != nil {
		t.Fatal(err)
	}
	if run.seed != 0 {
		t.Errorf("seed = %d, want none reported", run.seed)
	}
	// This graph carries its own dimensions, so there is no size haunt
	// chose and none to report: saying 1024 here would name a number
	// nothing drew at.
	if run.width != 0 || run.height != 0 {
		t.Errorf("size = %dx%d, want none reported", run.width, run.height)
	}
}

// A workflow that does ask for the dimensions is drawn at the configured
// size, or at the one a workflow is assumed to want when none is set.
func TestWorkflowSize(t *testing.T) {
	body := `{"6": {"class_type": "CLIPTextEncode", "inputs": {"text": "{{prompt}}"}},
	          "5": {"class_type": "EmptyLatentImage",
	                "inputs": {"width": "{{width}}", "height": "{{height}}"}},
	          "9": {"class_type": "PreviewImage", "inputs": {"images": ["8", 0]}}}`
	cfg := &ImageConfig{Backend: "comfyui", BaseURL: "http://x", Workflow: writeWorkflow(t, body)}
	run, err := comfyPlan(cfg, DrawRequest{Prompt: "a ghost"})
	if err != nil {
		t.Fatal(err)
	}
	if run.width != comfyWorkflowSize || run.height != comfyWorkflowSize {
		t.Errorf("size = %dx%d, want %d square", run.width, run.height, comfyWorkflowSize)
	}
}

// The built-in graph still draws, and the ghost's negative joins the
// configured one rather than replacing the default wholesale.
func TestBuiltinGraphNegative(t *testing.T) {
	cfg := &ImageConfig{Backend: "comfyui", BaseURL: "http://x", Model: "test.safetensors"}
	run, err := comfyPlan(cfg, DrawRequest{Prompt: "a ghost", Negative: "photorealistic"})
	if err != nil {
		t.Fatal(err)
	}
	// What the package asks to keep out is added to the default, not put
	// in its place: a ghost that dislikes photographs still wants the
	// blur and the watermarks kept out.
	want := "blurry, text, watermark, low quality, photorealistic"
	if run.negative != want {
		t.Errorf("negative = %q, want %q", run.negative, want)
	}
	if run.seed == 0 {
		t.Error("the built-in graph always knows its seed")
	}
	text := run.graph["3"].(map[string]any)["inputs"].(map[string]any)["text"]
	if text != want {
		t.Errorf("negative node text = %#v", text)
	}

	// A negative the person configured is joined the same way.
	cfg.Negative = "low contrast"
	run, err = comfyPlan(cfg, DrawRequest{Prompt: "a ghost", Negative: "photorealistic"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "low contrast, photorealistic"; run.negative != want {
		t.Errorf("negative = %q, want %q", run.negative, want)
	}
}

// The lowest-numbered output node wins, and 9 comes before 10.
func TestSortedNodeIDs(t *testing.T) {
	got := sortedNodeIDs(map[string]int{"10": 0, "9": 0, "2": 0, "save": 0})
	want := []string{"2", "9", "10", "save"}
	if !slices.Equal(got, want) {
		t.Errorf("sortedNodeIDs = %v, want %v", got, want)
	}
}
