package seriko

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/aaronjauregui/haunt/internal/shell"
)

// The conversion is only worth anything if the driver can load what comes
// out. This is the test that would have caught a manifest that referred to
// files it never wrote, or a variant with no art behind it.
func TestConvertedShellLoads(t *testing.T) {
	sh := parse(t, `surface0
{
0interval,sometimes
0pattern0,101,5,overlay,20,30
0pattern1,102,5,overlay,20,30
0pattern2,-1,10,overlay,20,30

collision0,10,20,40,60,head
collision1,15,25,20,30,face
}

surface1
{
0interval,runonce
0pattern0,113,10,base
}
`)
	src := mem{
		"surface0.png":   pngOf(t, 60, 90, green, skin),
		"surface1.png":   pngOf(t, 60, 90, green, skin),
		"surface101.png": pngOf(t, 12, 8, green, skin),
		"surface102.png": pngOf(t, 12, 8, green, skin),
		"surface113.png": pngOf(t, 60, 90, green, skin),
	}
	res, err := Convert(sh, src, Options{})
	if err != nil {
		t.Fatal(err)
	}

	// Write it out exactly as the tool does.
	dir := t.TempDir()
	for p, b := range res.Files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Round-trip the manifest through TOML too: the encoder is part of the
	// path, and a shape it cannot express would only show up here.
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(map[string]any{"shell": res.Shell}); err != nil {
		t.Fatal(err)
	}
	var back struct {
		Shell shell.Manifest `toml:"shell"`
	}
	if err := toml.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("the manifest did not survive TOML: %v\n%s", err, buf.String())
	}

	loaded, err := shell.Load(dir, back.Shell)
	if err != nil {
		t.Fatalf("the driver could not load the converted shell: %v\n%s", err, buf.String())
	}

	// The parts a person would notice.
	if w, h := loaded.Sprite().Size(); w != 60 || h != 90 {
		t.Errorf("shell is %dx%d, want the surface's size", w, h)
	}
	if !loaded.SetPose("s1") {
		t.Error("pose s1 is missing")
	}
	if got := loaded.RegionAt(12, 22); got != "head" {
		t.Errorf("RegionAt inside the head box = %q", got)
	}
	// Overlapping rectangles resolve in declaration order, which is what
	// SERIKO means by them: the face is inside the head.
	if got := loaded.RegionAt(100, 100); got != "" {
		t.Errorf("RegionAt outside every box = %q, want nothing", got)
	}
	if len(loaded.Clips()) != 2 {
		t.Errorf("%d clips survived the round trip, want 2", len(loaded.Clips()))
	}
}
