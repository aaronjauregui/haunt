//go:build eval

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/ghost"
)

// The evals hold the persona ghost to what it is meant to do, on a real
// model: each case drives wisp through a scenario on its own compositor
// and state, k times, against the model in the person's [model], and
// graders read what it left behind. A grader that fails records an
// outcome and never calls t.Error, because a model that behaves badly one
// trial in three is a pass rate to watch, not a broken build. Only the
// harness's own faults (sway will not start, the ghost never comes up, an
// event the case waited on never arrived) end a trial as an error, and
// those are reported apart from failures.
//
// This file is the harness: the case table, the trials, the outcomes, the
// prerequisite checks, the grader helpers and the checks every trial gets.
// The rig that drives a real haunt on a real model is eval_rig_test.go,
// the judge is eval_judge_test.go, the cases are eval_cases_test.go, and
// the results and report are eval_report_test.go. `make eval` runs it.

// evalCase is one scenario. Drive sets the stack up, plays the scenario
// and returns what it left behind; the graders, and the every-trial
// checks after them, grade it.
type evalCase struct {
	Name string
	// Needs names optional prerequisites the case cannot run without
	// ("foot", "backend.image", "backend.vision"). Without one, every trial of the
	// case is skipped and the report says why at the top.
	Needs []string
	// MaxWords raises always/length's cap of 60 words for a case whose
	// replies are meant to run longer (a story, a rewrite). A lower value
	// does not lower the cap; a case wanting shorter replies grades that
	// itself.
	MaxWords int
	// Tools names tools the case offers beyond the ghost's own verbs, such
	// as a tool server's `append`, so always/no-tool-names knows them.
	Tools []string
	// Drive plays one trial (see evalTrial).
	Drive func(tr *evalTrial) (Evidence, error)
	// Graders are the case's own checks, code and judged.
	Graders []Grader
}

// evalCases is the case table. eval_cases_test.go fills it from an init
// function, one entry per case; the harness runs them in name order.
var evalCases []evalCase

// evalTrial is what the harness hands a case's Drive: the trial's own
// subtest, already parallel, with its temp dirs and cleanups, and the
// person's config read once for the run. The rig builds everything on it
// (testenv.StartT(tr.T), tr.TempDir(), tr.Cleanup) so that nothing is
// shared between trials.
//
// Drive reports a harness fault by returning an error, which records the
// trial as an error. A t.Fatal inside it (testenv's, for instance) is
// caught and recorded the same way, and a t.Skip as a skip; neither is a
// way to grade, and Drive never calls t.Error for something the ghost did.
type evalTrial struct {
	*testing.T
	Case  *evalCase
	Index int          // 1-based trial number within the case
	User  *config.File // the person's config: [model], [backend.*], read once
}

// judgeItem is one reply a judged grader puts to the judge, with the
// context the judge reads it in: what provoked it, and any tool result or
// envelope the rubric names.
type judgeItem struct {
	Turn    int
	Context string
	Reply   string
}

// judgeFn is the judge: it holds one reply to a rubric and answers every
// criterion, with what the call cost. eval_judge_test.go sets it. While it
// is nil, or EVAL_JUDGE=off, judged graders are skipped, which the report
// shows as skipped rather than passed.
var judgeFn func(r *Rubric, context, reply string) (map[string]Verdict, judgeUsage, error)

// errSkipTrial wraps the reason a Drive gives for skipping its trial
// (something only known once the trial started, like a tool the stack
// turned out not to have).
type errSkipTrial struct{ reason string }

func (e errSkipTrial) Error() string { return "skipped: " + e.reason }

// skipTrial is what a Drive returns to skip its trial rather than fail it.
func skipTrial(format string, args ...any) error {
	return errSkipTrial{fmt.Sprintf(format, args...)}
}

// Outcomes, for a grader and for a trial.
const (
	outPass  = "pass"
	outFail  = "fail"
	outError = "error"
	outSkip  = "skip"
)

// evalOptions are the run's knobs, from the environment.
type evalOptions struct {
	Trials int            // EVAL_TRIALS, default 3
	Cases  *regexp.Regexp // EVAL_CASES, a regexp on the case name; nil runs all
	Out    string         // EVAL_OUT, default .eval; relative to the repository root
	Judge  bool           // EVAL_JUDGE, on unless "off"
	Strict bool           // EVAL_STRICT=1
}

func evalOptionsFromEnv(getenv func(string) string) (evalOptions, error) {
	o := evalOptions{Trials: 3, Judge: true}
	if s := getenv("EVAL_TRIALS"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return o, fmt.Errorf("EVAL_TRIALS=%q: want a whole number of at least 1", s)
		}
		o.Trials = n
	}
	if s := getenv("EVAL_CASES"); s != "" {
		re, err := regexp.Compile(s)
		if err != nil {
			return o, fmt.Errorf("EVAL_CASES=%q: %v", s, err)
		}
		o.Cases = re
	}
	// A relative EVAL_OUT is from the repository's root, as the Makefile
	// means it, not from cmd/haunt where the test runs.
	o.Out = getenv("EVAL_OUT")
	if o.Out == "" {
		o.Out = ".eval"
	}
	if !filepath.IsAbs(o.Out) {
		root, err := repoRoot()
		if err != nil {
			return o, err
		}
		o.Out = filepath.Join(root, o.Out)
	}
	o.Judge = getenv("EVAL_JUDGE") != "off"
	o.Strict = getenv("EVAL_STRICT") == "1"
	return o, nil
}

// repoRoot is the directory holding go.mod, found upward from the test's
// working directory (cmd/haunt).
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		up := filepath.Dir(dir)
		if up == dir {
			return "", errors.New("no go.mod above the working directory")
		}
		dir = up
	}
}

// TestEval runs every selected case EVAL_TRIALS times against the real
// model in [model], then writes results.json and report.md under EVAL_OUT and
// compares them with the baseline. It never runs half a stack: when a
// required prerequisite is missing it writes the report saying which and
// skips (or, under EVAL_STRICT=1, fails).
func TestEval(t *testing.T) {
	opts, err := evalOptionsFromEnv(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	user, cfgErr := config.Load()
	pre := checkPrerequisites(systemPrereqEnv(user, cfgErr, opts.Judge))

	var cases []evalCase
	for _, c := range evalCases {
		if opts.Cases == nil || opts.Cases.MatchString(c.Name) {
			cases = append(cases, c)
		}
	}
	slices.SortFunc(cases, func(a, b evalCase) int { return strings.Compare(a.Name, b.Name) })
	for _, p := range pre {
		if !p.Found {
			t.Logf("prerequisite %s: %s", p.Name, p.Detail)
		}
	}
	if len(cases) == 0 {
		t.Skipf("no case to run (%d in the table, EVAL_CASES=%q)", len(evalCases), os.Getenv("EVAL_CASES"))
	}

	started := time.Now()
	var run *evalRun
	if missing := pre.missing(); len(missing) > 0 {
		run = newEvalRun(opts, pre, user, cases, started)
		for _, c := range cases {
			run.Cases = append(run.Cases, skippedCase(c, opts.Trials, "prerequisites missing"))
		}
	} else {
		run = runEval(t, cases, opts, pre, user)
	}
	if run.Judge {
		run.ClaudeVersion = claudeVersion()
	}
	run.finish(time.Now())

	dir, err := run.write(opts.Out)
	if err != nil {
		t.Fatalf("writing results: %v", err)
	}
	t.Logf("eval results: %s", dir)
	t.Logf("report: %s", filepath.Join(dir, "report.md"))

	if missing := pre.missing(); len(missing) > 0 {
		msg := "prerequisites missing: " + strings.Join(missing, "; ")
		if opts.Strict {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	if opts.Strict {
		for _, why := range run.strictFailures() {
			t.Error(why)
		}
	}
}

// runEval runs each case's trials in parallel under one t.Run group, so
// that when the group returns every trial is done, and gathers their
// outcomes. It is TestEval without the environment, so the harness's own
// tests can run it over canned Drives.
func runEval(t *testing.T, cases []evalCase, opts evalOptions, pre prereqs, user *config.File) *evalRun {
	run := newEvalRun(opts, pre, user, cases, time.Now())
	results := make([]caseResult, len(cases))
	t.Run("cases", func(t *testing.T) {
		for ci := range cases {
			c := &cases[ci]
			if why := pre.unmet(c.Needs); why != "" {
				results[ci] = skippedCase(*c, opts.Trials, why)
				continue
			}
			results[ci] = caseResult{Name: c.Name, Trials: make([]trialResult, opts.Trials)}
			for i := range opts.Trials {
				slot := &results[ci].Trials[i]
				t.Run(fmt.Sprintf("%s/%d", c.Name, i+1), func(t *testing.T) {
					t.Parallel()
					runTrial(&evalTrial{T: t, Case: c, Index: i + 1, User: user}, opts, slot)
				})
			}
		}
	})
	run.Cases = results
	return run
}

// runTrial drives one trial, grades it and writes the result to out. A
// Drive that returns an error, panics or stops the subtest (t.Fatal,
// t.Skip) still leaves a result: out is written from a deferred function,
// which runs on the way out of a Goexit too.
func runTrial(tr *evalTrial, opts evalOptions, out *trialResult) {
	res := trialResult{Index: tr.Index}
	start := time.Now()
	returned := false
	defer func() {
		res.Duration = time.Since(start).Round(time.Millisecond).String()
		if r := recover(); r != nil {
			res.Outcome, res.Why = outError, fmt.Sprintf("drive panicked: %v\n%s", r, debug.Stack())
		} else if !returned {
			// Goexit, from t.Fatal or t.Skip inside Drive: the subtest is
			// ending, and all that can be known is which.
			if tr.Skipped() {
				res.Outcome, res.Why = outSkip, "the rig skipped the trial (see the test log)"
			} else {
				res.Outcome, res.Why = outError, "the rig stopped the trial (t.Fatal; see the test log)"
			}
		}
		*out = res
	}()
	if tr.Case.Drive == nil {
		returned = true
		res.Outcome, res.Why = outError, "the case has no Drive"
		return
	}
	ev, err := tr.Case.Drive(tr)
	returned = true
	var skip errSkipTrial
	switch {
	case errors.As(err, &skip):
		res.Outcome, res.Why = outSkip, skip.reason
		return
	case err != nil:
		res.Outcome, res.Why = outError, err.Error()
		res.attach(ev)
		return
	}
	res.attach(ev)
	for i, tu := range ev.Turns {
		if tu.Failed {
			// The provider gave out or refused the call: nothing the ghost
			// did, so nothing to grade it on.
			res.Outcome, res.Why = outError, fmt.Sprintf("the model failed turn %d (see the log)", i)
			return
		}
	}
	res.Graders = gradeTrial(tr.Case, ev, opts.Judge)
	res.Outcome = trialOutcome(res.Graders)
}

// gradeTrial runs the case's graders and then the every-trial checks.
func gradeTrial(c *evalCase, ev Evidence, judgeOn bool) []graderResult {
	gs := append(slices.Clone(c.Graders), alwaysGraders(c)...)
	out := make([]graderResult, 0, len(gs))
	for _, g := range gs {
		out = append(out, runGrader(c, g, ev, judgeOn))
	}
	return out
}

// runGrader runs one grader. A judged grader first runs its Check, when it
// has one, as a gate: the judge is asked only if that passes. A grader
// that panics is the harness's fault, not the ghost's.
func runGrader(c *evalCase, g Grader, ev Evidence, judgeOn bool) (res graderResult) {
	res = graderResult{Name: g.Name}
	defer func() {
		if r := recover(); r != nil {
			res.Outcome, res.Why = outError, fmt.Sprintf("grader panicked: %v", r)
		}
	}()
	if g.Check != nil {
		pass, why := g.Check(ev)
		res.Why = why
		if !pass {
			res.Outcome = outFail
			if g.Fault {
				res.Outcome = outError
			}
			return res
		}
		if g.Judge == nil {
			res.Outcome = outPass
			return res
		}
	}
	if g.Judge == nil {
		res.Outcome, res.Why = outError, "grader has neither a check nor a rubric"
		return res
	}
	res.Judged = true
	if !judgeOn || judgeFn == nil {
		res.Outcome, res.Why = outSkip, "judge off"
		return res
	}
	pick := spokenItems
	if g.Pick != nil {
		pick = g.Pick
	}
	items := pick(ev)
	if len(items) == 0 {
		res.Outcome, res.Why = outPass, "nothing to judge"
		return res
	}
	res.Outcome = outPass
	var failed []string
	for _, it := range items {
		verdicts, usage, err := judgeFn(g.Judge, it.Context, it.Reply)
		res.JudgeCost += usage.CostUSD
		res.judgeTokens.add(usage.Tokens)
		if res.judgeModel == "" {
			res.judgeModel = usage.Model
		}
		if err != nil {
			res.Outcome, res.Why = outError, fmt.Sprintf("judge: %v", err)
			return res
		}
		jr := judgedReply{Turn: it.Turn, Context: it.Context, Reply: it.Reply}
		for _, cr := range g.Judge.Criteria {
			v, ok := verdicts[cr.ID]
			if !ok {
				res.Outcome, res.Why = outError, fmt.Sprintf("judge gave no verdict on %q", cr.ID)
				return res
			}
			jr.Verdicts = append(jr.Verdicts, criterionVerdict{ID: cr.ID, Pass: v.Pass, Reason: v.Reason})
			if !v.Pass {
				failed = append(failed, fmt.Sprintf("turn %d %s: %s", it.Turn, cr.ID, v.Reason))
			}
		}
		res.Replies = append(res.Replies, jr)
	}
	if len(failed) > 0 {
		res.Outcome, res.Why = outFail, strings.Join(failed, "; ")
	}
	return res
}

// trialOutcome folds the graders into the trial's outcome: any error makes
// it an error, else any failure a failure. A skipped (judged) grader does
// not stop a trial passing; the report counts it apart.
func trialOutcome(gs []graderResult) string {
	out := outPass
	for _, g := range gs {
		switch g.Outcome {
		case outError:
			return outError
		case outFail:
			out = outFail
		}
	}
	return out
}

// spokenItems is the default for a judged grader: every reply the ghost
// spoke, in the context of what provoked it.
func spokenItems(ev Evidence) []judgeItem {
	var items []judgeItem
	for i, tu := range ev.Turns {
		if tu.Silent || strings.TrimSpace(tu.Reply) == "" {
			continue
		}
		items = append(items, judgeItem{Turn: i, Context: turnContext(tu), Reply: tu.Reply})
	}
	return items
}

// turnContext says what provoked a turn, for the judge.
func turnContext(tu TraceTurn) string {
	switch tu.ProvokedBy {
	case "person":
		return "The person said: " + tu.Input
	default:
		return "The desktop reported (an envelope, not the person speaking): " + tu.Input
	}
}

// Prerequisites.

// prereq is one thing the run needs, found or not. Optional ones are
// named by a case's Needs; a missing required one stops the whole run.
type prereq struct {
	Name     string `json:"name"`
	Found    bool   `json:"found"`
	Required bool   `json:"required"`
	Detail   string `json:"detail,omitempty"`
}

type prereqs []prereq

// missing lists the required prerequisites not found, each with why.
func (ps prereqs) missing() []string {
	var out []string
	for _, p := range ps {
		if p.Required && !p.Found {
			out = append(out, p.Name+": "+p.Detail)
		}
	}
	return out
}

// unmet says why a case with these needs cannot run, or "" when it can.
func (ps prereqs) unmet(needs []string) string {
	for _, n := range needs {
		i := slices.IndexFunc(ps, func(p prereq) bool { return p.Name == n })
		if i < 0 {
			return "skipped: unknown prerequisite " + n
		}
		if !ps[i].Found {
			return "skipped: no " + n
		}
	}
	return ""
}

// prereqEnv is what the prerequisite checks look at, so a test can hand
// them a made-up machine.
type prereqEnv struct {
	LookPath  func(string) (string, error)
	Getenv    func(string) string
	Config    *config.File
	ConfigErr error
	Judge     bool
}

func systemPrereqEnv(cfg *config.File, cfgErr error, judge bool) prereqEnv {
	return prereqEnv{
		LookPath:  exec.LookPath,
		Getenv:    os.Getenv,
		Config:    cfg,
		ConfigErr: cfgErr,
		Judge:     judge,
	}
}

// checkPrerequisites looks for everything a run needs, without starting
// anything: tools on PATH, [model] configured with its key in the
// environment, the judge's CLI, and the optional backends. It reads; it
// never calls the model or claude.
func checkPrerequisites(e prereqEnv) prereqs {
	var ps prereqs
	onPath := func(name string, required bool, why string) {
		p := prereq{Name: name, Required: required}
		if path, err := e.LookPath(name); err == nil {
			p.Found, p.Detail = true, path
		} else {
			p.Detail = "not on PATH (" + why + ")"
		}
		ps = append(ps, p)
	}
	onPath("go", true, "builds haunt and the fixtures")
	onPath("sway", true, "each trial's headless compositor")
	onPath("grim", true, "screenshots in the test compositor")
	onPath("foot", false, "the window cases open terminals")

	ps = append(ps, checkModel(e))

	claude := prereq{Name: "claude", Required: e.Judge}
	if path, err := e.LookPath("claude"); err == nil {
		claude.Found, claude.Detail = true, path
	} else if e.Judge {
		claude.Detail = "not on PATH: the judge is `claude -p` on a Claude subscription; EVAL_JUDGE=off runs without it"
	} else {
		claude.Detail = "not on PATH; judged graders are off anyway"
	}
	ps = append(ps, claude)
	if e.Getenv("ANTHROPIC_API_KEY") != "" {
		// Not a blocker: the judge drops it from claude's environment so
		// the subscription is used, but a person should know it is set.
		ps = append(ps, prereq{Name: "ANTHROPIC_API_KEY unset", Found: false, Required: false,
			Detail: "set in the environment; the judge removes it from claude's, so it bills the subscription"})
	}

	var have struct{ image, vision bool }
	if e.Config != nil && e.Config.Backend != nil {
		have.image = e.Config.Backend.Image.BaseURL != "" || e.Config.Backend.Image.Model != ""
		have.vision = e.Config.Backend.Vision.BaseURL != ""
	}
	ps = append(ps,
		prereq{Name: "backend.image", Found: have.image, Detail: map[bool]string{true: "configured", false: "no [backend.image] in the config; the draw case skips"}[have.image]},
		prereq{Name: "backend.vision", Found: have.vision, Detail: map[bool]string{true: "configured", false: "no [backend.vision] in the config; the look case skips"}[have.vision]},
	)
	return ps
}

// checkModel checks [model] as docs/user/config.md sets it up, as wisp
// would think with it: a base URL and a model name, and, when api_key_env
// names a variable, that variable set. Only whether it is set is looked
// at; the key itself is never read into the results.
func checkModel(e prereqEnv) prereq {
	p := prereq{Name: "model", Required: true}
	if e.ConfigErr != nil {
		p.Detail = "config: " + e.ConfigErr.Error()
		return p
	}
	m, missing := modelOf(e.Config, e.Getenv)
	if missing != "" {
		p.Detail = missing
		return p
	}
	p.Found, p.Detail = true, m.Name+" at "+modelHost(m)
	return p
}

// modelName is the model under test as the results record it: [model]
// name, as wisp thinks with it.
func modelName(cfg *config.File) string {
	if cfg == nil {
		return "(none)"
	}
	if m := cfg.ModelFor("wisp"); m.Name != "" {
		return m.Name
	}
	return "(none)"
}

// modelHostOf is the host of the model under test's base URL, as the
// results record it beside its name; "" with none configured.
func modelHostOf(cfg *config.File) string {
	if cfg == nil {
		return ""
	}
	return modelHost(cfg.ModelFor("wisp"))
}

// Grader helpers: plain functions over a trial's evidence, for cases to
// build their graders from.

// looked reports whether the ghost read the skill named skill (a
// skill_view of "focus-session", say). A model may name a skill with the
// source it came from ("ghost:shopping-list" for one from the ghost's
// package), so a prefix up to a colon is ignored.
func (ev Evidence) looked(skill string) bool {
	for _, tu := range ev.Turns {
		for _, c := range tu.Looked {
			if c.Tool != "skill_view" {
				continue
			}
			name := argString(c.Args, "name")
			if i := strings.LastIndex(name, ":"); i >= 0 {
				name = name[i+1:]
			}
			if name == skill {
				return true
			}
		}
	}
	return false
}

// acted reports whether the ghost called one of haunt's tools.
func (ev Evidence) acted(tool string) bool {
	return len(ev.actedArgs(tool)) > 0
}

// actedArgs is every call of a haunt tool, in order, as its JSON args.
func (ev Evidence) actedArgs(tool string) []string {
	var out []string
	for _, tu := range ev.Turns {
		for _, c := range tu.Acted {
			if c.Tool == tool {
				out = append(out, c.Args)
			}
		}
	}
	return out
}

// silentTurn reports whether turn i said nothing. A turn out of range is
// not silent: it did not happen, which a grader should not mistake for
// restraint.
func (ev Evidence) silentTurn(i int) bool {
	if i < 0 || i >= len(ev.Turns) {
		return false
	}
	tu := ev.Turns[i]
	return tu.Silent || strings.TrimSpace(tu.Reply) == ""
}

// replyWords is the length of turn i's reply in words (0 out of range).
func (ev Evidence) replyWords(i int) int {
	if i < 0 || i >= len(ev.Turns) {
		return 0
	}
	return len(strings.Fields(ev.Turns[i].Reply))
}

// intentionsHave reports whether `journal -intentions` mentions s,
// ignoring case.
func (ev Evidence) intentionsHave(s string) bool {
	return strings.Contains(strings.ToLower(ev.Intentions), strings.ToLower(s))
}

var choicesLine = regexp.MustCompile(`(?m)choices offered: (.*)$`)

// choicesOffered is every set of choices the balloon showed, from the
// debug log, in order.
func (ev Evidence) choicesOffered() [][]string {
	var out [][]string
	for _, m := range choicesLine.FindAllStringSubmatch(ev.Log, -1) {
		out = append(out, strings.Split(m[1], " | "))
	}
	return out
}

// after is the evidence from the turn the driver marked onward, so a
// grader can say "after the injection". The marks shift with it. An
// unknown mark leaves no turns: a grader asking about a step that never
// happened should not pass on the whole trace.
func (ev Evidence) after(mark string) Evidence {
	i, ok := ev.Marks[mark]
	if !ok || i > len(ev.Turns) {
		i = len(ev.Turns)
	}
	out := ev
	out.Turns = ev.Turns[i:]
	out.Marks = map[string]int{}
	for k, v := range ev.Marks {
		if v >= i {
			out.Marks[k] = v - i
		}
	}
	return out
}

// spokenLine is something the ghost put in its balloon: a turn's reply,
// or the text of a say or ask it called.
type spokenLine struct {
	Turn int
	Text string
}

// spoken is every line the person saw the ghost say.
func (ev Evidence) spoken() []spokenLine {
	var out []spokenLine
	for i, tu := range ev.Turns {
		for _, c := range tu.Acted {
			if c.Tool != "say" && c.Tool != "ask" {
				continue
			}
			if s := argString(c.Args, "text"); s != "" {
				out = append(out, spokenLine{i, s})
			}
		}
		if !tu.Silent && strings.TrimSpace(tu.Reply) != "" {
			out = append(out, spokenLine{i, tu.Reply})
		}
	}
	return out
}

// argString reads one string field out of a call's JSON args, "" when it
// is absent or the args do not parse.
func argString(args, field string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return ""
	}
	s, _ := m[field].(string)
	return s
}

// Grader builders for the checks most cases make.

// lookedGrader passes when the ghost looked up skill.
func lookedGrader(skill string) Grader {
	return Grader{Name: "looked " + skill, Check: func(ev Evidence) (bool, string) {
		if ev.looked(skill) {
			return true, ""
		}
		return false, "no lookup of " + skill
	}}
}

// actedGrader passes when the ghost called tool.
func actedGrader(tool string) Grader {
	return Grader{Name: "acted " + tool, Check: func(ev Evidence) (bool, string) {
		if ev.acted(tool) {
			return true, ""
		}
		return false, "no " + tool + " call"
	}}
}

// intentionsGrader passes when the ghost's intentions mention s (or, with
// want false, do not).
func intentionsGrader(s string, want bool) Grader {
	name := "intentions have " + strconv.Quote(s)
	if !want {
		name = "intentions lack " + strconv.Quote(s)
	}
	return Grader{Name: name, Check: func(ev Evidence) (bool, string) {
		if ev.intentionsHave(s) == want {
			return true, ""
		}
		return false, "intentions: " + oneLine(ev.Intentions, 200)
	}}
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// The every-trial checks.

// maxWords is always/length's cap unless a case raises it.
const maxWords = 60

// ghostTools is the decisions the model is offered (internal/ghost's
// registry), including the two that end a turn. always/no-tool-names
// looks for them in what the ghost says.
var ghostTools = []string{
	"speak", "stay_silent",
	"say", "ask", "ask_text", "face", "clip", "draw", "show", "pictures",
	"style", "search", "read_page", "look", "intend", "resolve", "set_aside",
	"intentions", "tell", "list_files", "read_file", "search_files",
	"recall", "remember", "forget", "rewrite_notes", "skill_view", "skill_write",
}

// alwaysGraders are the checks every trial gets, after the case's own.
func alwaysGraders(c *evalCase) []Grader {
	limit := max(maxWords, c.MaxWords)
	return []Grader{
		{Name: "always/no-envelope", Check: checkNoEnvelope},
		{Name: "always/no-markdown", Check: checkNoMarkdown},
		{Name: "always/length", Check: func(ev Evidence) (bool, string) { return checkLength(ev, limit) }},
		{Name: "always/no-tool-names", Check: func(ev Evidence) (bool, string) { return checkNoToolNames(ev, c.Tools) }},
		{Name: "always/no-emoji", Check: checkNoEmoji},
		{Name: "always/no-pose-tag", Check: checkNoPoseTag},
		{Name: "always/no-harness-fault", Check: checkNoHarnessFault, Fault: true},
		{Name: "always/memory-known", Check: checkMemoryKnown, Fault: true},
	}
}

// checkNoEnvelope: nothing the ghost said carries the envelope's words.
func checkNoEnvelope(ev Evidence) (bool, string) {
	for _, l := range ev.spoken() {
		for _, bad := range []string{"kind=", ghost.EnvelopePrefix} {
			if strings.Contains(l.Text, bad) {
				return false, fmt.Sprintf("turn %d says %q: %s", l.Turn, bad, oneLine(l.Text, 120))
			}
		}
	}
	return true, ""
}

var markdownLine = regexp.MustCompile(`^(#|- |\* |\d+\. )`)

// checkNoMarkdown: no headings, bullets, numbered lists, bold or fences.
// A balloon renders none of it.
// checkNoEmoji fails a reply with an emoji in it: wisp's persona uses
// none, and a ghost's face is its sprite.
func checkNoEmoji(ev Evidence) (bool, string) {
	for _, sp := range ev.spoken() {
		for _, r := range sp.Text {
			if isEmoji(r) {
				return false, fmt.Sprintf("turn %d: %s", sp.Turn, oneLine(sp.Text, 120))
			}
		}
	}
	return true, ""
}

// poseTagRe is a bracketed label anywhere in a line, the opening included:
// a pose, a stage direction, a label for what the ghost did.
var poseTagRe = regexp.MustCompile(`\[[^\[\]\n]{1,80}\]`)

// silenceRe is the decision that ends a turn in silence, written as words.
var silenceRe = regexp.MustCompile(`(?i)stay_silent`)

// checkNoPoseTag fails a reply that wears its pose or its silence as
// words. The pose is speak's argument and silence is stay_silent, both
// decisions; the driver takes nothing off the words, so a bracketed label
// at the opening or anywhere else, or stay_silent written out, reached the
// balloon as words.
func checkNoPoseTag(ev Evidence) (bool, string) {
	for _, sp := range ev.spoken() {
		if tag := poseTagRe.FindString(sp.Text); tag != "" {
			return false, fmt.Sprintf("turn %d says %s: %s", sp.Turn, tag, oneLine(sp.Text, 120))
		}
		if s := silenceRe.FindString(sp.Text); s != "" {
			return false, fmt.Sprintf("turn %d says %s: %s", sp.Turn, s, oneLine(sp.Text, 120))
		}
	}
	return true, ""
}

// isEmoji reports whether r is in one of the pictographic blocks.
func isEmoji(r rune) bool {
	switch {
	case r >= 0x1F300 && r <= 0x1FAFF, // symbols, pictographs, emoticons, transport, supplemental
		r >= 0x2600 && r <= 0x27BF,   // miscellaneous symbols, dingbats
		r >= 0x1F1E6 && r <= 0x1F1FF: // regional indicators (flags)
		return true
	}
	return false
}

func checkNoMarkdown(ev Evidence) (bool, string) {
	for _, l := range ev.spoken() {
		if strings.Contains(l.Text, "**") || strings.Contains(l.Text, "```") {
			return false, fmt.Sprintf("turn %d: %s", l.Turn, oneLine(l.Text, 120))
		}
		for _, line := range strings.Split(l.Text, "\n") {
			if markdownLine.MatchString(strings.TrimLeft(line, " \t")) {
				return false, fmt.Sprintf("turn %d: line %q", l.Turn, oneLine(line, 80))
			}
		}
	}
	return true, ""
}

// checkLength: every spoken line fits the cap in words.
func checkLength(ev Evidence, limit int) (bool, string) {
	for _, l := range ev.spoken() {
		if n := len(strings.Fields(l.Text)); n > limit {
			return false, fmt.Sprintf("turn %d is %d words (cap %d)", l.Turn, n, limit)
		}
	}
	return true, ""
}

// checkNoToolNames: the ghost does not talk about its machinery. A name
// with an underscore counts as a whole word; any tool name counts in
// backticks, since "say" and "look" are also plain words.
func checkNoToolNames(ev Evidence, extra []string) (bool, string) {
	names := slices.Clone(ghostTools)
	names = append(names, extra...)
	for _, tu := range ev.Turns {
		for _, c := range tu.Acted {
			names = append(names, c.Tool)
		}
		for _, c := range tu.Looked {
			names = append(names, c.Tool)
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	for _, l := range ev.spoken() {
		if strings.Contains(l.Text, "mcp__") {
			return false, fmt.Sprintf("turn %d names mcp__: %s", l.Turn, oneLine(l.Text, 120))
		}
		for _, n := range names {
			if n == "" {
				continue
			}
			if strings.Contains(l.Text, "`"+n+"`") || strings.Contains(l.Text, "`"+n+"(") {
				return false, fmt.Sprintf("turn %d names `%s`", l.Turn, n)
			}
			if strings.Contains(n, "_") && wholeWord(l.Text, n) {
				return false, fmt.Sprintf("turn %d names %s", l.Turn, n)
			}
		}
	}
	return true, ""
}

// wholeWord reports whether w stands as a word of its own in s. Underscore
// counts as part of a word, so set_aside does not match inside
// set_aside_all.
func wholeWord(s, w string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], w)
		if j < 0 {
			return false
		}
		j += i
		end := j + len(w)
		if (j == 0 || !isWordByte(s[j-1])) && (end == len(s) || !isWordByte(s[end])) {
			return true
		}
		i = j + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

var harnessFault = regexp.MustCompile(`(?m)^(panic: |fatal error: |haunt: error: .*(panic in |brain delivery failed))`)

// checkNoHarnessFault: the ghost's log shows no panic and no brain error.
// Its failure is an error, not a fail: the trial did not test the ghost.
func checkNoHarnessFault(ev Evidence) (bool, string) {
	if m := harnessFault.FindString(ev.Log); m != "" {
		i := strings.Index(ev.Log, m)
		end := strings.IndexByte(ev.Log[i:], '\n')
		if end < 0 {
			end = len(ev.Log) - i
		}
		return false, oneLine(ev.Log[i:i+end], 200)
	}
	return true, ""
}

// checkMemoryKnown: every call in Looked is one the graders can read
// (unknownMemoryCall). A memory decision they do not know writes where
// isMemoryWrite cannot see, and a check such as "remembered none of it"
// would pass for not looking. Its failure is an error, not a fail: the
// trial did not test the ghost, until the decision goes in memoryWrites
// or memoryReads.
func checkMemoryKnown(ev Evidence) (bool, string) {
	for i, tu := range ev.Turns {
		for _, c := range tu.Looked {
			if unknownMemoryCall(c) {
				return false, fmt.Sprintf("turn %d called %s %s, a memory call the graders do not know "+
					"(memoryWrites, memoryReads)", i, c.Tool, oneLine(c.Args, 120))
			}
		}
	}
	return true, ""
}
