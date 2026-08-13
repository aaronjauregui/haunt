package testenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHeadlessCompositor(t *testing.T) {
	c := StartT(t)

	if c.Display == "" {
		t.Fatal("no display name")
	}

	out, err := c.Swaymsg("-t", "get_outputs")
	if err != nil {
		t.Fatalf("swaymsg get_outputs: %v", err)
	}
	var outputs []struct {
		Name string `json:"name"`
		Rect struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"rect"`
	}
	if err := json.Unmarshal(out, &outputs); err != nil {
		t.Fatalf("parsing outputs: %v", err)
	}
	if len(outputs) != 1 || outputs[0].Name != "HEADLESS-1" {
		t.Fatalf("unexpected outputs: %+v", outputs)
	}
	if outputs[0].Rect.Width != 1280 || outputs[0].Rect.Height != 720 {
		t.Fatalf("unexpected output size: %+v", outputs[0].Rect)
	}

	shot := filepath.Join(t.TempDir(), "shot.png")
	if err := c.Screenshot(shot); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if fi, err := os.Stat(shot); err != nil || fi.Size() == 0 {
		t.Fatalf("screenshot file missing or empty: %v", err)
	}
}
