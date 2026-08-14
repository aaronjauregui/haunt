package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/ghost"
)

// TestNewScaffoldBuilds scaffolds a ghost, points its go.mod at this
// checkout, and verifies the brain compiles to WASM and the manifest loads.
func TestNewScaffoldBuilds(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	if err := cmdNew([]string{"specter"}); err != nil {
		t.Fatal(err)
	}
	if err := cmdNew([]string{"specter"}); err == nil {
		t.Error("expected error scaffolding over an existing directory")
	}
	if err := cmdNew([]string{"Bad Name"}); err == nil {
		t.Error("expected error for invalid name")
	}

	// Point the scaffold's replace directive at this checkout.
	gomodPath := filepath.Join(dir, "specter", "brain", "go.mod")
	gomod, err := os.ReadFile(gomodPath)
	if err != nil {
		t.Fatal(err)
	}
	fixed := strings.ReplaceAll(string(gomod), "=> ../../..", "=> "+repo)
	if err := os.WriteFile(gomodPath, []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}

	brainDir := filepath.Join(dir, "specter", "brain")
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = brainDir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("tidying scaffolded brain: %v: %s", err, out)
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "specter", "brain.wasm"), ".")
	cmd.Dir = brainDir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building scaffolded brain: %v: %s", err, out)
	}

	m, err := ghost.LoadManifest(filepath.Join(dir, "specter"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Ghost.Name != "specter" || m.Brain.Wasm != "brain.wasm" {
		t.Errorf("manifest = %+v", m)
	}
}
