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

// `haunt perms revoke` is recorded whether or not there was a grant to
// take, since a local model reads without one; a yes, from `perms grant`
// or the balloon, is the way back.
func TestWithdrawRecordsAndAllowClears(t *testing.T) {
	g := &Grants{Capabilities: []string{"files"}}
	g.Withdraw("files")
	g.Withdraw("web") // never granted
	g.Withdraw("web") // and recorded once
	if g.Granted("files") {
		t.Error("withdrawing left the grant")
	}
	if !g.IsWithdrawn("files") || !g.IsWithdrawn("web") || len(g.Withdrawn) != 2 {
		t.Errorf("withdrawn = %v, want files and web once each", g.Withdrawn)
	}
	g.Refuse("web")
	if !g.IsRefused("web") || !g.IsWithdrawn("web") {
		t.Errorf("refusing a withdrawn capability: refused %v, withdrawn %v", g.Refused, g.Withdrawn)
	}
	g.Allow("files")
	g.Allow("web")
	if !g.Granted("files") || !g.Granted("web") {
		t.Error("allowing did not grant")
	}
	if len(g.Withdrawn) != 0 || len(g.Refused) != 0 {
		t.Errorf("allowing left withdrawn %v, refused %v", g.Withdrawn, g.Refused)
	}
}

// The withdrawn list survives a save, and a grants file written before it
// existed reads as nothing withdrawn.
func TestWithdrawnRoundTrip(t *testing.T) {
	t.Setenv("HAUNT_DATA_DIR", t.TempDir())
	g := &Grants{AuthorKey: "ab", Capabilities: []string{"llm"}}
	g.Withdraw("files")
	if err := SaveGrants("wisp", g); err != nil {
		t.Fatal(err)
	}
	back, err := LoadGrants("wisp")
	if err != nil {
		t.Fatal(err)
	}
	if !back.IsWithdrawn("files") || !back.Granted("llm") || back.IsWithdrawn("llm") {
		t.Errorf("round trip = %+v", back)
	}

	dir, err := GhostDir("spook")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := "author_key = \"ab\"\ncapabilities = [\"llm\"]\nrefused = [\"web\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "grants.toml"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	older, err := LoadGrants("spook")
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Withdrawn) != 0 || !older.IsRefused("web") || !older.Granted("llm") {
		t.Errorf("a file without the list read as %+v", older)
	}
}
