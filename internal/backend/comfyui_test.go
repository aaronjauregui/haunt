package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
