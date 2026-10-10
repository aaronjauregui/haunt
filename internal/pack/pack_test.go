package pack

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func testGhostDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"ghost.toml":    "[ghost]\nname = \"spook\"\n\n[brain]\nwasm = \"brain.wasm\"\n",
		"brain.wasm":    "\x00asm-fake",
		"art/shell.png": "not-really-png",
		// A persona ghost carries its routines as skills.
		"skills/tidy/SKILL.md":           "---\nname: tidy\n---\n",
		"skills/tidy/references/desk.md": "reference",
		".hidden":                        "excluded",
		"brain/main.go":                  "excluded build dir",
	}
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPackSignVerifyExtract(t *testing.T) {
	dir := testGhostDir(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := Write(&buf, dir, priv); err != nil {
		t.Fatal(err)
	}

	p, err := Read(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !p.AuthorKey.Equal(priv.Public().(ed25519.PublicKey)) {
		t.Error("author key mismatch")
	}

	dest := t.TempDir()
	if err := p.Extract(dest); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ghost.toml", "brain.wasm", "art/shell.png",
		"skills/tidy/SKILL.md", "skills/tidy/references/desk.md"} {
		if _, err := os.Stat(filepath.Join(dest, want)); err != nil {
			t.Errorf("extracted package missing %s", want)
		}
	}
	for _, exclude := range []string{".hidden", "brain/main.go"} {
		if _, err := os.Stat(filepath.Join(dest, exclude)); err == nil {
			t.Errorf("package should not contain %s", exclude)
		}
	}
}

func TestTamperedPackageRejected(t *testing.T) {
	dir := testGhostDir(t)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	var buf bytes.Buffer
	if err := Write(&buf, dir, priv); err != nil {
		t.Fatal(err)
	}
	// Flip a byte inside the content region.
	data := buf.Bytes()
	data[1024] ^= 0xFF
	if _, err := Read(bytes.NewReader(data)); err == nil {
		t.Fatal("tampered package passed verification")
	}
}

func TestDeterministicContent(t *testing.T) {
	dir := testGhostDir(t)
	a, err := BuildContent(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildContent(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("content archive not deterministic")
	}
}

func TestKeygenRoundtrip(t *testing.T) {
	t.Setenv("HAUNT_CONFIG_DIR", t.TempDir())
	pub, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateKey(); err == nil {
		t.Fatal("second keygen should refuse to overwrite")
	}
	priv, err := LoadKey()
	if err != nil {
		t.Fatal(err)
	}
	if !pub.Equal(priv.Public().(ed25519.PublicKey)) {
		t.Error("loaded key does not match generated public key")
	}
}
