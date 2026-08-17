package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aaronjauregui/haunt/internal/artimport"
)

// cmdShell handles `haunt shell <subcommand>`.
func cmdShell(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("shell: expected a subcommand (init)")
	}
	switch args[0] {
	case "init":
		return cmdShellInit(args[1:])
	default:
		return fmt.Errorf("shell: unknown subcommand %q", args[0])
	}
}

// cmdShellInit turns a layered drawing into a shell: cropped PNGs and the
// TOML that arranges them.
//
// The point is that the drawing already says everything the format needs.
// An artist with an `eyes` group holding `open` and `closed` has described
// a layer with two variants; asking them to also write that out in TOML,
// and to crop every layer by hand so damage tracking stays cheap, is asking
// them to say it twice — and the second saying is the one they will get
// wrong.
func cmdShellInit(args []string) error {
	fs := flag.NewFlagSet("shell init", flag.ExitOnError)
	dir := fs.String("C", ".", "ghost directory to write into")
	art := fs.String("art", "shell", "subdirectory for the generated PNGs")
	write := fs.Bool("write", false, "replace the [shell] section of the ghost's ghost.toml")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("shell init: exactly one .ora file expected")
	}
	src := fs.Arg(0)
	if ext := strings.ToLower(filepath.Ext(src)); ext != ".ora" {
		return fmt.Errorf("shell init: %s is not a .ora file — export one from Krita, GIMP, MyPaint or Pencil2D", src)
	}

	stack, err := artimport.ReadORA(src)
	if err != nil {
		return err
	}
	res, err := artimport.Convert(stack, *art)
	if err != nil {
		return err
	}

	for rel, data := range res.Files {
		path := filepath.Join(*dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}

	for _, n := range res.Notes {
		fmt.Fprintln(os.Stderr, "haunt: "+n)
	}
	fmt.Fprintf(os.Stderr, "haunt: wrote %d layer images to %s\n",
		len(res.Files), filepath.Join(*dir, *art))

	toml := res.TOML()
	if !*write {
		// To stdout by default: the section is small, and a person pasting
		// it into their manifest can see what they are agreeing to. -write
		// is for when they have seen it once.
		fmt.Print(toml)
		fmt.Fprintln(os.Stderr, "haunt: add that to your ghost.toml, or re-run with -write")
		return nil
	}

	manifest := filepath.Join(*dir, "ghost.toml")
	existing, err := os.ReadFile(manifest)
	if err != nil {
		return fmt.Errorf("shell init -write: %w", err)
	}
	updated, err := replaceShellSection(string(existing), toml)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	if err := os.WriteFile(manifest, []byte(updated), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "haunt: updated %s\n", manifest)
	return nil
}

// replaceShellSection swaps the [shell] section of a manifest for a new
// one, leaving everything else — identity, capabilities, persona, triggers
// — exactly where the author put it. Re-running the import must not cost
// somebody their persona.
func replaceShellSection(manifest, section string) (string, error) {
	lines := strings.Split(manifest, "\n")
	var out []string
	skipping := false
	replaced := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		isShell := trimmed == "[shell]" || strings.HasPrefix(trimmed, "[shell.") ||
			strings.HasPrefix(trimmed, "[[shell.")
		switch {
		case isShell:
			if !replaced {
				out = append(out, strings.TrimRight(section, "\n"))
				replaced = true
			}
			skipping = true
			continue
		case skipping && strings.HasPrefix(trimmed, "["):
			// A section that is not the shell's: stop skipping and keep it.
			skipping = false
		case skipping:
			continue
		}
		out = append(out, line)
	}
	if !replaced {
		// One blank line before it, however the file ended.
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
		out = append(out, "", strings.TrimRight(section, "\n"))
	}
	return strings.Join(out, "\n"), nil
}
