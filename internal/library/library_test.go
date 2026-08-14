package library

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/aaronjauregui/haunt/internal/pack"
)

func makePackage(t *testing.T, name string, key ed25519.PrivateKey, caps string) *pack.Package {
	t.Helper()
	dir := t.TempDir()
	manifest := "[ghost]\nname = \"" + name + "\"\nversion = \"1.0.0\"\n" + caps + "\n[brain]\nwasm = \"brain.wasm\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ghost.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "brain.wasm"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := pack.Write(&buf, dir, key); err != nil {
		t.Fatal(err)
	}
	p, err := pack.Read(&buf)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestInstallListSwitchRemove(t *testing.T) {
	t.Setenv("HAUNT_DATA_DIR", t.TempDir())
	_, key, _ := ed25519.GenerateKey(rand.Reader)

	p := makePackage(t, "spook", key, "capabilities = [\"message\"]\n")
	res, err := Install(InstallRequest{Package: p, GrantAll: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.FirstInstall || res.Name != "spook" || res.Version != "1.0.0" {
		t.Errorf("result = %+v", res)
	}

	names, err := List()
	if err != nil || len(names) != 1 || names[0] != "spook" {
		t.Fatalf("List() = %v, %v", names, err)
	}

	grants, err := LoadGrants("spook")
	if err != nil {
		t.Fatal(err)
	}
	if !grants.Granted("message") {
		t.Error("message capability not granted")
	}
	if grants.Granted("llm") {
		t.Error("ungranted capability reported as granted")
	}

	if err := SetCurrent("spook"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := Current(); cur != "spook" {
		t.Errorf("current = %q", cur)
	}
	if _, err := PkgDir("spook"); err != nil {
		t.Error(err)
	}

	if err := Remove("spook"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := Current(); cur != "" {
		t.Errorf("current after remove = %q", cur)
	}
	if err := Remove("spook"); err == nil {
		t.Error("removing a missing ghost should fail")
	}
}

func TestTOFUPinsAuthorKey(t *testing.T) {
	t.Setenv("HAUNT_DATA_DIR", t.TempDir())
	_, keyA, _ := ed25519.GenerateKey(rand.Reader)
	_, keyB, _ := ed25519.GenerateKey(rand.Reader)

	if _, err := Install(InstallRequest{Package: makePackage(t, "spook", keyA, "")}); err != nil {
		t.Fatal(err)
	}
	// Same author: update allowed.
	if _, err := Install(InstallRequest{Package: makePackage(t, "spook", keyA, "")}); err != nil {
		t.Fatalf("reinstall by same author failed: %v", err)
	}
	// Different author: refused.
	if _, err := Install(InstallRequest{Package: makePackage(t, "spook", keyB, "")}); err == nil {
		t.Fatal("install by different author should be refused")
	}
}

func TestGrantsSurviveUngrantedUpdate(t *testing.T) {
	t.Setenv("HAUNT_DATA_DIR", t.TempDir())
	_, key, _ := ed25519.GenerateKey(rand.Reader)

	p := makePackage(t, "spook", key, "capabilities = [\"message\"]\n")
	if _, err := Install(InstallRequest{Package: p, GrantAll: true}); err != nil {
		t.Fatal(err)
	}
	// Update without re-granting: existing grants stay.
	if _, err := Install(InstallRequest{Package: p, GrantAll: false}); err != nil {
		t.Fatal(err)
	}
	grants, err := LoadGrants("spook")
	if err != nil {
		t.Fatal(err)
	}
	if !grants.Granted("message") {
		t.Error("grants lost on update")
	}
}
