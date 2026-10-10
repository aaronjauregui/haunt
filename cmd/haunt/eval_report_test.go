//go:build eval

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/config"
)

// A run leaves a directory under EVAL_OUT named for when it started:
// results.json, which is the whole record and what a baseline is, and
// report.md, which is what a person reads. Each graded trial's trace and
// log are kept beside them under trials/, so a failure can be read in
// full. `make eval-baseline` copies the newest results.json to
// EVAL_OUT/baseline.json, and later runs report each case's pass rate
// against it. A baseline belongs to a model: against another model's the
// report says so and gives no deltas.

// evalRun is one run, as results.json records it.
type evalRun struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	// Model is [model] name as wisp thinks with it, and ModelHost the host
	// of its base_url: where it ran, never the key.
	Model         string       `json:"model"`
	ModelHost     string       `json:"model_host,omitempty"`
	GitRev        string       `json:"git_rev"`
	Dirty         bool         `json:"dirty"`
	Trials        int          `json:"trials"`
	Judge         bool         `json:"judge"`
	CasesFilter   string       `json:"cases_filter,omitempty"`
	Prerequisites prereqs      `json:"prerequisites"`
	Cases         []caseResult `json:"cases"`
	// Face is the run's face and pace (eval_face_test.go); absent from a
	// run older than it.
	Face *faceStats `json:"face,omitempty"`

	// base is the baseline this run is compared with, and baseNote why
	// there is none or why its deltas are not given.
	base     *evalRun
	baseNote string
}

// caseResult is one case's trials and the tallies over them.
type caseResult struct {
	Name string `json:"name"`
	// Skipped says why the case did not run at all (a missing
	// prerequisite); its trials are then all skips.
	Skipped string        `json:"skipped,omitempty"`
	Pass    int           `json:"pass"`
	Fail    int           `json:"fail"`
	Error   int           `json:"error"`
	Skip    int           `json:"skip"`
	Rate    *float64      `json:"rate"` // pass / (pass + fail); null with none graded
	Graders []graderTally `json:"graders,omitempty"`
	Trials  []trialResult `json:"trials"`
}

// graderTally is one grader's outcomes across a case's trials, which is
// where a regression usually shows first.
type graderTally struct {
	Name  string `json:"name"`
	Pass  int    `json:"pass"`
	Fail  int    `json:"fail"`
	Error int    `json:"error"`
	Skip  int    `json:"skip"`
}

// trialResult is one trial: its outcome, and each grader's.
type trialResult struct {
	Index    int            `json:"index"`
	Outcome  string         `json:"outcome"`
	Why      string         `json:"why,omitempty"`
	Duration string         `json:"duration,omitempty"`
	Turns    int            `json:"turns"`
	Graders  []graderResult `json:"graders,omitempty"`

	ev *Evidence // kept for the trace files and the report, not the JSON
}

// attach keeps the trial's evidence for the report.
func (r *trialResult) attach(ev Evidence) {
	r.Turns = len(ev.Turns)
	r.ev = &ev
}

// graderResult is one grader on one trial.
type graderResult struct {
	Name    string        `json:"name"`
	Outcome string        `json:"outcome"`
	Why     string        `json:"why,omitempty"`
	Judged  bool          `json:"judged,omitempty"`
	Replies []judgedReply `json:"replies,omitempty"`
}

// judgedReply is one reply the judge read, with its verdict on each
// criterion.
type judgedReply struct {
	Turn     int                `json:"turn"`
	Reply    string             `json:"reply"`
	Verdicts []criterionVerdict `json:"verdicts"`
}

type criterionVerdict struct {
	ID     string `json:"id"`
	Pass   bool   `json:"pass"`
	Reason string `json:"reason"`
}

func newEvalRun(opts evalOptions, pre prereqs, user *config.File, started time.Time) *evalRun {
	r := &evalRun{
		Started:       started,
		Model:         modelName(user),
		ModelHost:     modelHostOf(user),
		Trials:        opts.Trials,
		Judge:         opts.Judge && judgeFn != nil,
		Prerequisites: pre,
	}
	if opts.Cases != nil {
		r.CasesFilter = opts.Cases.String()
	}
	r.GitRev, r.Dirty = gitState()
	return r
}

// skippedCase is a case none of whose trials ran.
func skippedCase(c evalCase, k int, why string) caseResult {
	cr := caseResult{Name: c.Name, Skipped: why}
	for i := range k {
		cr.Trials = append(cr.Trials, trialResult{Index: i + 1, Outcome: outSkip, Why: why})
	}
	return cr
}

// gitState is the revision under test and whether the tree has changes
// beyond it; "unknown" outside a repository.
func gitState() (rev string, dirty bool) {
	root, err := repoRoot()
	if err != nil {
		return "unknown", false
	}
	out, err := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown", false
	}
	st, _ := exec.Command("git", "-C", root, "status", "--porcelain").Output()
	return strings.TrimSpace(string(out)), len(strings.TrimSpace(string(st))) > 0
}

// finish tallies every case.
func (r *evalRun) finish(at time.Time) {
	r.Finished = at
	for i := range r.Cases {
		r.Cases[i].tally()
	}
	r.Face = faceOf(r.Cases)
}

func (c *caseResult) tally() {
	c.Pass, c.Fail, c.Error, c.Skip = 0, 0, 0, 0
	c.Graders = nil
	index := map[string]int{}
	for _, t := range c.Trials {
		switch t.Outcome {
		case outPass:
			c.Pass++
		case outFail:
			c.Fail++
		case outError:
			c.Error++
		default:
			c.Skip++
		}
		for _, g := range t.Graders {
			i, ok := index[g.Name]
			if !ok {
				i = len(c.Graders)
				index[g.Name] = i
				c.Graders = append(c.Graders, graderTally{Name: g.Name})
			}
			switch g.Outcome {
			case outPass:
				c.Graders[i].Pass++
			case outFail:
				c.Graders[i].Fail++
			case outError:
				c.Graders[i].Error++
			default:
				c.Graders[i].Skip++
			}
		}
	}
	c.Rate = nil
	if n := c.Pass + c.Fail; n > 0 {
		rate := float64(c.Pass) / float64(n)
		c.Rate = &rate
	}
}

// loadBaseline reads a results.json kept as the baseline, and says why it
// cannot be compared with, if it cannot.
func (r *evalRun) loadBaseline(path string) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		r.baseNote = "no baseline (make eval-baseline keeps this run's results as one)"
		return
	}
	if err != nil {
		r.baseNote = "baseline unreadable: " + err.Error()
		return
	}
	var base evalRun
	if err := json.Unmarshal(b, &base); err != nil {
		r.baseNote = "baseline unreadable: " + err.Error()
		return
	}
	if base.Model != r.Model {
		r.baseNote = fmt.Sprintf("the baseline is for %s, not %s; no deltas", base.Model, r.Model)
		return
	}
	r.base = &base
	r.baseNote = fmt.Sprintf("baseline: %s, rev %s, k=%d", base.Started.Format(time.DateTime), base.GitRev, base.Trials)
}

// baseCase is the baseline's result for a case, if it has one.
func (r *evalRun) baseCase(name string) *caseResult {
	if r.base == nil {
		return nil
	}
	for i := range r.base.Cases {
		if r.base.Cases[i].Name == name {
			return &r.base.Cases[i]
		}
	}
	return nil
}

// strictFailures is why EVAL_STRICT=1 fails the run: a case skipped for
// want of a prerequisite, or a case more than one trial below the
// baseline's pass rate (scaled to this run's graded trials).
func (r *evalRun) strictFailures() []string {
	var out []string
	for _, c := range r.Cases {
		if c.Skipped != "" {
			out = append(out, fmt.Sprintf("%s: %s", c.Name, c.Skipped))
			continue
		}
		if c.Skip > 0 {
			out = append(out, fmt.Sprintf("%s: %d of %d trials skipped", c.Name, c.Skip, len(c.Trials)))
		}
		b := r.baseCase(c.Name)
		if b == nil || b.Rate == nil {
			continue
		}
		graded := c.Pass + c.Fail
		if graded == 0 {
			out = append(out, fmt.Sprintf("%s: no trial graded (baseline %s)", c.Name, pct(b.Rate)))
			continue
		}
		if drop := (*b.Rate - *c.Rate) * float64(graded); drop > 1+1e-9 {
			out = append(out, fmt.Sprintf("%s: %s against a baseline of %s, %.1f trials below", c.Name, pct(c.Rate), pct(b.Rate), drop))
		}
	}
	return out
}

// write makes the run's directory under root and fills it: results.json,
// report.md and the graded trials' traces. It returns the directory.
func (r *evalRun) write(root string) (string, error) {
	r.loadBaseline(filepath.Join(root, "baseline.json"))
	dir := filepath.Join(root, r.Started.Format("20060102-150405"))
	for n := 2; ; n++ {
		if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
			break
		}
		dir = filepath.Join(root, fmt.Sprintf("%s-%d", r.Started.Format("20060102-150405"), n))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "results.json"), append(b, '\n'), 0o644); err != nil {
		return "", err
	}
	for _, c := range r.Cases {
		for _, t := range c.Trials {
			if t.ev == nil {
				continue
			}
			td := filepath.Join(dir, "trials", c.Name, fmt.Sprint(t.Index))
			if err := os.MkdirAll(td, 0o755); err != nil {
				return "", err
			}
			tb, _ := json.MarshalIndent(t.ev.Turns, "", "  ")
			os.WriteFile(filepath.Join(td, "trace.json"), append(tb, '\n'), 0o644)
			os.WriteFile(filepath.Join(td, "haunt.log"), []byte(t.ev.Log), 0o644)
			if t.ev.Intentions != "" {
				os.WriteFile(filepath.Join(td, "intentions.txt"), []byte(t.ev.Intentions), 0o644)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(r.report()), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// report renders report.md: what did not run at the top, then the pass
// rates against the baseline, the per-grader rates, and each failing or
// erroring trial with its reasons and trace.
func (r *evalRun) report() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Eval %s\n\n", r.Started.Format("2006-01-02 15:04:05"))
	dirty := ""
	if r.Dirty {
		dirty = " (dirty)"
	}
	judge := "on"
	if !r.Judge {
		judge = "off: judged graders skipped"
	}
	host := ""
	if r.ModelHost != "" {
		host = " at " + r.ModelHost
	}
	fmt.Fprintf(&b, "Model %s%s · rev %s%s · k=%d · judge %s · %s\n\n",
		r.Model, host, r.GitRev, dirty, r.Trials, judge, r.Finished.Sub(r.Started).Round(time.Second))
	if r.CasesFilter != "" {
		fmt.Fprintf(&b, "Cases matching `%s`.\n\n", r.CasesFilter)
	}

	var notRun []string
	for _, p := range r.Prerequisites {
		if !p.Found && p.Required {
			notRun = append(notRun, fmt.Sprintf("- **missing** %s: %s", p.Name, p.Detail))
		}
	}
	for _, p := range r.Prerequisites {
		if !p.Found && !p.Required {
			notRun = append(notRun, fmt.Sprintf("- %s: %s", p.Name, p.Detail))
		}
	}
	for _, c := range r.Cases {
		switch {
		case c.Skipped != "":
			notRun = append(notRun, fmt.Sprintf("- case `%s` %s", c.Name, c.Skipped))
		case c.Skip > 0:
			notRun = append(notRun, fmt.Sprintf("- case `%s`: %d of %d trials skipped", c.Name, c.Skip, len(c.Trials)))
		}
	}
	if len(notRun) > 0 {
		b.WriteString("## Not run\n\n" + strings.Join(notRun, "\n") + "\n\n")
	}

	b.WriteString("## Cases\n\n")
	b.WriteString(r.baseNote + "\n\n")
	b.WriteString("| case | pass | fail | error | skip | rate | baseline | Δ |\n|---|---|---|---|---|---|---|---|\n")
	for _, c := range r.Cases {
		base, delta := "", ""
		if bc := r.baseCase(c.Name); bc != nil {
			base = pct(bc.Rate)
			if bc.Rate != nil && c.Rate != nil {
				delta = fmt.Sprintf("%+.0f", (*c.Rate-*bc.Rate)*100)
			}
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %s | %s | %s |\n",
			c.Name, c.Pass, c.Fail, c.Error, c.Skip, pct(c.Rate), base, delta)
	}
	b.WriteString("\n")

	var baseFace *faceStats
	if r.base != nil {
		baseFace = r.base.Face
	}
	b.WriteString(faceReport(r.Face, baseFace))

	b.WriteString("## Graders\n\n")
	for _, c := range r.Cases {
		if len(c.Graders) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n| grader | pass | fail | error | skip |\n|---|---|---|---|---|\n", c.Name)
		for _, g := range c.Graders {
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d |\n", g.Name, g.Pass, g.Fail, g.Error, g.Skip)
		}
		b.WriteString("\n")
	}

	var bad strings.Builder
	for _, c := range r.Cases {
		for _, t := range c.Trials {
			if t.Outcome != outFail && t.Outcome != outError {
				continue
			}
			fmt.Fprintf(&bad, "### %s #%d: %s\n\n", c.Name, t.Index, t.Outcome)
			if t.Why != "" {
				fmt.Fprintf(&bad, "%s\n\n", t.Why)
			}
			for _, g := range t.Graders {
				if g.Outcome != outFail && g.Outcome != outError {
					continue
				}
				fmt.Fprintf(&bad, "- **%s** %s: %s\n", g.Name, g.Outcome, g.Why)
				for _, jr := range g.Replies {
					for _, v := range jr.Verdicts {
						if !v.Pass {
							fmt.Fprintf(&bad, "  - turn %d, %s: %s\n", jr.Turn, v.ID, v.Reason)
						}
					}
				}
			}
			if t.ev != nil {
				fmt.Fprintf(&bad, "\n<details><summary>trace (%d turns)</summary>\n\n```\n%s```\n\n</details>\n", len(t.ev.Turns), traceText(t.ev.Turns))
			}
			bad.WriteString("\n")
		}
	}
	if bad.Len() > 0 {
		b.WriteString("## Failing trials\n\n" + bad.String())
	}
	return b.String()
}

// traceText is a trace as the report shows it: one block per turn, what
// provoked it, what the ghost called, and what it said.
func traceText(turns []TraceTurn) string {
	var b strings.Builder
	for i, tu := range turns {
		fmt.Fprintf(&b, "[%d] %s %s: %s\n", i, tu.Time.Format("15:04:05"), tu.ProvokedBy, oneLine(tu.Input, 200))
		if tu.Window != nil {
			fmt.Fprintf(&b, "    window: %s — %s\n", tu.Window.App, tu.Window.Title)
		}
		for _, c := range tu.Looked {
			fmt.Fprintf(&b, "    looked %s %s → %s\n", c.Tool, oneLine(c.Args, 120), oneLine(c.Result, 120))
		}
		for _, c := range tu.Acted {
			fmt.Fprintf(&b, "    acted %s %s → %s\n", c.Tool, oneLine(c.Args, 160), oneLine(c.Result, 120))
		}
		switch {
		case tu.Silent:
			b.WriteString("    (silent)\n")
		default:
			fmt.Fprintf(&b, "    → %s\n", oneLine(tu.Reply, 400))
		}
	}
	return b.String()
}

// pct renders a rate, or "–" when there is none.
func pct(r *float64) string {
	if r == nil {
		return "–"
	}
	return fmt.Sprintf("%.0f%%", *r*100)
}
