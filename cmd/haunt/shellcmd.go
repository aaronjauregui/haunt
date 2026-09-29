package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/aaronjauregui/haunt/internal/artimport"
	"github.com/aaronjauregui/haunt/internal/shell"
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
	scale := fs.Float64("scale", 0, "how large to draw the art, e.g. 0.25 for a quarter of its pixel size")
	write := fs.Bool("write", false, "replace the [shell] section of the ghost's ghost.toml")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("shell init: exactly one .ora file expected")
	}
	scaleSet := false
	fs.Visit(func(f *flag.Flag) { scaleSet = scaleSet || f.Name == "scale" })
	if scaleSet && !(*scale > 0 && *scale <= 4) {
		return fmt.Errorf("shell init: -scale %g is out of range; it must be above 0 and at most 4", *scale)
	}
	src := fs.Arg(0)
	if ext := strings.ToLower(filepath.Ext(src)); ext != ".ora" {
		return fmt.Errorf("shell init: %s is not a .ora file — export one from Krita, GIMP, MyPaint or Pencil2D", src)
	}

	// The shell being replaced, if there is one: its layers' rigging is
	// carried over, and its scale decides whether to suggest one. Only
	// -write depends on reading it; printing a section to paste should not
	// fail over a manifest it does not touch.
	manifest := filepath.Join(*dir, "ghost.toml")
	var old shellFile
	existing, err := os.ReadFile(manifest)
	switch {
	case err == nil:
		if _, err := toml.Decode(string(existing), &old); err != nil {
			if *write {
				return fmt.Errorf("shell init -write: reading %s: %w", manifest, err)
			}
			old = shellFile{}
		}
	case *write:
		return fmt.Errorf("shell init -write: %w", err)
	}

	stack, err := artimport.ReadORA(src)
	if err != nil {
		return err
	}
	res, err := artimport.Convert(stack, *art)
	if err != nil {
		return err
	}
	res.Keep(old.Shell)
	if scaleSet {
		res.Shell.Scale = *scale
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
	if res.Masks > 0 {
		fmt.Fprintf(os.Stderr, "haunt: wrote %d layer images and %d masks to %s\n",
			len(res.Files)-res.Masks, res.Masks, filepath.Join(*dir, *art))
	} else {
		fmt.Fprintf(os.Stderr, "haunt: wrote %d layer images to %s\n",
			len(res.Files), filepath.Join(*dir, *art))
	}
	// Art drawn at a painting's resolution makes a ghost that fills the
	// screen. The scale is the author's call, so this only suggests one,
	// sized to about what a desktop companion usually is.
	if !scaleSet && old.Shell.Scale == 0 {
		if long := max(res.W, res.H); long > 512 {
			fmt.Fprintf(os.Stderr, "haunt: the art is %dx%d pixels; to draw it near 256 px, re-run with -scale %.2g\n",
				res.W, res.H, 256/float64(long))
		}
	}

	section := res.TOML()
	if !*write {
		// To stdout by default: the section is small, and a person pasting
		// it into their manifest can see what they are agreeing to. -write
		// is for when they have seen it once.
		fmt.Print(section)
		fmt.Fprintln(os.Stderr, "haunt: add that to your ghost.toml, or re-run with -write")
		return nil
	}

	updated, err := replaceShellSection(string(existing), section)
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

	// What was kept was written against the old art: a pose naming a
	// variant the drawing no longer has is the usual casualty. The file
	// stays written, since the fix is an edit to it, and the error says
	// what the edit is.
	var written shellFile
	if _, err := toml.Decode(updated, &written); err != nil {
		return fmt.Errorf("shell init: %s no longer parses: %w", manifest, err)
	}
	if _, err := shell.LoadWith(*dir, written.Shell, shell.LoadOptions{}); err != nil {
		return fmt.Errorf("shell init: %s is written, but its shell does not load: %w", manifest, err)
	}
	return nil
}

// shellFile is the part of a ghost.toml the importer reads.
type shellFile struct {
	Shell shell.Manifest `toml:"shell"`
}

// A shell table's place in a re-import. Regenerated tables come from the
// drawing; kept ones are the author's, written against it.
type shellTable int

const (
	notShell    shellTable = iota
	shellMain              // [shell]: kept, less what the drawing says
	regenerated            // layers and masks
	kept                   // poses, clips, hits
)

// classify reads a table header line and says whose table it is.
func classify(trimmed string) shellTable {
	name, ok := tableName(trimmed)
	if !ok {
		return notShell
	}
	switch {
	case name == "shell":
		return shellMain
	case name == "shell.layer" || strings.HasPrefix(name, "shell.layer.") ||
		name == "shell.masks" || strings.HasPrefix(name, "shell.masks."):
		return regenerated
	case strings.HasPrefix(name, "shell."):
		// Poses, clips and hits, and anything else under [shell] this
		// code does not know: an author's text is kept unless it is known
		// to come from the drawing.
		return kept
	}
	return notShell
}

// tableName returns the dotted name in a `[name]` or `[[name]]` header,
// with spaces around the dots removed, and whether the line is a header.
func tableName(trimmed string) (string, bool) {
	if !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	inner := strings.TrimLeft(trimmed, "[")
	end := strings.Index(inner, "]")
	if end < 0 {
		return "", false
	}
	parts := strings.Split(inner[:end], ".")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return strings.Join(parts, "."), true
}

// replaceShellSection puts a freshly generated shell section into a
// manifest, leaving everything else — identity, capabilities, persona,
// triggers — exactly where the author put it. Re-running the import must
// not cost somebody their persona, nor the poses and clips they wrote.
//
// What the drawing describes is replaced: the layer tables, the masks, and
// in the [shell] table the sprite and any key the new section sets. The
// rest is the author's and kept verbatim, comments and order included: the
// other [shell] lines, which go after the new section's own, and every pose,
// clip and hit table, which follow the new section. It works on lines
// rather than a parsed document because a TOML encoder would keep the
// values and lose the comments.
//
// Comments and blank lines at the end of a table are taken to introduce
// the next one, since that is where a comment about a table is written;
// they move with it.
func replaceShellSection(manifest, section string) (string, error) {
	// The new section's own [shell] lines, and which keys they set.
	secLines := strings.Split(strings.TrimRight(section, "\n"), "\n")
	var secMain, secRest []string
	// The sprite is replaced by the layers, and layers and masks written
	// as keys rather than tables are the drawing's too.
	setKeys := map[string]bool{"sprite": true, "layer": true, "masks": true}
	inMain := false
	for i, line := range secLines {
		trimmed := strings.TrimSpace(line)
		if i == 0 && classify(trimmed) == shellMain {
			inMain = true
			continue
		}
		if inMain && strings.HasPrefix(trimmed, "[") {
			inMain = false
		}
		if inMain {
			if key, ok := keyOf(trimmed); ok {
				setKeys[key] = true
			}
			if trimmed != "" {
				secMain = append(secMain, line)
			}
			continue
		}
		secRest = append(secRest, line)
	}

	// The manifest as blocks: each is a header and its body, preceded by
	// the comments that introduce it.
	type block struct {
		kind  shellTable
		lines []string // leading comments, header, body
	}
	var blocks []block
	cur := block{kind: notShell}
	for _, line := range strings.Split(manifest, "\n") {
		trimmed := strings.TrimSpace(line)
		kind := classify(trimmed)
		if _, isHeader := tableName(trimmed); !isHeader {
			cur.lines = append(cur.lines, line)
			continue
		}
		// Move the trailing comments and blanks of the block before to
		// this one.
		cut := len(cur.lines)
		for cut > 0 {
			t := strings.TrimSpace(cur.lines[cut-1])
			if t != "" && !strings.HasPrefix(t, "#") {
				break
			}
			cut--
		}
		lead := append([]string(nil), cur.lines[cut:]...)
		cur.lines = cur.lines[:cut]
		blocks = append(blocks, cur)
		cur = block{kind: kind, lines: append(lead, line)}
	}
	blocks = append(blocks, cur)

	var out, mainBody, keptTables []string
	placed := false
	appendBlock := func(dst []string, lines []string) []string {
		// One blank line between tables, however they were spaced.
		if len(dst) > 0 && len(lines) > 0 && strings.TrimSpace(dst[len(dst)-1]) != "" &&
			strings.TrimSpace(lines[0]) != "" {
			dst = append(dst, "")
		}
		return append(dst, lines...)
	}
	flush := func() {
		if placed {
			return
		}
		if n := len(out); n > 0 && strings.HasPrefix(strings.TrimSpace(out[n-1]), "#") {
			out = append(out, "[shell]") // the comment introducing it
		} else {
			out = appendBlock(out, []string{"[shell]"})
		}
		out = append(out, secMain...)
		out = append(out, trimBlank(mainBody)...)
		out = appendBlock(out, trimBlank(secRest))
		out = appendBlock(out, trimBlank(keptTables))
		placed = true
	}
	sawShell := false
	for i, b := range blocks {
		if b.kind == notShell {
			if sawShell && !placed {
				flush()
			}
			if i == 0 {
				out = append(out, b.lines...)
			} else {
				out = appendBlock(out, b.lines)
			}
			continue
		}
		if !sawShell {
			sawShell = true
			// What introduces the first shell table introduces the
			// section.
			for len(b.lines) > 0 {
				t := strings.TrimSpace(b.lines[0])
				if t != "" && !strings.HasPrefix(t, "#") {
					break
				}
				out = append(out, b.lines[0])
				b.lines = b.lines[1:]
			}
		}
		switch b.kind {
		case shellMain:
			for _, line := range b.lines {
				trimmed := strings.TrimSpace(line)
				if classify(trimmed) == shellMain {
					continue
				}
				if key, ok := keyOf(trimmed); ok && setKeys[key] {
					continue
				}
				mainBody = append(mainBody, line)
			}
		case kept:
			// Verbatim, spacing included: a clip's keys follow it the way
			// the author laid them out.
			keptTables = append(keptTables, b.lines...)
		}
	}
	if sawShell {
		flush()
		return strings.Join(out, "\n"), nil
	}

	// No shell yet: one blank line before it, however the file ended.
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	out = append(out, "", strings.TrimRight(section, "\n"))
	return strings.Join(out, "\n"), nil
}

// keyOf returns the key a `key = value` line sets, and whether it is one.
func keyOf(trimmed string) (string, bool) {
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	key, _, ok := strings.Cut(trimmed, "=")
	if !ok {
		return "", false
	}
	return strings.Trim(strings.TrimSpace(key), `"'`), true
}

// trimBlank drops blank lines from both ends of a block.
func trimBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
