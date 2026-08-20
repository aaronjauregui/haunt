package seriko

import (
	"reflect"
	"testing"
)

func parse(t *testing.T, s string) *Shell {
	t.Helper()
	sh, err := Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

// The SERIKO/1.x spelling is what almost every shell in the wild uses, and
// its argument order is different from 2.0's — reading one as the other
// silently swaps the surface for the wait.
func TestParseAnimation1x(t *testing.T) {
	sh := parse(t, `surface0
{
0interval,sometimes
0pattern0,101,5,overlay,172,156
0pattern1,102,5,overlay,172,156
0pattern2,-1,10,overlay,172,156
}
`)
	s := sh.Surfaces[0]
	if s == nil {
		t.Fatal("surface 0 missing")
	}
	a := s.Animations[0]
	if a == nil || a.Interval != "sometimes" {
		t.Fatalf("animation 0 = %+v", a)
	}
	if len(a.Patterns) != 3 {
		t.Fatalf("%d patterns, want 3", len(a.Patterns))
	}
	// 1.x waits are hundredths of a second: 5 means 50ms, not 5ms.
	want := Pattern{Surface: 101, WaitMS: 50, Method: "overlay", X: 172, Y: 156}
	if a.Patterns[0] != want {
		t.Errorf("pattern0 = %+v, want %+v", a.Patterns[0], want)
	}
	if !a.Patterns[2].Clearing() {
		t.Error("surface -1 should clear the frame")
	}
}

// A file that declares version 1 is using 2.0 semantics, including the wait
// unit, even where it still spells animations the old way.
func TestParseVersionChangesTheWaitUnit(t *testing.T) {
	sh := parse(t, `descript
{
version,1
}

surface0
{
0interval,sometimes
0pattern0,101,50,overlay,0,0
}
`)
	if got := sh.Surfaces[0].Animations[0].Patterns[0].WaitMS; got != 50 {
		t.Errorf("wait = %dms, want 50 — version 1 means milliseconds", got)
	}
}

func TestParseAnimation2x(t *testing.T) {
	sh := parse(t, `descript
{
version,1
}

surface0
{
animation0.interval,random,5
animation0.option,exclusive
animation0.pattern0,overlay,101,100,168,67
animation0.pattern1,overlay,-1,0,0,0
}
`)
	a := sh.Surfaces[0].Animations[0]
	if a.Interval != "random" || a.Every != 5 {
		t.Errorf("interval = %q every %d, want random 5", a.Interval, a.Every)
	}
	if len(a.Options) != 1 || a.Options[0] != "exclusive" {
		t.Errorf("options = %v", a.Options)
	}
	want := Pattern{Surface: 101, WaitMS: 100, Method: "overlay", X: 168, Y: 67}
	if a.Patterns[0] != want {
		t.Errorf("pattern0 = %+v, want %+v", a.Patterns[0], want)
	}
}

func TestParseCollisions(t *testing.T) {
	sh := parse(t, `surface0
{
collision0,160,106,250,130,head
collision1,-1,-1,-1,-1,face
collisionex2,Bust,rect,10,20,30,40
collisionex3,Hair,polygon,1,2,3,4,5,6
}
`)
	cs := sh.Surfaces[0].Collisions
	if len(cs) != 3 {
		t.Fatalf("%d collisions, want 3 (the polygon is skipped)", len(cs))
	}
	if cs[0] != (Collision{Name: "head", X1: 160, Y1: 106, X2: 250, Y2: 130}) {
		t.Errorf("collision0 = %+v", cs[0])
	}
	// Shells switch a collision off per surface by setting it to -1 rather
	// than deleting the line, so this is ordinary rather than malformed.
	if cs[1].Valid() {
		t.Error("an all -1 collision should not be a valid rectangle")
	}
	if cs[2] != (Collision{Name: "Bust", X1: 10, Y1: 20, X2: 30, Y2: 40}) {
		t.Errorf("collisionex = %+v", cs[2])
	}
}

func TestParseElements(t *testing.T) {
	sh := parse(t, `surface0
{
element0,base,body.png,0,0
element1,overlay,face.PNG,50,20
}
`)
	els := sh.Surfaces[0].Elements
	if len(els) != 2 {
		t.Fatalf("%d elements, want 2", len(els))
	}
	if els[1] != (Element{Method: "overlay", File: "face.PNG", X: 50, Y: 20}) {
		t.Errorf("element1 = %+v", els[1])
	}
}

// SSP's range spellings, which real shells do use to give two hundred
// surfaces the same collision boxes.
func TestParseSurfaceRanges(t *testing.T) {
	sh := parse(t, `surface1-5,!3
{
collision0,0,0,10,10,head
}

surface10,12
{
collision0,0,0,10,10,head
}
`)
	for _, id := range []int{1, 2, 4, 5, 10, 12} {
		if sh.Surfaces[id] == nil {
			t.Errorf("surface %d missing", id)
		}
	}
	if sh.Surfaces[3] != nil {
		t.Error("surface 3 was excluded with !3 and should not exist")
	}
	if sh.Surfaces[11] != nil {
		t.Error("surface 11 is not in the enumeration 10,12")
	}
}

// .append must not conjure surfaces, which is the whole reason it exists.
func TestParseAppendOnlyTouchesExisting(t *testing.T) {
	sh := parse(t, `surface0
{
collision0,0,0,10,10,head
}

surface.append0-2
{
collision1,0,0,5,5,face
}
`)
	if got := len(sh.Surfaces[0].Collisions); got != 2 {
		t.Errorf("surface 0 has %d collisions, want 2", got)
	}
	if sh.Surfaces[1] != nil || sh.Surfaces[2] != nil {
		t.Error(".append created a surface that did not exist")
	}
}

func TestParseAliases(t *testing.T) {
	sh := parse(t, `sakura.surface.alias
{
Default,[0]
Embarrassed,[1,101,201]
}
`)
	if !reflect.DeepEqual(sh.Aliases["Embarrassed"], []int{1, 101, 201}) {
		t.Errorf("aliases = %v", sh.Aliases)
	}
}

// A brace on the same line as its block name is invalid SERIKO and SSP
// rejects it; half-reading it would baffle whoever wrote the file.
func TestParseRejectsInlineBrace(t *testing.T) {
	if _, err := Parse([]byte("surface0 {\ncollision0,0,0,1,1,head\n}\n")); err == nil {
		t.Error("an inline brace should be an error")
	}
}

func TestParseRejectsUnbalancedBraces(t *testing.T) {
	if _, err := Parse([]byte("surface0\n{\ncollision0,0,0,1,1,head\n")); err == nil {
		t.Error("an unclosed block should be an error")
	}
}

// A range wide enough to exhaust memory is a typo, not a shell.
func TestParseRejectsAbsurdRange(t *testing.T) {
	if _, err := Parse([]byte("surface1-999999\n{\n}\n")); err == nil {
		t.Error("an implausibly wide range should be refused")
	}
}

// These files are Shift-JIS far more often than not, and the charset line
// that would say so is inside the file.
func TestDecodeShiftJIS(t *testing.T) {
	// "まゆら" in Shift-JIS.
	sjis := []byte{0x82, 0xdc, 0x82, 0xe4, 0x82, 0xe7}
	if got := Decode(sjis); got != "まゆら" {
		t.Errorf("Decode(Shift-JIS) = %q, want まゆら", got)
	}
	if got := Decode([]byte("already utf-8 ✓")); got != "already utf-8 ✓" {
		t.Errorf("Decode(UTF-8) = %q", got)
	}
}

func TestParseDescript(t *testing.T) {
	d := ParseDescript([]byte("charset,Shift_JIS\nname,mayura\ntype,shell\n// a comment\n"))
	if d["name"] != "mayura" || d["type"] != "shell" {
		t.Errorf("descript = %v", d)
	}
}
