//go:build eval

package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// Face and pace: how the ghost wore its words and how long it took to
// find them, over a whole run. A pass rate says whether the ghost did the
// right thing; these say whether it felt alive doing it, and whether it
// kept the person waiting. They are read from the line that ends every
// turn in the ghost's log, which records how long the turn took once the
// gateway was up and which face went with the words, and by whose choice:
//
//	turn over: person took=4.2s face=happy by=tag
//	turn over: event kind=boot took=21.3s face=-
//	turn over: event kind=idle took=3.1s silent
//
// A face by "tool" cost a model round trip; one by "tag" or "lexicon" did
// not. Fewer faces changed, or fewer poses over a trial, is a ghost gone
// wooden, whatever its pass rate.

// faceStats is a run's face and pace, as results.json records it.
type faceStats struct {
	// Spoken is the turns that put words on screen.
	Spoken int `json:"spoken"`
	// ByTag, ByTool, ByLexicon and Unchanged split Spoken by how the face
	// went with the words.
	ByTag     int `json:"by_tag"`
	ByTool    int `json:"by_tool"`
	ByLexicon int `json:"by_lexicon"`
	Unchanged int `json:"unchanged"`
	// FaceOnly is the spoken turns whose only tools were face and clip:
	// a model round trip spent on nothing but a face.
	FaceOnly int `json:"face_only"`
	// PosesPerTrial is the mean number of distinct poses worn with words
	// in a trial.
	PosesPerTrial float64 `json:"poses_per_trial"`
	// BootMS and PersonMS are the median boot and person turn, from the
	// gateway being up to the turn being over.
	BootMS   int64 `json:"boot_ms"`
	PersonMS int64 `json:"person_ms"`
}

// turnLine is one parsed turn-over line.
type turnLine struct {
	person bool
	kind   string
	took   time.Duration
	spoke  bool
	face   string // "" when unchanged
	by     string
}

var turnOverRe = regexp.MustCompile(`turn over: (person|event)(?: kind=(\S+))?(?: took=(\S+))?(?: (silent|failed)| face=(\S+)(?: by=(\S+))?)?`)

// turnLines reads the turn-over lines from a ghost's log.
func turnLines(log string) []turnLine {
	var out []turnLine
	for _, m := range turnOverRe.FindAllStringSubmatch(log, -1) {
		l := turnLine{person: m[1] == "person", kind: m[2], spoke: m[5] != "", by: m[6]}
		if d, err := time.ParseDuration(m[3]); err == nil {
			l.took = d
		}
		if m[5] != "-" {
			l.face = m[5]
		}
		out = append(out, l)
	}
	return out
}

// faceOf tallies the face and pace of every trial that left evidence.
func faceOf(cases []caseResult) *faceStats {
	var (
		s            faceStats
		boot, person []time.Duration
		trials       int
		poses        int
	)
	for _, c := range cases {
		for _, t := range c.Trials {
			if t.ev == nil {
				continue
			}
			trials++
			worn := map[string]bool{}
			for _, l := range turnLines(t.ev.Log) {
				switch {
				case l.kind == "boot" && l.took > 0:
					boot = append(boot, l.took)
				case l.person && l.took > 0:
					person = append(person, l.took)
				}
				if !l.spoke {
					continue
				}
				s.Spoken++
				switch {
				case l.face == "":
					s.Unchanged++
				case l.by == "tag":
					s.ByTag++
				case l.by == "tool":
					s.ByTool++
				default:
					s.ByLexicon++
				}
				if l.face != "" {
					worn[l.face] = true
				}
			}
			poses += len(worn)
			for _, tu := range t.ev.Turns {
				if !tu.Silent && faceOnly(tu.Acted) {
					s.FaceOnly++
				}
			}
		}
	}
	if trials == 0 {
		return nil
	}
	s.PosesPerTrial = float64(poses) / float64(trials)
	s.BootMS = median(boot).Milliseconds()
	s.PersonMS = median(person).Milliseconds()
	return &s
}

// faceOnly is whether a turn's tools were all face and clip, and it had
// any.
func faceOnly(acted []TraceCall) bool {
	if len(acted) == 0 {
		return false
	}
	for _, c := range acted {
		if c.Tool != "face" && c.Tool != "clip" {
			return false
		}
	}
	return true
}

func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	ds = slices.Clone(ds)
	slices.Sort(ds)
	return ds[len(ds)/2]
}

// faceReport is the report's face-and-pace section, beside the baseline's
// when it has one.
func faceReport(s, base *faceStats) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Face and pace\n\n")
	b.WriteString("How the face went with the words, and how long a turn took once hermes was up. " +
		"A face by tool cost a model round trip; by tag or by the driver's guess, none. " +
		"Fewer faces changed or fewer poses per trial is a ghost gone wooden.\n\n")
	b.WriteString("| | this run | baseline |\n|---|---|---|\n")
	share := func(n, of int) string {
		if of == 0 {
			return fmt.Sprint(n)
		}
		return fmt.Sprintf("%d (%.0f%%)", n, float64(n)/float64(of)*100)
	}
	row := func(name string, get func(*faceStats) string) {
		was := ""
		if base != nil {
			was = get(base)
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", name, get(s), was)
	}
	row("spoken turns", func(f *faceStats) string { return fmt.Sprint(f.Spoken) })
	row("face by tag", func(f *faceStats) string { return share(f.ByTag, f.Spoken) })
	row("face by tool", func(f *faceStats) string { return share(f.ByTool, f.Spoken) })
	row("face by the driver's guess", func(f *faceStats) string { return share(f.ByLexicon, f.Spoken) })
	row("face unchanged", func(f *faceStats) string { return share(f.Unchanged, f.Spoken) })
	row("a round trip for a face alone", func(f *faceStats) string { return share(f.FaceOnly, f.Spoken) })
	row("distinct poses per trial", func(f *faceStats) string { return fmt.Sprintf("%.1f", f.PosesPerTrial) })
	row("boot turn, median", func(f *faceStats) string { return seconds(f.BootMS) })
	row("person turn, median", func(f *faceStats) string { return seconds(f.PersonMS) })
	b.WriteString("\n")
	return b.String()
}

func seconds(ms int64) string {
	if ms == 0 {
		return "–"
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// The turn-over lines are read back into the run's face and pace: the
// split by how each face was chosen, the round trips spent on a face
// alone, the poses worn per trial and the median turns.
func TestEvalFaceAndPace(t *testing.T) {
	log := strings.Join([]string{
		"haunt: [brain] turn over: event kind=boot took=20s face=happy by=tool",
		"haunt: [brain] turn over: person took=4s face=happy by=tag",
		"haunt: [brain] turn over: person took=6s face=-",
		"haunt: [brain] turn over: event kind=idle took=3s silent",
		"haunt: [brain] turn over: person took=2s face=sad by=lexicon",
		"haunt: [brain] turn over: person failed",
	}, "\n")
	lines := turnLines(log)
	if len(lines) != 6 || !lines[0].spoke || lines[0].kind != "boot" || lines[0].took != 20*time.Second ||
		lines[2].face != "" || !lines[2].spoke || lines[3].spoke || lines[5].spoke || lines[5].took != 0 {
		t.Fatalf("lines = %+v", lines)
	}
	ev := Evidence{Log: log, Turns: []TraceTurn{
		{Acted: []TraceCall{{Tool: "face"}}},
		{Acted: []TraceCall{{Tool: "face"}, {Tool: "draw"}}},
		{Silent: true, Acted: []TraceCall{{Tool: "face"}}},
		{},
	}}
	s := faceOf([]caseResult{{Trials: []trialResult{{ev: &ev}, {ev: &Evidence{}}}}})
	want := faceStats{Spoken: 4, ByTag: 1, ByTool: 1, ByLexicon: 1, Unchanged: 1, FaceOnly: 1,
		PosesPerTrial: 1, BootMS: 20000, PersonMS: 4000}
	if *s != want {
		t.Errorf("stats = %+v\nwant    %+v", *s, want)
	}
	report := faceReport(s, &faceStats{Spoken: 2, ByTool: 2})
	for _, w := range []string{"## Face and pace", "| face by tag | 1 (25%) | 0 (0%) |", "| face by tool | 1 (25%) | 2 (100%) |",
		"| distinct poses per trial | 1.0 | 0.0 |", "| boot turn, median | 20.0s | – |"} {
		if !strings.Contains(report, w) {
			t.Errorf("report lacks %q:\n%s", w, report)
		}
	}
	if faceOf(nil) != nil || faceReport(nil, nil) != "" {
		t.Error("a run with no evidence has face stats")
	}
}

// A turn's kind is found among the line's other fields, and a kind that is
// the prefix of another is not counted as it.
func TestTurnsOfKind(t *testing.T) {
	log := "haunt: [brain] turn over: event kind=boot took=20s face=happy by=tool\n" +
		"haunt: [brain] turn over: event kind=boot\n" +
		"haunt: [brain] turn over: event kind=bootleg took=1s silent\n" +
		"haunt: [brain] intention due kind=boot\n"
	if n := turnsOfKind(log, "boot"); n != 2 {
		t.Errorf("%d boot turns, want 2", n)
	}
}
