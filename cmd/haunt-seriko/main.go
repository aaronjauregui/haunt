// Command haunt-seriko converts ukagaka shell art (SERIKO's surfaces.txt
// and the images beside it) into a haunt shell.
//
//	haunt-seriko [-o DIR] [-surface N] [-only 0,1,2] <shell-dir-or-nar>
//
// It is a separate binary on purpose. The driver's subject is a character
// on a screen; reading somebody else's archive format is a job you do once,
// before the ghost exists, and it should not be code that ships inside the
// thing that runs on your desktop all day.
package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/aaronjauregui/haunt/internal/seriko"
)

func main() {
	log := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
	if err := run(); err != nil {
		log("haunt-seriko: %v", err)
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("haunt-seriko", flag.ExitOnError)
	out := fs.String("o", "", "write the converted ghost here (default: alongside, named after the shell)")
	surface := fs.Int("surface", 0, "the surface the ghost rests on")
	only := fs.String("only", "", "convert only these surface ids (comma separated), plus whatever they animate")
	name := fs.String("name", "", "the ghost's name (default: from descript.txt)")
	dry := fs.Bool("n", false, "say what would be written without writing it")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, `usage: haunt-seriko [-o DIR] [-surface N] [-only IDS] [-name NAME] [-n] <shell-dir | ghost.nar>

Converts ukagaka shell art into a haunt shell. Reads SERIKO/1.x and 2.0
surfaces.txt, PNG+PNA transparency and colour-keyed art, collisions, and
frame-swap animations.

It converts art, not logic: SHIORI, SSTP and SakuraScript are out of scope
and always will be. What you get is a shell a haunt brain can wear.`)
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("one shell directory or .nar expected")
	}

	target := fs.Arg(0)
	root, cleanup, err := open(target)
	if err != nil {
		return err
	}
	defer cleanup()

	src, err := seriko.OpenDir(root)
	if err != nil {
		return err
	}
	raw, ok := src.Read("surfaces.txt")
	if !ok {
		return fmt.Errorf("no surfaces.txt under %s — is this a shell?", target)
	}

	sh, err := seriko.Parse(raw)
	if err != nil {
		return fmt.Errorf("surfaces.txt: %w", err)
	}
	if b, ok := src.Read("descript.txt"); ok {
		d := seriko.ParseDescript(b)
		sh.Name = d["name"]
	}

	ids, err := parseOnly(*only)
	if err != nil {
		return err
	}
	res, err := seriko.Convert(sh, src, seriko.Options{Default: *surface, Only: ids})
	if err != nil {
		return err
	}

	ghostName := *name
	if ghostName == "" {
		ghostName = sh.Name
	}
	if ghostName == "" {
		ghostName = strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
	}

	dest := *out
	if dest == "" {
		dest = safeDir(ghostName)
	}

	manifest, err := renderManifest(ghostName, res)
	if err != nil {
		return err
	}

	// What happened, before what was written: the notes are the part a
	// person has to read, and burying them under a file list guarantees
	// they will not.
	if len(res.Notes) > 0 {
		fmt.Fprintln(os.Stderr, "\n── what the conversion decided ──")
		for _, n := range res.Notes {
			fmt.Fprintln(os.Stderr, "  · "+n)
		}
	}
	fmt.Fprintf(os.Stderr, "\n%d surface(s), %d clip(s), %d named region(s), %d file(s)\n",
		len(res.Shell.Poses), len(res.Shell.Clips), len(res.Shell.Hits), len(res.Files))

	if *dry {
		fmt.Fprintf(os.Stderr, "\nwould write %s/\n", dest)
		for _, p := range sortedPaths(res.Files) {
			fmt.Fprintf(os.Stderr, "  %s (%d bytes)\n", p, len(res.Files[p]))
		}
		fmt.Fprintln(os.Stderr, "  ghost.toml")
		return nil
	}

	if err := write(dest, manifest, res.Files); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nwrote %s/\n\ntry it:  haunt run %s\n", dest, dest)
	return nil
}

// open resolves the target to a directory of files, unpacking a .nar (which
// is a zip) into a temporary one.
func open(target string) (root string, cleanup func(), err error) {
	info, err := os.Stat(target)
	if err != nil {
		return "", func() {}, err
	}
	if info.IsDir() {
		return target, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "haunt-seriko-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	if err := unzip(target, dir); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("%s: %w (a .nar is a zip; is this one?)", target, err)
	}
	return dir, cleanup, nil
}

// unzip extracts an archive, refusing entries that would escape the
// destination — an archive from the internet is not to be trusted with a
// path.
func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		name := strings.ReplaceAll(f.Name, `\`, "/")
		p := filepath.Join(dest, filepath.Clean("/"+name))
		if !strings.HasPrefix(p, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("entry %q would escape the destination", f.Name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.Create(p)
		if err != nil {
			rc.Close()
			return err
		}
		// Bounded: a zip bomb should cost a message, not the machine.
		const maxEntry = 128 << 20
		_, err = io.Copy(w, io.LimitReader(rc, maxEntry))
		rc.Close()
		w.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// renderManifest writes the ghost.toml a converted shell needs.
func renderManifest(name string, res *seriko.Result) ([]byte, error) {
	var b strings.Builder
	b.WriteString("# Converted from ukagaka shell art by haunt-seriko.\n\n")
	b.WriteString("[ghost]\n")
	b.WriteString(fmt.Sprintf("name = %q\n", name))
	b.WriteString("version = \"0.1.0\"\n")
	// A converted shell has art and no logic, so it runs on the built-in
	// brain — which needs to be allowed to talk to a model and to be
	// spoken to. Declared, not granted: the user still says yes.
	b.WriteString("capabilities = [\"message\", \"llm\"]\n\n")
	b.WriteString("[persona]\n")
	b.WriteString(fmt.Sprintf("system = %q\n\n",
		"You are "+name+", a desktop companion. Reply in one or two short sentences."))

	var shellDoc strings.Builder
	enc := toml.NewEncoder(&shellDoc)
	if err := enc.Encode(map[string]any{"shell": res.Shell}); err != nil {
		return nil, err
	}
	b.WriteString(shellDoc.String())
	return []byte(b.String()), nil
}

func write(dest string, manifest []byte, files map[string][]byte) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	for _, p := range sortedPaths(files) {
		full := filepath.Join(dest, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, files[p], 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dest, "ghost.toml"), manifest, 0o644)
}

func sortedPaths(files map[string][]byte) []string {
	out := make([]string, 0, len(files))
	for p := range files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func parseOnly(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []int
	for _, f := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			return nil, fmt.Errorf("-only: %q is not a surface id", f)
		}
		out = append(out, n)
	}
	return out, nil
}

// safeDir turns a ghost name — often Japanese — into a directory name.
func safeDir(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "converted-shell"
	}
	return s
}
