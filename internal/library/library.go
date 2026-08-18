// Package library manages installed ghosts: extraction into the data
// directory, author-key pinning (trust on first use), capability grants,
// and the "current ghost" selection.
//
// Layout under $XDG_DATA_HOME/haunt:
//
//	ghosts/<name>/pkg/         extracted package content
//	ghosts/<name>/state.db     the ghost's persistent memory
//	ghosts/<name>/grants.toml  pinned author key + granted capabilities
//	current                    name of the selected ghost
package library

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/pack"
)

// Root returns the library root, honoring HAUNT_DATA_DIR for tests.
func Root() (string, error) {
	if dir := os.Getenv("HAUNT_DATA_DIR"); dir != "" {
		return dir, nil
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "haunt"), nil
}

// GhostDir returns the per-ghost directory (state, grants, pkg).
func GhostDir(name string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "ghosts", name), nil
}

// Grants records the user's decisions about a ghost.
type Grants struct {
	// AuthorKey is the hex Ed25519 key pinned at first install.
	AuthorKey string `toml:"author_key"`
	// Capabilities the user has granted.
	Capabilities []string `toml:"capabilities"`
	// Refused are capabilities the user has said no to for good, when the
	// ghost asked in the moment. Without somewhere to write that down, "no"
	// means "ask me again in a minute", which is how a companion becomes a
	// nuisance.
	Refused []string `toml:"refused"`
}

// Refused reports whether the user has told this ghost to stop asking.
func (g *Grants) IsRefused(cap string) bool {
	for _, c := range g.Refused {
		if c == cap {
			return true
		}
	}
	return false
}

// Allow grants a capability and clears any standing refusal: saying yes is
// also saying "you may ask about this again".
func (g *Grants) Allow(cap string) {
	if !g.Granted(cap) {
		g.Capabilities = append(g.Capabilities, cap)
	}
	g.Refused = without(g.Refused, cap)
}

// Refuse records a refusal and withdraws the grant if there was one.
func (g *Grants) Refuse(cap string) {
	g.Capabilities = without(g.Capabilities, cap)
	if !g.IsRefused(cap) {
		g.Refused = append(g.Refused, cap)
	}
}

// Withdraw removes a grant without recording a refusal — `haunt perms
// revoke`, which says nothing about being asked again.
func (g *Grants) Withdraw(cap string) {
	g.Capabilities = without(g.Capabilities, cap)
}

func without(list []string, s string) []string {
	var out []string
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

// Granted reports whether cap is in the grant list.
func (g *Grants) Granted(cap string) bool {
	for _, c := range g.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// LoadGrants reads a ghost's grants; a missing file returns empty grants.
func LoadGrants(name string) (*Grants, error) {
	dir, err := GhostDir(name)
	if err != nil {
		return nil, err
	}
	var g Grants
	data, err := os.ReadFile(filepath.Join(dir, "grants.toml"))
	if os.IsNotExist(err) {
		return &g, nil
	}
	if err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(data, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// SaveGrants persists a ghost's grants.
func SaveGrants(name string, g *Grants) error {
	dir, err := GhostDir(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(g); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "grants.toml"), buf.Bytes(), 0o644)
}

// InstallRequest is a verified package plus the user's consent decisions.
type InstallRequest struct {
	Package *pack.Package
	// GrantAll grants every capability the manifest requests. The CLI sets
	// this after prompting (or via -yes).
	GrantAll bool
}

// InstallResult reports what happened.
type InstallResult struct {
	Name         string
	Version      string
	Capabilities []string
	AuthorKey    ed25519.PublicKey
	FirstInstall bool
}

// Install extracts a verified package into the library. The author key must
// match the pinned key on reinstall (TOFU); a mismatch fails hard.
func Install(req InstallRequest) (*InstallResult, error) {
	// Extract to a staging dir first so we can read the manifest before
	// committing.
	staging, err := os.MkdirTemp("", "haunt-install-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	if err := req.Package.Extract(staging); err != nil {
		return nil, err
	}
	m, err := ghost.LoadManifest(staging)
	if err != nil {
		return nil, fmt.Errorf("package manifest: %w", err)
	}
	name := m.Ghost.Name

	keyHex := hex.EncodeToString(req.Package.AuthorKey)
	grants, err := LoadGrants(name)
	if err != nil {
		return nil, err
	}
	first := grants.AuthorKey == ""
	if !first && grants.AuthorKey != keyHex {
		return nil, fmt.Errorf(
			"ghost %q is pinned to author %s but this package is signed by %s; refusing update",
			name, grants.AuthorKey[:16], keyHex[:16])
	}

	dir, err := GhostDir(name)
	if err != nil {
		return nil, err
	}
	pkgDir := filepath.Join(dir, "pkg")
	// Replace atomically enough for v1: clear and move in.
	if err := os.RemoveAll(pkgDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(pkgDir), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(staging, pkgDir); err != nil {
		// Cross-device fallback: extract again directly.
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			return nil, err
		}
		if err := req.Package.Extract(pkgDir); err != nil {
			return nil, err
		}
	}

	grants.AuthorKey = keyHex
	if req.GrantAll {
		grants.Capabilities = m.Ghost.Capabilities
	}
	if err := SaveGrants(name, grants); err != nil {
		return nil, err
	}

	return &InstallResult{
		Name:         name,
		Version:      m.Ghost.Version,
		Capabilities: m.Ghost.Capabilities,
		AuthorKey:    req.Package.AuthorKey,
		FirstInstall: first,
	}, nil
}

// List returns the installed ghost names, sorted.
func List() ([]string, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(root, "ghosts"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "ghosts", e.Name(), "pkg", "ghost.toml")); err == nil {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// Remove deletes an installed ghost, including its state and grants.
func Remove(name string) error {
	dir, err := GhostDir(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "pkg", "ghost.toml")); err != nil {
		return fmt.Errorf("ghost %q is not installed", name)
	}
	if cur, _ := Current(); cur == name {
		root, _ := Root()
		os.Remove(filepath.Join(root, "current"))
	}
	return os.RemoveAll(dir)
}

// SetCurrent selects the default ghost for haunt run.
func SetCurrent(name string) error {
	dir, err := GhostDir(name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "pkg", "ghost.toml")); err != nil {
		return fmt.Errorf("ghost %q is not installed", name)
	}
	root, err := Root()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "current"), []byte(name+"\n"), 0o644)
}

// Current returns the selected ghost name, or "" if none.
func Current() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(root, "current"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// PkgDir returns the content directory for an installed ghost.
func PkgDir(name string) (string, error) {
	dir, err := GhostDir(name)
	if err != nil {
		return "", err
	}
	pkgDir := filepath.Join(dir, "pkg")
	if _, err := os.Stat(filepath.Join(pkgDir, "ghost.toml")); err != nil {
		return "", fmt.Errorf("ghost %q is not installed", name)
	}
	return pkgDir, nil
}
