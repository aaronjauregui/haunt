// Package seriko reads ukagaka shell *art* — SERIKO's surfaces.txt and the
// images beside it — and converts it into haunt's shell format.
//
// The non-goals rule out SHIORI, SSTP and SakuraScript, and that stands:
// this inherits no execution model. Shell art is a different question.
// SERIKO describes elements, composition and frame-swap animation in a
// vocabulary this format deliberately mirrors, so the translation is
// mechanical, and two decades of shells exist that would otherwise need
// redrawing.
//
// What is read and what is ignored:
//
//   - element lines become the composited base image for a surface.
//   - collision rectangles become named regions, which is what a haunt
//     brain already reacts to ("you poked my hat").
//   - animation definitions become clips, in both the SERIKO/1.x and 2.0
//     spellings, because real shells in the wild are mostly 1.x.
//   - cursors, tooltips, balloon offsets and reference points are read but
//     not converted: they describe a baseware this driver is not.
package seriko

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/transform"
)

// Shell is a parsed surfaces.txt: the surface definitions and the
// descript.txt metadata beside them.
type Shell struct {
	// Name is the shell's name from descript.txt, if it gave one.
	Name string
	// Version is the SERIKO format the file asked for: 0 for 1.x, 1 for
	// 2.0. It decides how animation lines are spelled, and getting it
	// wrong means reading a wait of 5 as 5ms instead of 50ms.
	Version int
	// Surfaces by ID.
	Surfaces map[int]*Surface
	// Aliases map a name to the surface IDs it stands for.
	Aliases map[string][]int
}

// Surface is one surface definition.
type Surface struct {
	ID int
	// Elements composite into the surface's base image, in order.
	Elements []Element
	// Collisions are the named hit rectangles.
	Collisions []Collision
	// Animations by ID.
	Animations map[int]*Animation
}

// Element is one image composited into a surface.
type Element struct {
	// Method is the drawing method: base, overlay, replace, interpolate,
	// overlayfast, asis and the rest. Only the ones that matter for a
	// still composite are honoured; see convert.go.
	Method string
	File   string
	X, Y   int
}

// Collision is a named rectangle a person can poke.
type Collision struct {
	Name           string
	X1, Y1, X2, Y2 int
}

// Animation is one frame-swap sequence on a surface.
type Animation struct {
	ID int
	// Interval says who starts it: sometimes, rarely, always, runonce,
	// never, random,N, periodic,N, talk,N, bind, and combinations.
	Interval string
	// Every is the N in random,N / periodic,N / talk,N.
	Every    int
	Patterns []Pattern
	// Options are the animationN.option values (exclusive, background,
	// shared-index). Kept so the converter can say what it ignored.
	Options []string
}

// Pattern is one frame: which surface to draw, where, and for how long.
type Pattern struct {
	// Surface is the surface ID to draw. -1 clears this animation's frame,
	// -2 clears every other animation; both arrive here as-is.
	Surface int
	// WaitMS is how long this frame is held, already normalised to
	// milliseconds from whichever spelling the file used.
	WaitMS int
	Method string
	X, Y   int
}

// Clearing reports whether this pattern removes the frame rather than
// drawing one.
func (p Pattern) Clearing() bool { return p.Surface < 0 }

// Decode turns a shell file's bytes into text. SERIKO files are commonly
// Shift-JIS and occasionally UTF-8, and the `charset` line that would say
// which is itself inside the file — so the encoding is decided by looking:
// valid UTF-8 is UTF-8, anything else is Shift-JIS, which is the only other
// encoding these files are found in.
func Decode(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	out, _, err := transform.Bytes(japanese.ShiftJIS.NewDecoder(), b)
	if err != nil {
		// Undecodable bytes are not worth failing an import over: the
		// lines that matter are ASCII, and a mangled comment costs nothing.
		return string(b)
	}
	return string(out)
}

// ParseDescript reads descript.txt, which is `key,value` lines.
func ParseDescript(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(Decode(b)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		k, v, ok := strings.Cut(line, ",")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}

// Parse reads surfaces.txt.
func Parse(b []byte) (*Shell, error) {
	sh := &Shell{
		Surfaces: map[int]*Surface{},
		Aliases:  map[string][]int{},
	}
	lines := strings.Split(strings.ReplaceAll(Decode(b), "\r\n", "\n"), "\n")

	var block string
	var body []string
	depth := 0
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		switch {
		case line == "{":
			depth++
			continue
		case line == "}":
			depth--
			if depth == 0 && block != "" {
				if err := sh.block(block, body); err != nil {
					return nil, err
				}
				block, body = "", nil
			}
			continue
		}
		if depth == 0 {
			// A brace on the same line as its name is invalid SERIKO, and
			// SSP rejects it; so do we, rather than half-reading a file
			// whose author will be baffled by the result.
			if strings.HasSuffix(line, "{") {
				return nil, fmt.Errorf("line %d: %q — the brace must be on its own line", i+1, line)
			}
			block = line
			continue
		}
		body = append(body, line)
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced braces: %d block(s) left open", depth)
	}
	return sh, nil
}

// block dispatches one named brace block.
func (sh *Shell) block(name string, body []string) error {
	switch {
	case name == "descript":
		for _, line := range body {
			k, v, _ := strings.Cut(line, ",")
			if strings.TrimSpace(k) == "version" {
				sh.Version, _ = strconv.Atoi(strings.TrimSpace(v))
			}
		}
		return nil
	case strings.HasSuffix(name, ".surface.alias"):
		for _, line := range body {
			k, v, ok := strings.Cut(line, ",")
			if !ok {
				continue
			}
			ids := parseIDList(v)
			if len(ids) > 0 {
				sh.Aliases[strings.TrimSpace(k)] = ids
			}
		}
		return nil
	case strings.HasPrefix(name, "surface"):
		return sh.surfaceBlock(name, body)
	}
	// Cursors, tooltips, and anything else a baseware would want. Not an
	// error: a shell is allowed to describe more than this driver uses.
	return nil
}

// surfaceBlock handles `surface0`, `surface1-10`, `surface1,3,5`,
// `surface1-10,!5` and the `.append` forms.
func (sh *Shell) surfaceBlock(name string, body []string) error {
	spec := strings.TrimPrefix(name, "surface")
	appendOnly := false
	if strings.HasPrefix(spec, ".append") {
		appendOnly = true
		spec = strings.TrimPrefix(spec, ".append")
	}
	ids, err := parseSurfaceSpec(spec)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	for _, id := range ids {
		s := sh.Surfaces[id]
		if s == nil {
			if appendOnly {
				// The whole point of .append: it must not conjure a
				// surface that does not exist.
				continue
			}
			s = &Surface{ID: id, Animations: map[int]*Animation{}}
			sh.Surfaces[id] = s
		}
		for _, line := range body {
			s.line(line, sh.Version)
		}
	}
	return nil
}

// line reads one line inside a surface block.
func (s *Surface) line(line string, version int) {
	key, rest, ok := strings.Cut(line, ",")
	if !ok {
		return
	}
	key = strings.TrimSpace(key)
	fields := splitFields(rest)

	switch {
	case strings.HasPrefix(key, "element"):
		if len(fields) < 4 {
			return
		}
		x, _ := strconv.Atoi(fields[2])
		y, _ := strconv.Atoi(fields[3])
		s.Elements = append(s.Elements, Element{
			Method: strings.ToLower(fields[0]), File: fields[1], X: x, Y: y,
		})
	case strings.HasPrefix(key, "collisionex"):
		// Shapes beyond a rectangle: read the bounding box where it is
		// obvious and skip the rest. Regions are a coarse affordance here
		// ("you poked my hat"), so a polygon's box is a fair answer.
		if len(fields) >= 6 && strings.EqualFold(fields[1], "rect") {
			s.Collisions = append(s.Collisions, collisionFrom(fields[0], fields[2:6]))
		}
	case strings.HasPrefix(key, "collision"):
		if len(fields) >= 5 {
			s.Collisions = append(s.Collisions, collisionFrom(fields[4], fields[0:4]))
		}
	case strings.HasPrefix(key, "animation"):
		s.animation2(key, fields)
	default:
		s.animation1(key, fields, version)
	}
}

// animation2 reads the SERIKO/2.0 spelling: animation0.interval,
// animation0.pattern0, animation0.option.
func (s *Surface) animation2(key string, fields []string) {
	rest := strings.TrimPrefix(key, "animation")
	idStr, what, ok := strings.Cut(rest, ".")
	if !ok {
		return
	}
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return
	}
	a := s.anim(id)
	switch {
	case what == "interval":
		a.Interval, a.Every = parseInterval(fields)
	case what == "option":
		a.Options = append(a.Options, strings.Join(fields, ","))
	case strings.HasPrefix(what, "pattern"):
		// (method),(surfaceID),(wait ms),(X),(Y)
		if len(fields) < 2 {
			return
		}
		p := Pattern{Method: strings.ToLower(fields[0])}
		p.Surface, _ = strconv.Atoi(fields[1])
		if len(fields) > 2 {
			p.WaitMS, _ = strconv.Atoi(fields[2])
		}
		if len(fields) > 4 {
			p.X, _ = strconv.Atoi(fields[3])
			p.Y, _ = strconv.Atoi(fields[4])
		}
		a.Patterns = append(a.Patterns, p)
	}
}

// animation1 reads the SERIKO/1.x spelling: `0interval,sometimes` and
// `0pattern0,101,5,overlay,172,156`. The differences that bite are the
// argument order and the wait being in hundredths of a second.
func (s *Surface) animation1(key string, fields []string, version int) {
	digits := 0
	for digits < len(key) && key[digits] >= '0' && key[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return
	}
	id, err := strconv.Atoi(key[:digits])
	if err != nil {
		return
	}
	what := key[digits:]
	a := s.anim(id)
	switch {
	case what == "interval":
		a.Interval, a.Every = parseInterval(fields)
	case what == "option":
		a.Options = append(a.Options, strings.Join(fields, ","))
	case strings.HasPrefix(what, "pattern"):
		// (surfaceID),(wait),(method),(X),(Y)
		if len(fields) < 2 {
			return
		}
		var p Pattern
		p.Surface, _ = strconv.Atoi(fields[0])
		wait, _ := strconv.Atoi(fields[1])
		// 1.x waits are in units of 10ms. A file that declared version 1
		// is using 2.0 semantics even in this spelling, which SSP allows.
		if version >= 1 {
			p.WaitMS = wait
		} else {
			p.WaitMS = wait * 10
		}
		if len(fields) > 2 {
			p.Method = strings.ToLower(fields[2])
		}
		if len(fields) > 4 {
			p.X, _ = strconv.Atoi(fields[3])
			p.Y, _ = strconv.Atoi(fields[4])
		}
		a.Patterns = append(a.Patterns, p)
	}
}

func (s *Surface) anim(id int) *Animation {
	if s.Animations == nil {
		s.Animations = map[int]*Animation{}
	}
	a := s.Animations[id]
	if a == nil {
		a = &Animation{ID: id}
		s.Animations[id] = a
	}
	return a
}

func collisionFrom(name string, box []string) Collision {
	c := Collision{Name: strings.TrimSpace(name)}
	c.X1, _ = strconv.Atoi(box[0])
	c.Y1, _ = strconv.Atoi(box[1])
	c.X2, _ = strconv.Atoi(box[2])
	c.Y2, _ = strconv.Atoi(box[3])
	return c
}

// Valid reports whether a collision is a real rectangle. Shells disable one
// for a surface by setting every coordinate to -1 rather than removing the
// line, so this is the ordinary case and not a malformed file.
func (c Collision) Valid() bool { return c.X2 > c.X1 && c.Y2 > c.Y1 }

// parseInterval splits `random,5` into its name and its number.
func parseInterval(fields []string) (string, int) {
	if len(fields) == 0 {
		return "", 0
	}
	name := strings.ToLower(strings.TrimSpace(fields[0]))
	every := 0
	if len(fields) > 1 {
		every, _ = strconv.Atoi(strings.TrimSpace(fields[1]))
	}
	return name, every
}

// splitFields splits a comma list and trims each field.
func splitFields(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// parseIDList reads `[0]` or `[1,101,201]`.
func parseIDList(s string) []int {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	var out []int
	for _, f := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// parseSurfaceSpec reads the ID part of a surface block name: "0",
// "1-10", "1,3,5", "1-10,!5".
func parseSurfaceSpec(spec string) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("no surface id")
	}
	include := map[int]bool{}
	var order []int
	exclude := map[int]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		neg := strings.HasPrefix(part, "!")
		part = strings.TrimPrefix(part, "!")
		lo, hi, err := parseRange(part)
		if err != nil {
			return nil, err
		}
		for n := lo; n <= hi; n++ {
			if neg {
				exclude[n] = true
				continue
			}
			if !include[n] {
				include[n] = true
				order = append(order, n)
			}
		}
	}
	out := make([]int, 0, len(order))
	for _, n := range order {
		if !exclude[n] {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no surface ids in %q", spec)
	}
	return out, nil
}

func parseRange(part string) (int, int, error) {
	if lo, hi, ok := strings.Cut(part, "-"); ok && lo != "" {
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := strconv.Atoi(strings.TrimSpace(hi))
		if err1 != nil || err2 != nil {
			return 0, 0, fmt.Errorf("bad surface range %q", part)
		}
		if b < a {
			a, b = b, a
		}
		// A shell asking for ten thousand surfaces has a typo in it, and
		// obeying would allocate until something gave way.
		if b-a > 4096 {
			return 0, 0, fmt.Errorf("surface range %q is implausibly wide", part)
		}
		return a, b, nil
	}
	n, err := strconv.Atoi(part)
	if err != nil {
		return 0, 0, fmt.Errorf("bad surface id %q", part)
	}
	return n, n, nil
}
