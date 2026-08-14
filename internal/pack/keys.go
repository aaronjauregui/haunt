package pack

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KeyDir returns where the author keypair lives:
// HAUNT_CONFIG_DIR if set, else $XDG_CONFIG_HOME/haunt (~/.config/haunt).
func KeyDir() (string, error) {
	if dir := os.Getenv("HAUNT_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "haunt"), nil
}

// GenerateKey creates and stores a new author keypair, failing if one
// already exists. The private key seed is hex in a 0600 file.
func GenerateKey() (ed25519.PublicKey, error) {
	dir, err := KeyDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "author.key")
	if _, err := os.Stat(keyPath); err == nil {
		return nil, fmt.Errorf("author key already exists at %s", keyPath)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	seed := hex.EncodeToString(priv.Seed())
	if err := os.WriteFile(keyPath, []byte(seed+"\n"), 0o600); err != nil {
		return nil, err
	}
	pubPath := filepath.Join(dir, "author.pub")
	if err := os.WriteFile(pubPath, []byte(hex.EncodeToString(pub)+"\n"), 0o644); err != nil {
		return nil, err
	}
	return pub, nil
}

// LoadKey reads the stored author private key.
func LoadKey() (ed25519.PrivateKey, error) {
	dir, err := KeyDir()
	if err != nil {
		return nil, err
	}
	keyPath := filepath.Join(dir, "author.key")
	data, err := os.ReadFile(keyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no author key found; run 'haunt keygen' first")
		}
		return nil, err
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("malformed author key at %s", keyPath)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// Fingerprint renders a public key for human display.
func Fingerprint(pub ed25519.PublicKey) string {
	h := hex.EncodeToString(pub)
	return h[:16] + "…" + h[len(h)-4:]
}
