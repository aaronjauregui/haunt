//go:build eval

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/backend"
	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/hermes"
)

// These test the eval harness itself over canned evidence and stub Drives:
// no compositor, no hermes, no judge. Run them with
//
//	go test -tags eval -run 'TestEvalHarness' ./cmd/haunt

func spokeTurn(reply string) TraceTurn {
	return TraceTurn{ProvokedBy: "person", Input: "hi", Reply: reply}
}

func TestEvalHarnessAlwaysChecks(t *testing.T) {
	c := &evalCase{Name: "x", Tools: []string{"append"}}
	byName := map[string]Grader{}
	for _, g := range alwaysGraders(c) {
		byName[g.Name] = g
	}
	long := strings.Repeat("word ", 61)
	for _, tc := range []struct {
		grader string
		turns  []TraceTurn
		log    string
		pass   bool
	}{
		{"always/no-envelope", []TraceTurn{spokeTurn("the kettle is on")}, "", true},
		{"always/no-envelope", []TraceTurn{spokeTurn("got kind=idle, so hello")}, "", false},
		{"always/no-envelope", []TraceTurn{spokeTurn(hermes.EnvelopePrefix + " idle")}, "", false},
		{"always/no-envelope", []TraceTurn{spokeTurn("NO_REPLY")}, "", false},
		// A silent turn's text is never shown, whatever it holds.
		{"always/no-envelope", []TraceTurn{{Reply: "NO_REPLY", Silent: true}}, "", true},
		// A say's text is shown too.
		{"always/no-envelope", []TraceTurn{{Silent: true, Acted: []TraceCall{{Tool: "say", Args: `{"text":"kind=idle"}`}}}}, "", false},

		{"always/no-markdown", []TraceTurn{spokeTurn("Three things - maybe four.")}, "", true},
		{"always/no-markdown", []TraceTurn{spokeTurn("# Plan")}, "", false},
		{"always/no-markdown", []TraceTurn{spokeTurn("ok:\n- milk\n- eggs")}, "", false},
		{"always/no-markdown", []TraceTurn{spokeTurn("ok:\n  * milk")}, "", false},
		{"always/no-markdown", []TraceTurn{spokeTurn("steps:\n1. breathe")}, "", false},
		{"always/no-markdown", []TraceTurn{spokeTurn("that is **bold**")}, "", false},
		{"always/no-markdown", []TraceTurn{spokeTurn("```\ncode\n```")}, "", false},

		{"always/no-emoji", []TraceTurn{spokeTurn("Boo — the kettle's haunted.")}, "", true},
		{"always/no-emoji", []TraceTurn{spokeTurn("Boo! 👻")}, "", false},
		{"always/no-emoji", []TraceTurn{spokeTurn("Sunny ☀ out there")}, "", false},

		{"always/length", []TraceTurn{spokeTurn(strings.Repeat("word ", 60))}, "", true},
		{"always/length", []TraceTurn{spokeTurn(long)}, "", false},
		{"always/length", []TraceTurn{{Reply: long, Silent: true}}, "", true},

		{"always/no-tool-names", []TraceTurn{spokeTurn("I'll look into it and say so.")}, "", true},
		{"always/no-tool-names", []TraceTurn{spokeTurn("I can `look` at your window.")}, "", false},
		{"always/no-tool-names", []TraceTurn{spokeTurn("calling mcp__haunt__intend now")}, "", false},
		{"always/no-tool-names", []TraceTurn{spokeTurn("let me skill_view that")}, "", false},
		{"always/no-tool-names", []TraceTurn{spokeTurn("I'll set_aside the thesis.")}, "", false},
		{"always/no-tool-names", []TraceTurn{spokeTurn("I used `append` on the list")}, "", false},
		{"always/no-tool-names", []TraceTurn{spokeTurn("I'll append it to the list")}, "", true},
		{"always/no-tool-names", []TraceTurn{spokeTurn("set_aside_all is not a word")}, "", true},
		// A tool seen in the trace counts, even one no list names.
		{"always/no-tool-names", []TraceTurn{
			{Looked: []TraceCall{{Tool: "web_fetch"}}},
			spokeTurn("I did a web_fetch"),
		}, "", false},

		{"always/no-harness-fault", nil, "haunt: hermes is up for wisp at http://127.0.0.1:1\n", true},
		{"always/no-harness-fault", nil, "haunt: error: wisp: hermes: gateway closed\n", false},
		{"always/no-harness-fault", nil, "panic: runtime error: index out of range\n", false},
		{"always/no-harness-fault", nil, "haunt: error: wisp: panic in balloon: boom\n", false},
		{"always/no-harness-fault", nil, "haunt: error: draw: no image backend\n", true},
	} {
		g, ok := byName[tc.grader]
		if !ok {
			t.Fatalf("no grader %s", tc.grader)
		}
		pass, why := g.Check(Evidence{Turns: tc.turns, Log: tc.log})
		if pass != tc.pass {
			t.Errorf("%s over %+v %q: pass=%v (%s), want %v", tc.grader, tc.turns, tc.log, pass, why, tc.pass)
		}
	}
}

// A case may raise always/length's cap but not lower it below 60.
func TestEvalHarnessLengthCap(t *testing.T) {
	ev := Evidence{Turns: []TraceTurn{spokeTurn(strings.Repeat("word ", 100))}}
	check := func(c *evalCase) bool {
		for _, g := range alwaysGraders(c) {
			if g.Name == "always/length" {
				pass, _ := g.Check(ev)
				return pass
			}
		}
		t.Fatal("no always/length")
		return false
	}
	if check(&evalCase{}) {
		t.Error("100 words passed the default cap")
	}
	if !check(&evalCase{MaxWords: 120}) {
		t.Error("100 words failed a cap of 120")
	}
	short := Evidence{Turns: []TraceTurn{spokeTurn(strings.Repeat("word ", 40))}}
	for _, g := range alwaysGraders(&evalCase{MaxWords: 10}) {
		if g.Name == "always/length" {
			if pass, _ := g.Check(short); !pass {
				t.Error("a case's lower MaxWords lowered the cap")
			}
		}
	}
}

func TestEvalHarnessHelpers(t *testing.T) {
	ev := Evidence{
		Turns: []TraceTurn{
			{ProvokedBy: "event", Input: "[haunt event] kind=boot", Silent: true},
			{ProvokedBy: "person", Input: "focus on the slides", Reply: "Two minutes, go.",
				Looked: []TraceCall{{Tool: "skill_view", Args: `{"name":"focus-session"}`},
					{Tool: "skill_view", Args: `{"name":"ghost:shopping-list"}`}},
				Acted: []TraceCall{{Tool: "intend", Args: `{"text":"check the slides","delay_ms":120000}`, Result: "focus-1"}}},
			{ProvokedBy: "event", Input: "[haunt event] kind=errand", Reply: "How did it go?",
				Acted: []TraceCall{{Tool: "ask", Args: `{"text":"How did it go?","choices":["done","progress"]}`, Result: "done"},
					{Tool: "resolve", Args: `{"name":"focus-1"}`}}},
		},
		Intentions: "Goals:\n  finish the Quarterly Report\n",
		Log:        "haunt: [balloon] wisp: choices offered: done | progress\nhaunt: [input] input line opened\nhaunt: [balloon] wisp: choices offered: yes | no | later\n",
		Marks:      map[string]int{"asked": 1, "due": 2},
	}
	if !ev.looked("focus-session") || !ev.looked("shopping-list") || ev.looked("weekly-review") || ev.looked("focus") {
		t.Error("looked")
	}
	if !ev.acted("intend") || !ev.acted("resolve") || ev.acted("set_aside") {
		t.Error("acted")
	}
	if got := ev.actedArgs("intend"); len(got) != 1 || !strings.Contains(got[0], "120000") {
		t.Errorf("actedArgs(intend) = %q", got)
	}
	if !ev.silentTurn(0) || ev.silentTurn(1) || ev.silentTurn(9) || ev.silentTurn(-1) {
		t.Error("silentTurn")
	}
	if ev.replyWords(1) != 3 || ev.replyWords(0) != 0 || ev.replyWords(7) != 0 {
		t.Errorf("replyWords: %d %d", ev.replyWords(1), ev.replyWords(0))
	}
	if !ev.intentionsHave("quarterly report") || ev.intentionsHave("thesis") {
		t.Error("intentionsHave")
	}
	if got := ev.choicesOffered(); len(got) != 2 || !slices.Equal(got[1], []string{"yes", "no", "later"}) {
		t.Errorf("choicesOffered = %q", got)
	}
	after := ev.after("due")
	if len(after.Turns) != 1 || after.acted("intend") || !after.acted("resolve") || after.Marks["due"] != 0 {
		t.Errorf("after(due) = %+v", after)
	}
	if _, ok := after.Marks["asked"]; ok {
		t.Error("after kept a mark from before it")
	}
	if len(ev.after("never").Turns) != 0 {
		t.Error("after an unknown mark kept turns")
	}
	// A spoken ask's text is spoken.
	if sp := ev.spoken(); len(sp) != 3 || sp[1].Text != "How did it go?" {
		t.Errorf("spoken = %+v", sp)
	}

	for _, g := range []Grader{lookedGrader("focus-session"), actedGrader("resolve"),
		intentionsGrader("quarterly report", true), intentionsGrader("thesis", false)} {
		if pass, why := g.Check(ev); !pass {
			t.Errorf("%s failed: %s", g.Name, why)
		}
	}
	for _, g := range []Grader{lookedGrader("humanizer"), actedGrader("draw"), intentionsGrader("thesis", true)} {
		if pass, _ := g.Check(ev); pass {
			t.Errorf("%s passed", g.Name)
		}
	}
}

func TestEvalHarnessOptions(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	o, err := evalOptionsFromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if o.Trials != 3 || o.Cases != nil || !o.Judge || o.Strict || !strings.HasSuffix(o.Out, ".eval") {
		t.Errorf("defaults: %+v", o)
	}
	o, err = evalOptionsFromEnv(env(map[string]string{
		"EVAL_TRIALS": "5", "EVAL_CASES": "focus|reminder", "EVAL_OUT": "/x",
		"EVAL_JUDGE": "off", "EVAL_STRICT": "1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if o.Trials != 5 || !o.Cases.MatchString("reminder") || o.Cases.MatchString("boot") || o.Judge || !o.Strict || o.Out != "/x" {
		t.Errorf("set: %+v", o)
	}
	root, _ := repoRoot()
	if o, _ := evalOptionsFromEnv(env(map[string]string{"EVAL_OUT": "runs"})); o.Out != filepath.Join(root, "runs") {
		t.Errorf("relative EVAL_OUT is %s, want it under %s", o.Out, root)
	}
	for _, bad := range []map[string]string{{"EVAL_TRIALS": "0"}, {"EVAL_TRIALS": "x"}, {"EVAL_CASES": "("}} {
		if _, err := evalOptionsFromEnv(env(bad)); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
}

// fakeMachine is a prereqEnv with the named tools on PATH and the given
// environment.
func fakeMachine(tools []string, env map[string]string, cfg *config.File) prereqEnv {
	return prereqEnv{
		LookPath: func(name string) (string, error) {
			if slices.Contains(tools, name) || filepath.IsAbs(name) && slices.Contains(tools, filepath.Base(name)) {
				return "/usr/bin/" + filepath.Base(name), nil
			}
			return "", errors.New("not found")
		},
		Getenv: func(k string) string { return env[k] },
		Stat: func(p string) (os.FileInfo, error) {
			if p == "/home/p/.config/haunt/hermes.env" {
				return nil, nil
			}
			return nil, os.ErrNotExist
		},
		HasImage: func(image string) bool { return image == hermesImage },
		Config:   cfg,
		Judge:    true,
	}
}

func TestEvalHarnessPrerequisites(t *testing.T) {
	cfg := &config.File{}
	cfg.Hermes.Command = []string{"/src/haunt/scripts/hermes-docker"}
	cfg.Hermes.Model = &hermes.ModelConfig{Provider: "openrouter", Default: "some/model"}
	all := []string{"go", "sway", "grim", "docker", "foot", "claude", "hermes-docker"}
	env := map[string]string{"HOME": "/home/p"}

	ps := checkPrerequisites(fakeMachine(all, env, cfg))
	if m := ps.missing(); len(m) > 0 {
		t.Fatalf("missing on a complete machine: %v", m)
	}
	if why := ps.unmet([]string{"foot"}); why != "" {
		t.Errorf("foot unmet: %s", why)
	}
	if why := ps.unmet([]string{"backend.image"}); why != "skipped: no backend.image" {
		t.Errorf("backend.image: %q", why)
	}
	if modelName(cfg) != "openrouter/some/model" {
		t.Errorf("model %q", modelName(cfg))
	}

	cfg.Backend = &backend.Config{}
	cfg.Backend.Image.BaseURL = "http://localhost:8188"
	cfg.Backend.Vision.BaseURL = "http://localhost:11434/v1"
	ps = checkPrerequisites(fakeMachine(slices.DeleteFunc(slices.Clone(all), func(s string) bool { return s == "foot" }), env, cfg))
	if ps.unmet([]string{"backend.image", "backend.vision"}) != "" || ps.unmet([]string{"foot"}) != "skipped: no foot" {
		t.Errorf("optional: %+v", ps)
	}
	if len(ps.missing()) > 0 {
		t.Errorf("foot is optional, missing: %v", ps.missing())
	}

	// Each required thing, taken away, is named.
	for _, drop := range []string{"go", "sway", "grim", "docker", "claude", "hermes-docker"} {
		tools := slices.DeleteFunc(slices.Clone(all), func(s string) bool { return s == drop })
		m := checkPrerequisites(fakeMachine(tools, env, cfg)).missing()
		if len(m) != 1 {
			t.Errorf("without %s: missing %v", drop, m)
		}
	}
	// The judge is not needed with it off.
	e := fakeMachine([]string{"go", "sway", "grim", "docker", "hermes-docker"}, env, cfg)
	e.Judge = false
	if m := checkPrerequisites(e).missing(); len(m) > 0 {
		t.Errorf("judge off still wants claude: %v", m)
	}
	// Keys file and image, for hermes-docker.
	if m := checkPrerequisites(fakeMachine(all, map[string]string{"HOME": "/elsewhere"}, cfg)).missing(); len(m) != 1 || !strings.HasPrefix(m[0], "hermes.env") {
		t.Errorf("no keys file: %v", m)
	}
	if m := checkPrerequisites(fakeMachine(all, map[string]string{"HOME": "/home/p", "HERMES_IMAGE": "other:1"}, cfg)).missing(); len(m) != 1 || !strings.HasPrefix(m[0], "hermes image") {
		t.Errorf("no image: %v", m)
	}
	// An API key in the environment is noted, not a blocker.
	ps = checkPrerequisites(fakeMachine(all, map[string]string{"HOME": "/home/p", "ANTHROPIC_API_KEY": "k"}, cfg))
	if len(ps.missing()) > 0 || !slices.ContainsFunc(ps, func(p prereq) bool { return p.Name == "ANTHROPIC_API_KEY unset" }) {
		t.Errorf("api key: %+v", ps)
	}
	// Hermes not configured.
	for _, c := range []*config.File{nil, {}, {Hermes: hermes.Config{Command: []string{"hermes-docker"}}}} {
		m := checkPrerequisites(fakeMachine(all, env, c)).missing()
		if len(m) != 1 || !strings.HasPrefix(m[0], "hermes:") {
			t.Errorf("config %+v: missing %v", c, m)
		}
	}
	e = fakeMachine(all, env, nil)
	e.ConfigErr = errors.New("bad toml")
	if m := checkPrerequisites(e).missing(); len(m) != 1 || !strings.Contains(m[0], "bad toml") {
		t.Errorf("config error: %v", m)
	}
}

// stubJudge answers every criterion, failing those whose ID is in fail.
func stubJudge(fail ...string) func(r *Rubric, context, reply string) (map[string]Verdict, error) {
	return func(r *Rubric, context, reply string) (map[string]Verdict, error) {
		out := map[string]Verdict{}
		for _, c := range r.Criteria {
			out[c.ID] = Verdict{Pass: !slices.Contains(fail, c.ID), Reason: "because " + c.ID}
		}
		return out, nil
	}
}

var toneRubric = &Rubric{Name: "tone", Criteria: []Criterion{
	{ID: "in-character", Question: "Does the reply sound like Wisp?"},
	{ID: "no-boilerplate", Question: "Is the reply free of assistant boilerplate?"},
}}

// harnessCases are stub cases covering every outcome, over canned
// evidence.
func harnessCases() []evalCase {
	good := Evidence{
		Turns: []TraceTurn{
			{ProvokedBy: "event", Input: "[haunt event] kind=boot", Silent: true},
			{ProvokedBy: "person", Input: "remind me to stretch", Reply: "In a minute, then.",
				Acted: []TraceCall{{Tool: "intend", Args: `{"text":"stretch"}`}}},
		},
		Intentions: "stretch",
	}
	canned := func(ev Evidence) func(*evalTrial) (Evidence, error) {
		return func(*evalTrial) (Evidence, error) { return ev, nil }
	}
	return []evalCase{
		{Name: "good", Drive: canned(good), Graders: []Grader{actedGrader("intend"), intentionsGrader("stretch", true)}},
		{Name: "bad", Drive: canned(good), Graders: []Grader{actedGrader("draw")}},
		{Name: "leaky", Drive: canned(Evidence{Turns: []TraceTurn{spokeTurn("# kind=idle")}})},
		{Name: "broken", Drive: func(*evalTrial) (Evidence, error) { return Evidence{}, errors.New("hermes never came up") }},
		{Name: "panicky", Drive: func(*evalTrial) (Evidence, error) { panic("boom") }},
		{Name: "skipper", Drive: func(*evalTrial) (Evidence, error) { return Evidence{}, skipTrial("no %s", "thing") }},
		{Name: "tskip", Drive: func(tr *evalTrial) (Evidence, error) { tr.Skip("sway unavailable"); return Evidence{}, nil }},
		{Name: "needy", Needs: []string{"backend.image"}, Drive: canned(good)},
		{Name: "faulty", Drive: canned(good), Graders: []Grader{{Name: "event arrived", Fault: true,
			Check: func(Evidence) (bool, string) { return false, "no kind=session turn" }}}},
		{Name: "crashlog", Drive: canned(Evidence{Log: "panic: nil map\n"})},
		{Name: "judged", Drive: canned(good), Graders: []Grader{{Name: "judge/tone", Judge: toneRubric}}},
		{Name: "judged-picked", Drive: canned(good),
			Graders: []Grader{{Name: "judge/tone", Judge: toneRubric,
				Check: func(ev Evidence) (bool, string) { return ev.acted("intend"), "" },
				Pick: func(Evidence) []judgeItem {
					return []judgeItem{{Turn: 1, Context: "c", Reply: "one"}, {Turn: 1, Context: "c", Reply: "two"}}
				}}}},
		{Name: "judged-silent", Drive: canned(Evidence{Turns: []TraceTurn{{Silent: true}}}),
			Graders: []Grader{{Name: "judge/tone", Judge: toneRubric}}},
	}
}

func harnessPrereqs() prereqs {
	return prereqs{{Name: "backend.image", Found: false}, {Name: "foot", Found: true}}
}

func outcomes(run *evalRun) map[string][]string {
	out := map[string][]string{}
	for _, c := range run.Cases {
		for _, t := range c.Trials {
			out[c.Name] = append(out[c.Name], t.Outcome)
		}
	}
	return out
}

func TestEvalHarnessRun(t *testing.T) {
	defer func(f func(*Rubric, string, string) (map[string]Verdict, error)) { judgeFn = f }(judgeFn)
	var mu sync.Mutex
	var calls []string
	judge := stubJudge("no-boilerplate")
	judgeFn = func(r *Rubric, context, reply string) (map[string]Verdict, error) {
		mu.Lock()
		calls = append(calls, reply)
		mu.Unlock()
		return judge(r, context, reply)
	}
	opts := evalOptions{Trials: 2, Judge: true}
	run := runEval(t, harnessCases(), opts, harnessPrereqs(), nil)
	run.finish(time.Now())

	want := map[string]string{
		"good": outPass, "bad": outFail, "leaky": outFail, "broken": outError,
		"panicky": outError, "skipper": outSkip, "tskip": outSkip, "needy": outSkip,
		"faulty": outError, "crashlog": outError, "judged": outFail,
		"judged-picked": outFail, "judged-silent": outPass,
	}
	got := outcomes(run)
	for name, o := range want {
		if len(got[name]) != 2 || got[name][0] != o || got[name][1] != o {
			t.Errorf("%s: %v, want %s ×2", name, got[name], o)
		}
	}
	for _, c := range run.Cases {
		switch c.Name {
		case "needy":
			if c.Skipped != "skipped: no backend.image" {
				t.Errorf("needy skipped %q", c.Skipped)
			}
		case "good":
			if c.Rate == nil || *c.Rate != 1 || c.Pass != 2 {
				t.Errorf("good tally %+v", c)
			}
			if len(c.Graders) != 2+6 {
				t.Errorf("good graders %+v", c.Graders)
			}
		case "leaky":
			fails := map[string]bool{}
			for _, g := range c.Trials[0].Graders {
				if g.Outcome == outFail {
					fails[g.Name] = true
				}
			}
			if !fails["always/no-envelope"] || !fails["always/no-markdown"] {
				t.Errorf("leaky failed %v", fails)
			}
		case "judged-picked":
			g := c.Trials[0].Graders[0]
			if len(g.Replies) != 2 || !strings.Contains(g.Why, "no-boilerplate") || len(g.Replies[0].Verdicts) != 2 {
				t.Errorf("judged-picked %+v", g)
			}
		case "broken":
			if c.Rate != nil || c.Error != 2 {
				t.Errorf("broken tally %+v", c)
			}
		}
	}
	// judged: one spoken reply per trial; judged-picked: two per trial.
	if len(calls) != 2*1+2*2 {
		t.Errorf("judge calls %d: %q", len(calls), calls)
	}

	// With the judge off, judged graders skip and the trial still passes
	// on its code graders.
	calls = nil
	opts.Judge = false
	run = runEval(t, harnessCases()[10:11], opts, harnessPrereqs(), nil)
	run.finish(time.Now())
	tr := run.Cases[0].Trials[0]
	if tr.Outcome != outPass || tr.Graders[0].Outcome != outSkip || len(calls) != 0 {
		t.Errorf("judge off: %+v, %d calls", tr, len(calls))
	}
	judgeFn = nil
	opts.Judge = true
	run = runEval(t, harnessCases()[10:11], opts, harnessPrereqs(), nil)
	if g := run.Cases[0].Trials[0].Graders[0]; g.Outcome != outSkip {
		t.Errorf("no judge: %+v", g)
	}
	// A judge that errs, or skips a criterion, is a harness fault.
	judgeFn = func(*Rubric, string, string) (map[string]Verdict, error) {
		return nil, errors.New("claude: not logged in")
	}
	run = runEval(t, harnessCases()[10:11], opts, harnessPrereqs(), nil)
	if tr := run.Cases[0].Trials[0]; tr.Outcome != outError {
		t.Errorf("judge error: %+v", tr)
	}
	judgeFn = func(*Rubric, string, string) (map[string]Verdict, error) {
		return map[string]Verdict{"in-character": {Pass: true}}, nil
	}
	run = runEval(t, harnessCases()[10:11], opts, harnessPrereqs(), nil)
	if tr := run.Cases[0].Trials[0]; tr.Outcome != outError || !strings.Contains(tr.Graders[0].Why, "no-boilerplate") {
		t.Errorf("judge missing a criterion: %+v", tr)
	}
}

func TestEvalHarnessReport(t *testing.T) {
	defer func(f func(*Rubric, string, string) (map[string]Verdict, error)) { judgeFn = f }(judgeFn)
	judgeFn = stubJudge("no-boilerplate")
	out := t.TempDir()
	cfg := &config.File{}
	cfg.Hermes.Command = []string{"hermes-docker"}
	cfg.Hermes.Model = &hermes.ModelConfig{Default: "m1"}
	opts := evalOptions{Trials: 3, Judge: true, Out: out, Cases: regexp.MustCompile(".")}

	// A first run, kept as the baseline, where "flaky" passes every trial.
	var n atomic.Int32
	flaky := evalCase{Name: "flaky", Drive: func(*evalTrial) (Evidence, error) {
		return Evidence{Turns: []TraceTurn{spokeTurn("fine")}}, nil
	}, Graders: []Grader{{Name: "passes early", Check: func(Evidence) (bool, string) {
		return n.Add(1) <= 3, "ran out of luck"
	}}}}
	cases := append(harnessCases(), flaky)
	run := runEval(t, cases, opts, harnessPrereqs(), cfg)
	run.Started = time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)
	run.finish(run.Started.Add(time.Minute))
	dir, err := run.write(out)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(dir) != "20260918-100000" {
		t.Errorf("run dir %s", dir)
	}
	report, _ := os.ReadFile(filepath.Join(dir, "report.md"))
	for _, want := range []string{
		"Model m1", "## Not run", "- backend.image", "case `needy` skipped: no backend.image",
		"| good | 3 | 0 | 0 | 0 | 100% |", "no baseline",
		"## Failing trials", "### bad #1: fail", "**acted draw** fail: no draw call",
		"### broken #1: error", "hermes never came up",
		"turn 1, no-boilerplate: because no-boilerplate", "<details><summary>trace",
		"remind me to stretch", "acted intend",
		"### good", "| always/no-envelope | 3 | 0 | 0 | 0 |",
	} {
		if !strings.Contains(string(report), want) {
			t.Errorf("report lacks %q", want)
		}
	}
	if strings.Index(string(report), "## Not run") > strings.Index(string(report), "## Cases") {
		t.Error("skips are not at the top")
	}
	if _, err := os.Stat(filepath.Join(dir, "trials", "good", "1", "trace.json")); err != nil {
		t.Error(err)
	}
	var back evalRun
	b, _ := os.ReadFile(filepath.Join(dir, "results.json"))
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Model != "m1" || back.Trials != 3 || len(back.Cases) != len(cases) || back.GitRev == "" || len(back.Prerequisites) != 2 {
		t.Errorf("results.json: %+v", back)
	}
	for _, c := range back.Cases {
		if c.Name == "judged" && (len(c.Trials[0].Graders[0].Replies) != 1 || c.Trials[0].Graders[0].Replies[0].Verdicts[1].Reason == "") {
			t.Errorf("verdicts not kept: %+v", c.Trials[0].Graders[0])
		}
	}

	// Kept as the baseline, the way make eval-baseline does.
	if err := os.WriteFile(filepath.Join(out, "baseline.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	// A second run in which "flaky" fails every trial: three below the
	// baseline, which EVAL_STRICT fails on, as it does on the skipped cases.
	run2 := runEval(t, cases, opts, harnessPrereqs(), cfg)
	run2.Started = run.Started
	run2.finish(run.Started.Add(time.Minute))
	dir2, err := run2.write(out)
	if err != nil {
		t.Fatal(err)
	}
	if dir2 == dir {
		t.Error("second run overwrote the first")
	}
	report2, _ := os.ReadFile(filepath.Join(dir2, "report.md"))
	if !strings.Contains(string(report2), "| flaky | 0 | 3 | 0 | 0 | 0% | 100% | -100 |") ||
		!strings.Contains(string(report2), "| good | 3 | 0 | 0 | 0 | 100% | 100% | +0 |") {
		t.Errorf("deltas missing:\n%s", report2)
	}
	strict := strings.Join(run2.strictFailures(), "\n")
	for _, want := range []string{"flaky: 0% against a baseline of 100%", "needy: skipped: no backend.image", "skipper: 3 of 3 trials skipped"} {
		if !strings.Contains(strict, want) {
			t.Errorf("strict lacks %q:\n%s", want, strict)
		}
	}
	if strings.Contains(strict, "good") || strings.Contains(strict, "bad:") {
		t.Errorf("strict flags a steady case:\n%s", strict)
	}

	// One trial below the baseline is within noise.
	run3 := &evalRun{Model: "m1", Cases: []caseResult{{Name: "flaky", Trials: []trialResult{
		{Outcome: outPass}, {Outcome: outPass}, {Outcome: outFail}}}}}
	run3.finish(time.Now())
	run3.loadBaseline(filepath.Join(out, "baseline.json"))
	if f := run3.strictFailures(); len(f) != 0 {
		t.Errorf("one below: %v", f)
	}

	// Another model's baseline gives no deltas.
	cfg.Hermes.Model.Default = "m2"
	run4 := runEval(t, cases[:1], opts, harnessPrereqs(), cfg)
	run4.finish(time.Now())
	dir4, err := run4.write(out)
	if err != nil {
		t.Fatal(err)
	}
	report4, _ := os.ReadFile(filepath.Join(dir4, "report.md"))
	if !strings.Contains(string(report4), "the baseline is for m1, not m2") {
		t.Errorf("model mismatch not said:\n%s", report4)
	}
	if len(run4.strictFailures()) != 0 {
		t.Errorf("strict against another model's baseline: %v", run4.strictFailures())
	}
}
