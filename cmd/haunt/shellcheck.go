package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/shell"
)

// cmdShellCheck loads a ghost's shell the way the driver does and reports
// what it found.
//
// Without it the only way to learn whether a shell loads, and what it
// looks like at a given scale, is `haunt run`: `pack` does not load the
// shell, and a mask that a layer strays out of only shows as a ghost that
// will not start. The check says the same thing the driver would, with the
// loader's own message, and -render puts every face on disk where an
// author can look at them side by side.
func cmdShellCheck(args []string) error {
	return shellCheck(args, os.Stdout)
}

// shellCheck is cmdShellCheck writing its report to w, so tests can read it.
func shellCheck(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("shell check", flag.ExitOnError)
	dir := fs.String("C", ".", "ghost directory to check")
	factor := fs.Float64("factor", 0, "the person's scale factor, in place of the one in their config")
	render := fs.String("render", "", "directory to write each pose, the base and the masks into as PNGs")
	fs.Parse(args)
	if fs.NArg() != 0 {
		return fmt.Errorf("shell check: unexpected argument %q", fs.Arg(0))
	}

	m, err := ghost.LoadManifest(*dir)
	if err != nil {
		return err
	}

	// -factor skips the config entirely, so a mistake elsewhere in the
	// person's config.toml cannot stand between an author and a check.
	f := *factor
	if isSet(fs, "factor") {
		if f <= 0 {
			return fmt.Errorf("shell check: -factor must be positive, not %g", f)
		}
	} else {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		f = cfg.ShellScale(m.Ghost.Name)
	}

	load := func() (*shell.Shell, error) {
		return shell.LoadWith(*dir, m.Shell, shell.LoadOptions{ScaleFactor: f})
	}
	s, err := load()
	if err != nil {
		return fmt.Errorf("shell check: %w", err)
	}
	reportShell(w, m, s, f)

	if *render == "" {
		return nil
	}
	written, err := renderShell(*render, s, load)
	if err != nil {
		return fmt.Errorf("shell check -render: %w", err)
	}
	fmt.Fprintf(w, "rendered: %s in %s\n", strings.Join(written, ", "), *render)
	return nil
}

// reportShell writes what the loader made of the manifest: the numbers an author
// needs to see that the scale came out as meant, and the names a pose or a
// clip may use.
func reportShell(w io.Writer, m *ghost.Manifest, s *shell.Shell, factor float64) {
	own := m.Shell.Scale
	if own == 0 {
		own = 1
	}
	fmt.Fprintf(w, "ghost: %s\n", m.Ghost.Name)
	fmt.Fprintf(w, "scale: %g (shell) x %g (factor) = %g (effective)\n", own, factor, s.Scale())
	aw, ah := s.ArtSize()
	dw, dh := s.Size()
	fmt.Fprintf(w, "canvas: %dx%d art pixels, %dx%d display pixels\n", aw, ah, dw, dh)

	fmt.Fprintln(w, "layers (bottom to top):")
	for _, l := range s.Layers() {
		vs := make([]string, len(l.Variants))
		for i, v := range l.Variants {
			vs[i] = variantName(v)
		}
		fmt.Fprintf(w, "  %s: mask %s, region %s, parent %s, default %s, variants %s\n",
			l.Name, orDash(l.Mask), orDash(l.Region), orDash(l.Parent),
			variantName(l.Default), strings.Join(vs, " "))
	}

	fmt.Fprintf(w, "poses: %s\n", orDash(strings.Join(s.Poses(), " ")))

	fmt.Fprintf(w, "clips:%s\n", emptyDash(len(s.Clips())))
	for _, c := range s.Clips() {
		fmt.Fprintf(w, "  %s: trigger %s\n", c.Name, orDash(c.Trigger))
	}

	fmt.Fprintf(w, "masks:%s\n", emptyDash(len(s.Masks())))
	for _, name := range s.Masks() {
		fmt.Fprintf(w, "  %s: %d pixels inside\n", name, insidePixels(s.Mask(name)))
	}
}

// variantName shows a single-image layer's one appearance, whose name is
// the empty string, as something a reader can see.
func variantName(v string) string {
	if v == "" {
		return "(image)"
	}
	return v
}

// emptyDash follows a list heading with " -" when the list is empty, so
// the report says there is nothing rather than seeming cut short.
func emptyDash(n int) string {
	if n == 0 {
		return " -"
	}
	return ""
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// insidePixels counts a mask's inside, which is 128 and above; the soft
// edge below that is outside for every rule the loader applies.
func insidePixels(m *image.Gray) int {
	if m == nil {
		return 0
	}
	n := 0
	for _, v := range m.Pix {
		if v >= 128 {
			n++
		}
	}
	return n
}

// renderShell writes the shell's faces into dir and returns the file names
// in the order written.
//
// default.png comes from a load of its own, before anything calls SetPose,
// because that is what the ghost wears at boot: the default pose if there
// is one, else every layer's own default. Rendering it after the poses
// would show whichever pose came last.
func renderShell(dir string, s *shell.Shell, load func() (*shell.Shell, error)) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	put := func(name string, img image.Image) error {
		if err := writePNGFile(filepath.Join(dir, name), img); err != nil {
			return err
		}
		written = append(written, name)
		return nil
	}

	fresh, err := load()
	if err != nil {
		return nil, err
	}
	if err := put("default.png", fresh.Sprite().Img); err != nil {
		return nil, err
	}

	// A pose named like one of the other files, or like a path, would
	// overwrite it or land outside dir; those poses get a prefix instead.
	reserved := map[string]bool{"default": true, "base": true, "masks": true}
	for _, pose := range s.Poses() {
		s.SetPose(pose)
		name := pose + ".png"
		if reserved[pose] || !filepath.IsLocal(name) || strings.ContainsAny(pose, `/\`) {
			name = "pose-" + strings.NewReplacer("/", "_", `\`, "_").Replace(pose) + ".png"
		}
		if err := put(name, s.Sprite().Img); err != nil {
			return nil, err
		}
	}

	if len(s.Masks()) == 0 {
		return written, nil
	}
	base := s.Base()
	if base == nil {
		return written, nil
	}
	if err := put("base.png", base); err != nil {
		return nil, err
	}
	outlined := image.NewNRGBA(base.Bounds())
	draw.Draw(outlined, outlined.Bounds(), base, base.Bounds().Min, draw.Src)
	for i, name := range s.Masks() {
		outline(outlined, s.Mask(name), maskColours[i%len(maskColours)])
	}
	if err := put("masks.png", outlined); err != nil {
		return nil, err
	}
	return written, nil
}

// maskColours tell the masks apart in masks.png, in the sorted order of
// their names; they are saturated so an outline shows over any art.
var maskColours = []color.NRGBA{
	{255, 0, 0, 255},
	{0, 200, 0, 255},
	{0, 100, 255, 255},
	{255, 200, 0, 255},
	{255, 0, 255, 255},
	{0, 230, 230, 255},
	{255, 128, 0, 255},
	{140, 0, 255, 255},
}

// outline paints the edge of a mask's inside onto img: every inside pixel
// with a neighbour, up, down, left or right, that is outside or off the
// mask. The edge is what an author checks a face or a hat against, and
// filling the whole mask would hide the art underneath.
func outline(img *image.NRGBA, m *image.Gray, c color.NRGBA) {
	if m == nil {
		return
	}
	b := m.Bounds()
	in := func(x, y int) bool {
		return image.Pt(x, y).In(b) && m.GrayAt(x, y).Y >= 128
	}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if !in(x, y) {
				continue
			}
			if !in(x-1, y) || !in(x+1, y) || !in(x, y-1) || !in(x, y+1) {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func writePNGFile(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
