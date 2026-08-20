package seriko

import (
	"fmt"
	"image"
	"sort"
	"strconv"
	"strings"

	"github.com/aaronjauregui/haunt/internal/shell"
)

// Converting SERIKO's model into haunt's.
//
// The two formats agree more than they disagree, which is why this is
// mechanical: both describe a character as named art that swaps on a clock.
// Where they differ:
//
//   - SERIKO has *surfaces*, each a whole picture, and animations that
//     overlay one surface on another at an offset. haunt has *layers* with
//     named variants, and clips that swap variants. So a surface becomes a
//     variant of the body layer, and each animation becomes a layer of its
//     own whose variants are the frames it draws.
//   - SERIKO animations belong to a surface. haunt clips belong to the
//     shell. The default surface's animations therefore become the
//     automatic ones — a blink is a blink — and every other surface's
//     become manual clips a brain can ask for by name. Nothing else is
//     honest: a blink drawn for surface 0's eyes lands in the wrong place
//     on surface 5's face, and firing it automatically would be a bug that
//     looks like bad art.
//   - SERIKO collisions are per-surface. haunt regions are per-layer. The
//     default surface's collisions win, since regions are a coarse
//     affordance ("you poked my hat") and a per-pose hitbox is a precision
//     this driver has no way to use.

// bodyLayer is the layer every surface's own picture becomes a variant of.
const bodyLayer = "body"

// Options configures a conversion.
type Options struct {
	// Default is the surface ID worn at rest. Zero is SERIKO's own
	// convention and nearly always right.
	Default int
	// Only, when non-empty, limits the conversion to these surface IDs and
	// whatever their animations reference. A shell with two hundred
	// surfaces converts to two hundred full-size PNGs otherwise, which is
	// a lot of megabytes for a first look.
	Only []int
	// Named gives poses human names: "happy" -> 3. SERIKO identifies its
	// surfaces by number, and a number cannot be chosen from the mood of a
	// line — so a converted shell keeps one face until somebody says which
	// surface is a smile. Only a person looking at the art can, which is
	// why this is an argument rather than a guess.
	Named map[string]int
}

// Result is a converted shell.
type Result struct {
	// Shell is the [shell] section, ready to write into a ghost.toml.
	Shell shell.Manifest
	// Files maps a path relative to the ghost directory to PNG bytes.
	Files map[string][]byte
	// Notes are what the conversion decided that a person might want to
	// know: what was dropped, what was approximated, what was renamed.
	Notes []string
}

// Source supplies the shell's files by name, case-insensitively — SERIKO
// shells are Windows artefacts and their surfaces.txt does not agree with
// the filesystem about case anything like often enough.
type Source interface {
	// Read returns a file's bytes, reporting whether it existed.
	Read(name string) ([]byte, bool)
}

// Convert turns a parsed shell and its art into haunt's format.
func Convert(sh *Shell, src Source, opts Options) (*Result, error) {
	res := &Result{Files: map[string][]byte{}}
	if len(sh.Surfaces) == 0 {
		return nil, fmt.Errorf("no surfaces in surfaces.txt")
	}

	wanted := wantedSurfaces(sh, opts)
	if len(wanted) == 0 {
		return nil, fmt.Errorf("none of the requested surfaces exist in this shell")
	}

	// Every surface's picture, composited and colour-keyed.
	pics := map[int]*image.NRGBA{}
	for _, id := range wanted {
		img, err := surfaceImage(sh, src, id)
		if err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("surface %d skipped: %v", id, err))
			continue
		}
		pics[id] = img
	}
	if len(pics) == 0 {
		return nil, fmt.Errorf("no surface image could be read")
	}

	base := opts.Default
	if _, ok := pics[base]; !ok {
		// The shell does not have the surface we were told to rest on.
		// Take the lowest it does have rather than refusing: an importer
		// that will not open a shell over a missing surface 0 is one
		// nobody gets to use.
		base = lowest(pics)
		res.Notes = append(res.Notes,
			fmt.Sprintf("surface %d is missing; resting on surface %d instead", opts.Default, base))
	}

	// The body layer: one variant per surface, cropped, with the crop
	// folded into a per-variant offset — except haunt's `at` is per layer,
	// so the body keeps the full canvas and is not cropped. It is the one
	// layer that legitimately covers everything.
	body := shell.Layer{
		Name:     bodyLayer,
		Variants: map[string]string{},
		Default:  variantName(base),
		Z:        0,
		Region:   "body",
	}
	for _, id := range sortedKeys(pics) {
		name := fmt.Sprintf("surface%d.png", id)
		data, err := encodePNG(pics[id])
		if err != nil {
			return nil, err
		}
		res.Files["shell/"+name] = data
		body.Variants[variantName(id)] = "shell/" + name
	}
	layers := []shell.Layer{body}

	// Poses: one per surface, so a brain can wear any of them by name, plus
	// whatever the shell's own aliases called them.
	poses := map[string]map[string]string{}
	for _, id := range sortedKeys(pics) {
		poses[variantName(id)] = map[string]string{bodyLayer: variantName(id)}
	}
	for alias, ids := range sh.Aliases {
		name := safeName(alias)
		if name == "" || len(ids) == 0 {
			continue
		}
		if _, taken := poses[name]; taken {
			continue
		}
		if _, ok := pics[ids[0]]; !ok {
			continue
		}
		// An alias standing for several surfaces is a random choice in
		// SERIKO. haunt has no such thing, so it takes the first and says
		// so rather than inventing randomness the author cannot see.
		if len(ids) > 1 {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"alias %q stood for surfaces %v; using the first, since poses here are not random", alias, ids))
		}
		poses[name] = map[string]string{bodyLayer: variantName(ids[0])}
	}

	// Names the person gave, and the shell's own aliases above. Both are
	// how a numbered surface becomes something the driver can choose.
	for name, id := range opts.Named {
		safe := safeName(name)
		if safe == "" {
			res.Notes = append(res.Notes, fmt.Sprintf("pose name %q has nothing usable in it; skipped", name))
			continue
		}
		if _, ok := pics[id]; !ok {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"pose %q wants surface %d, which this shell does not have; skipped", name, id))
			continue
		}
		poses[safe] = map[string]string{bodyLayer: variantName(id)}
	}

	// Whether anything here can be chosen without a brain asking. Said out
	// loud because the failure is silent otherwise: the ghost simply wears
	// one face forever and looks broken rather than unconfigured.
	if !anyMoodPose(poses) {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"no pose is named for the driver's own expression inference (%s), so the face will not "+
				"follow what the ghost says — pass -pose happy=N,sad=N to name them, or leave it to a brain",
			strings.Join(shell.MoodPoses, ", ")))
	}

	// Animations.
	clips, animLayers, notes := convertAnimations(sh, src, pics, base, res)
	layers = append(layers, animLayers...)
	res.Notes = append(res.Notes, notes...)

	// Named areas from the resting surface's collisions. Per-surface in
	// SERIKO and per-shell here: regions are a coarse affordance ("you
	// poked my hat"), and a hitbox that changed with every expression is a
	// precision this driver has nothing to do with.
	var areas []shell.Hit
	if s := sh.Surfaces[base]; s != nil {
		if areas = hits(s); len(areas) > 0 {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"%d collision area(s) from surface %d became named regions", len(areas), base))
		}
	}

	res.Shell = shell.Manifest{
		Layers:  layers,
		Poses:   poses,
		Default: variantName(base),
		Clips:   clips,
		Hits:    areas,
	}
	return res, nil
}

// wantedSurfaces is which surfaces to convert: everything, or the requested
// ones — and in both cases whatever their animations draw.
//
// The frames matter separately from the blocks. A shell may animate through
// surface 113 without ever writing a `surface113` block, because it has no
// collisions or animations of its own to declare; it is a picture, not a
// pose. Converting only the declared blocks loses exactly those frames, and
// the animation that used them comes out empty.
func wantedSurfaces(sh *Shell, opts Options) []int {
	want := map[int]bool{}
	seeds := opts.Only
	if len(seeds) == 0 {
		seeds = sortedSurfaceIDs(sh)
	}
	for _, id := range seeds {
		s := sh.Surfaces[id]
		if s == nil {
			continue
		}
		want[id] = true
		for _, a := range s.Animations {
			for _, p := range a.Patterns {
				if !p.Clearing() {
					want[p.Surface] = true
				}
			}
		}
	}
	out := make([]int, 0, len(want))
	for id := range want {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// surfaceImage composites one surface: its elements if it has any, else the
// surfaceN.png beside surfaces.txt, with its .pna alpha if there is one.
func surfaceImage(sh *Shell, src Source, id int) (*image.NRGBA, error) {
	s := sh.Surfaces[id]
	if s != nil && len(s.Elements) > 0 {
		return compositeElements(src, s.Elements)
	}
	name := fmt.Sprintf("surface%d.png", id)
	data, ok := src.Read(name)
	if !ok {
		return nil, fmt.Errorf("%s is not in the shell", name)
	}
	pna, _ := src.Read(fmt.Sprintf("surface%d.pna", id))
	return LoadSurface(data, pna)
}

// compositeElements builds a surface from its element lines.
func compositeElements(src Source, els []Element) (*image.NRGBA, error) {
	var out *image.NRGBA
	for i, e := range els {
		data, ok := src.Read(e.File)
		if !ok {
			return nil, fmt.Errorf("element %d wants %s, which is not in the shell", i, e.File)
		}
		pna, _ := src.Read(pnaFor(e.File))
		img, err := LoadSurface(data, pna)
		if err != nil {
			return nil, fmt.Errorf("element %d (%s): %w", i, e.File, err)
		}
		if out == nil {
			// The first element establishes the canvas. Sized to hold
			// itself at its own offset, so an element placed at a positive
			// offset is not cropped by its own base.
			out = image.NewNRGBA(image.Rect(0, 0,
				e.X+img.Bounds().Dx(), e.Y+img.Bounds().Dy()))
		}
		composite(out, img, image.Pt(e.X, e.Y), e.Method)
	}
	if out == nil {
		return nil, fmt.Errorf("no elements")
	}
	return out, nil
}

// pnaFor is the alpha mask that would accompany a file.
func pnaFor(file string) string {
	if i := strings.LastIndexByte(file, '.'); i >= 0 {
		return file[:i] + ".pna"
	}
	return file + ".pna"
}

// hits turns a surface's collisions into named rectangles. SERIKO says
// where a character's parts are outright rather than deriving them from the
// art, which is why the shell format grew `[[hit]]` to receive them: there
// is no layer to hang a region on when the whole character is one picture.
//
// Order is kept, because SERIKO's first match wins and the rectangles do
// overlap — a face inside a head.
func hits(s *Surface) []shell.Hit {
	var out []shell.Hit
	seen := map[string]bool{}
	for _, c := range s.Collisions {
		name := safeName(c.Name)
		// An invalid rectangle is not a broken file: shells switch a
		// collision off for a surface by setting it to -1 rather than
		// deleting the line.
		if name == "" || !c.Valid() || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, shell.Hit{
			Region: name,
			At:     [2]int{c.X1, c.Y1},
			Size:   [2]int{c.X2 - c.X1, c.Y2 - c.Y1},
		})
	}
	return out
}

// anyMoodPose reports whether the shell has a pose the driver can pick from
// the mood of a line.
func anyMoodPose(poses map[string]map[string]string) bool {
	for _, m := range shell.MoodPoses {
		if _, ok := poses[m]; ok {
			return true
		}
	}
	return false
}

func variantName(id int) string { return "s" + strconv.Itoa(id) }

func sortedSurfaceIDs(sh *Shell) []int {
	out := make([]int, 0, len(sh.Surfaces))
	for id := range sh.Surfaces {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

func sortedKeys(m map[int]*image.NRGBA) []int {
	out := make([]int, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

func lowest(m map[int]*image.NRGBA) int {
	best := -1
	for id := range m {
		if best < 0 || id < best {
			best = id
		}
	}
	return best
}
