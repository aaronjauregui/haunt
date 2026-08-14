// Package pack implements the ghost package format: a .haunt file is an
// uncompressed outer tar holding the content archive, the author's Ed25519
// public key, and a signature over the content bytes.
//
//	name.haunt
//	├── ghost.tgz   gzipped tar of the ghost directory
//	├── author.pub  32-byte Ed25519 public key
//	└── sig         64-byte Ed25519 signature of ghost.tgz
//
// Signing is by the ghost author; installers verify the signature and pin
// the author key per ghost name (trust on first use).
package pack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	entryContent = "ghost.tgz"
	entryPubKey  = "author.pub"
	entrySig     = "sig"

	// MaxUnpackedSize caps extraction to keep hostile archives from
	// filling the disk.
	MaxUnpackedSize = 256 << 20
)

// BuildContent produces the deterministic content archive (ghost.tgz bytes)
// for a ghost directory: regular files only, sorted paths, fixed metadata.
func BuildContent(dir string) ([]byte, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			// Skip VCS and the brain's build directory; the shipped
			// artifact is brain.wasm.
			if path != dir && (strings.HasPrefix(name, ".") || name == "brain") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)
	for _, path := range files {
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		hdr := &tar.Header{
			Name:    filepath.ToSlash(rel),
			Mode:    0o644,
			Size:    int64(len(data)),
			ModTime: time.Unix(0, 0), // deterministic
			Format:  tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Write assembles a signed .haunt package from a ghost directory.
func Write(w io.Writer, dir string, key ed25519.PrivateKey) error {
	content, err := BuildContent(dir)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(key, content)
	pub := key.Public().(ed25519.PublicKey)

	tw := tar.NewWriter(w)
	for _, e := range []struct {
		name string
		data []byte
	}{
		{entryContent, content},
		{entryPubKey, pub},
		{entrySig, sig},
	} {
		hdr := &tar.Header{
			Name:    e.name,
			Mode:    0o644,
			Size:    int64(len(e.data)),
			ModTime: time.Unix(0, 0),
			Format:  tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(e.data); err != nil {
			return err
		}
	}
	return tw.Close()
}

// Package is a parsed, signature-verified .haunt file.
type Package struct {
	// AuthorKey is the Ed25519 public key that signed the content.
	AuthorKey ed25519.PublicKey
	content   []byte
}

// Read parses a .haunt package and verifies its signature. It does NOT
// decide whether the author is trusted; that is the installer's job.
func Read(r io.Reader) (*Package, error) {
	tr := tar.NewReader(r)
	parts := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading package: %w", err)
		}
		if hdr.Size > MaxUnpackedSize {
			return nil, fmt.Errorf("package entry %q too large", hdr.Name)
		}
		data, err := io.ReadAll(io.LimitReader(tr, MaxUnpackedSize+1))
		if err != nil {
			return nil, err
		}
		parts[hdr.Name] = data
	}
	content, ok := parts[entryContent]
	if !ok {
		return nil, fmt.Errorf("package has no %s", entryContent)
	}
	pub, ok := parts[entryPubKey]
	if !ok || len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("package has no valid %s", entryPubKey)
	}
	sig, ok := parts[entrySig]
	if !ok || len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("package has no valid %s", entrySig)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), content, sig) {
		return nil, fmt.Errorf("signature verification failed")
	}
	return &Package{AuthorKey: ed25519.PublicKey(pub), content: content}, nil
}

// Extract unpacks the verified content into destDir, refusing path escapes
// and non-regular files.
func (p *Package) Extract(destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(p.content))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("package contains non-regular file %q", hdr.Name)
		}
		name := filepath.FromSlash(hdr.Name)
		if filepath.IsAbs(name) || !filepath.IsLocal(name) {
			return fmt.Errorf("package entry %q escapes the install directory", hdr.Name)
		}
		total += hdr.Size
		if total > MaxUnpackedSize {
			return fmt.Errorf("package exceeds %d bytes unpacked", int64(MaxUnpackedSize))
		}
		dest := filepath.Join(destDir, name)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, io.LimitReader(tr, hdr.Size)); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
}
