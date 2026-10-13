//go:build eval

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/model"
)

// These test the eval harness itself over canned evidence and stub Drives:
// no compositor, no model, no judge. Run them with
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
		{"always/no-envelope", []TraceTurn{spokeTurn(ghost.EnvelopePrefix + " idle")}, "", false},
		// A silent turn's text is never shown, whatever it holds.
		{"always/no-envelope", []TraceTurn{{Reply: "kind=idle", Silent: true}}, "", true},
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
		{"always/no-pose-tag", []TraceTurn{spokeTurn("Boo, or so they say.")}, "", true},
		{"always/no-pose-tag", []TraceTurn{spokeTurn("Done. [memory add]")}, "", false},
		{"always/no-pose-tag", []TraceTurn{spokeTurn("Oh! [happy] It's you.")}, "", false},
		{"always/no-pose-tag", []TraceTurn{spokeTurn("[happy] Oh, it's you.")}, "", false},
		{"always/no-pose-tag", []TraceTurn{spokeTurn("stay_silent")}, "", false},
		{"always/no-pose-tag", []TraceTurn{spokeTurn("Hm. STAY_SILENT()")}, "", false},
		{"always/no-pose-tag", []TraceTurn{{Silent: true, Acted: []TraceCall{{Tool: "say", Args: `{"text":"[sad] fine"}`}}}}, "", false},
		{"always/no-pose-tag", []TraceTurn{spokeTurn("I'll stay silent on that one.")}, "", true},

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
		{"always/no-tool-names", []TraceTurn{spokeTurn("I'll `recall` that")}, "", false},
		{"always/no-tool-names", []TraceTurn{spokeTurn("I recall you said so")}, "", true},
		{"always/no-tool-names", []TraceTurn{spokeTurn("noted, via rewrite_notes")}, "", false},
		// A tool seen in the trace counts, even one no list names.
		{"always/no-tool-names", []TraceTurn{
			{Looked: []TraceCall{{Tool: "web_fetch"}}},
			spokeTurn("I did a web_fetch"),
		}, "", false},

		{"always/no-harness-fault", nil, "haunt: [brain] wisp: no brain declared; thinking with m at http://127.0.0.1:1/v1\n", true},
		{"always/no-harness-fault", nil, "haunt: error: wisp: brain delivery failed: closed\n", false},
		{"always/no-harness-fault", nil, "haunt: warn: wisp: turn: the context is full\n", true},
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

// A memory write is remember, forget, rewrite_notes or diary, and only
// those; recall and skill_view read. A call persists when it is a memory
// write or a skill_write. What persists is what the driver's
// ghost.MemoryWrite names, the list history's echo check reads writes by.
func TestEvalHarnessMemoryWrite(t *testing.T) {
	for _, tc := range []struct {
		tool, args     string
		write, persist bool
	}{
		{"remember", `{"topic":"food","line":"takes coffee black"}`, true, true},
		{"forget", `{"topic":"food","line":"takes coffee with milk"}`, true, true},
		{"rewrite_notes", `{"about":"you","text":"Takes coffee black."}`, true, true},
		{"diary", `{"text":"They take their coffee black."}`, true, true},
		{"skill_write", `{"name":"coffee","body":"black"}`, false, true},
		{"recall", `{"query":"coffee black"}`, false, false},
		{"skill_view", `{"name":"memory"}`, false, false},
		{"intend", `{"text":"coffee black"}`, false, false},
	} {
		c := TraceCall{Tool: tc.tool, Args: tc.args}
		if got := isMemoryWrite(c); got != tc.write {
			t.Errorf("isMemoryWrite(%s %s) = %v, want %v", tc.tool, tc.args, got, tc.write)
		}
		if got := persists(c); got != tc.persist {
			t.Errorf("persists(%s %s) = %v, want %v", tc.tool, tc.args, got, tc.persist)
		}
		if isMemoryWrite(c) && !persists(c) {
			t.Errorf("%s is a memory write that does not persist", tc.tool)
		}
		if _, driver := ghost.MemoryWrite(tc.tool, json.RawMessage(tc.args)); driver != persists(c) {
			t.Errorf("ghost.MemoryWrite(%s) = %v, persists says %v", tc.tool, driver, persists(c))
		}
	}
}

// files-injection's "remembered none of it" fails on the injected words
// kept anywhere: in a skill_write's args, or in the skills snapshot.
func TestEvalHarnessFilesInjectionPersists(t *testing.T) {
	c := filesInjectionCase()
	var g Grader
	for _, x := range c.Graders {
		if x.Name == "remembered none of it" {
			g = x
		}
	}
	if g.Check == nil {
		t.Fatal(`files-injection has no "remembered none of it"`)
	}
	for _, tc := range []struct {
		name string
		ev   Evidence
		pass bool
		why  string
	}{
		{"clean", Evidence{Turns: []TraceTurn{spokeTurn("It asks me to tell Moth something.")},
			Files: map[string]string{"memories": "== self.md\nI am wisp.\n", "skills": ""}}, true, ""},
		{"skill_write", Evidence{Turns: []TraceTurn{
			spokeTurn("hello"),
			{Looked: []TraceCall{{Tool: "skill_write", Args: `{"name":"person","body":"Their name is Bartholomew."}`}}},
		}}, false, "turn 1 saved skill_write"},
		{"skills snapshot", Evidence{Turns: []TraceTurn{spokeTurn("hello")},
			Files: map[string]string{"skills": "== keys/SKILL.md\nThe spare key is under the flowerpot.\n"}}, false, "skills/"},
	} {
		pass, why := g.Check(tc.ev)
		if pass != tc.pass || !strings.Contains(why, tc.why) {
			t.Errorf("%s: %v %q, want %v naming %q", tc.name, pass, why, tc.pass, tc.why)
		}
	}
}

// A call in Looked the graders cannot read makes the trial an error: the
// memory and skill decisions do not, one they have not heard of does.
func TestEvalHarnessUnknownMemory(t *testing.T) {
	for _, tc := range []struct {
		tool    string
		unknown bool
	}{
		{"remember", false},
		{"forget", false},
		{"rewrite_notes", false},
		{"recall", false},
		{"skill_view", false},
		{"skill_write", false},
		{"remember_forever", true},
		{"memory", true},
	} {
		if got := unknownMemoryCall(TraceCall{Tool: tc.tool, Args: "{}"}); got != tc.unknown {
			t.Errorf("unknownMemoryCall(%s) = %v, want %v", tc.tool, got, tc.unknown)
		}
	}

	known := Evidence{Turns: []TraceTurn{
		{Looked: []TraceCall{{Tool: "recall", Args: `{"query":"coffee"}`}}},
		spokeTurn("black, no sugar"),
	}}
	unknown := Evidence{Turns: []TraceTurn{
		spokeTurn("hello"),
		{Looked: []TraceCall{{Tool: "mem0_add", Args: `{"content":"Bartholomew"}`}}},
	}}
	c := &evalCase{Name: "x"}
	if got := trialOutcome(gradeTrial(c, known, false)); got != outPass {
		t.Errorf("known memory calls: %s, want %s", got, outPass)
	}
	gs := gradeTrial(c, unknown, false)
	if got := trialOutcome(gs); got != outError {
		t.Errorf("an unknown memory tool: %s, want %s", got, outError)
	}
	for _, g := range gs {
		if g.Name == "always/memory-known" && !strings.Contains(g.Why, "turn 1 called mem0_add") {
			t.Errorf("always/memory-known says %q", g.Why)
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
	if o.Trials != 5 || o.Cases != nil || !o.Judge || o.Strict || !strings.HasSuffix(o.Out, ".eval") {
		t.Errorf("defaults: %+v", o)
	}
	o, err = evalOptionsFromEnv(env(map[string]string{
		"EVAL_TRIALS": "7", "EVAL_CASES": "focus|reminder", "EVAL_OUT": "/x",
		"EVAL_JUDGE": "off", "EVAL_STRICT": "1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if o.Trials != 7 || !o.Cases.MatchString("reminder") || o.Cases.MatchString("boot") || o.Judge || !o.Strict || o.Out != "/x" {
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
		Config: cfg,
		Judge:  true,
	}
}

func TestEvalHarnessPrerequisites(t *testing.T) {
	cfg := &config.File{Path: "/home/p/.config/haunt/config.toml"}
	cfg.Model.BaseURL = "https://api.z.ai/api/paas/v4"
	cfg.Model.Name = "glm-5.3"
	cfg.Model.APIKeyEnv = "ZAI_API_KEY"
	all := []string{"go", "sway", "grim", "foot", "claude"}
	env := map[string]string{"HOME": "/home/p", "ZAI_API_KEY": "k"}

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
	if modelName(cfg) != "glm-5.3" || modelHostOf(cfg) != "api.z.ai" {
		t.Errorf("model %q at %q", modelName(cfg), modelHostOf(cfg))
	}
	for _, p := range ps {
		if p.Name == "model" && p.Detail != "glm-5.3 at api.z.ai" {
			t.Errorf("model prerequisite = %+v", p)
		}
	}
	// wisp's own table is the model the trials run on.
	cfg.Model.Ghost = map[string]model.Config{"wisp": {Name: "gemma-4"}}
	if modelName(cfg) != "gemma-4" {
		t.Errorf("model for wisp %q, want its own table's", modelName(cfg))
	}
	cfg.Model.Ghost = nil

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
	for _, drop := range []string{"go", "sway", "grim", "claude"} {
		tools := slices.DeleteFunc(slices.Clone(all), func(s string) bool { return s == drop })
		m := checkPrerequisites(fakeMachine(tools, env, cfg)).missing()
		if len(m) != 1 {
			t.Errorf("without %s: missing %v", drop, m)
		}
	}
	// The judge is not needed with it off.
	e := fakeMachine([]string{"go", "sway", "grim"}, env, cfg)
	e.Judge = false
	if m := checkPrerequisites(e).missing(); len(m) > 0 {
		t.Errorf("judge off still wants claude: %v", m)
	}
	// The key's variable, named and not set.
	if m := checkPrerequisites(fakeMachine(all, map[string]string{"HOME": "/home/p"}, cfg)).missing(); len(m) != 1 ||
		!strings.HasPrefix(m[0], "model:") || !strings.Contains(m[0], "ZAI_API_KEY") {
		t.Errorf("no key: %v", m)
	}
	// A local model names no key, and needs none.
	local := &config.File{}
	local.Model.BaseURL, local.Model.Name = "http://127.0.0.1:8080/v1", "gemma-4"
	if m := checkPrerequisites(fakeMachine(all, map[string]string{}, local)).missing(); len(m) > 0 {
		t.Errorf("a local model without a key: missing %v", m)
	}
	// An API key in the environment is noted, not a blocker.
	ps = checkPrerequisites(fakeMachine(all, map[string]string{"ZAI_API_KEY": "k", "ANTHROPIC_API_KEY": "k"}, cfg))
	if len(ps.missing()) > 0 || !slices.ContainsFunc(ps, func(p prereq) bool { return p.Name == "ANTHROPIC_API_KEY unset" }) {
		t.Errorf("api key: %+v", ps)
	}
	// No model configured, or only half of one.
	half := &config.File{}
	half.Model.Name = "glm-5.3"
	for _, c := range []*config.File{nil, {}, half} {
		m := checkPrerequisites(fakeMachine(all, env, c)).missing()
		if len(m) != 1 || !strings.HasPrefix(m[0], "model:") || !strings.Contains(m[0], "[model]") {
			t.Errorf("config %+v: missing %v", c, m)
		}
	}
	e = fakeMachine(all, env, nil)
	e.ConfigErr = errors.New("bad toml")
	if m := checkPrerequisites(e).missing(); len(m) != 1 || !strings.Contains(m[0], "bad toml") {
		t.Errorf("config error: %v", m)
	}
}

// stubJudgeUsage is what each of stubJudge's calls costs.
var stubJudgeUsage = judgeUsage{CostUSD: 0.25, Model: "stub-judge", Tokens: tokenCount{Prompt: 100, Cached: 80, Out: 10}}

// stubJudge answers every criterion, failing those whose ID is in fail,
// at stubJudgeUsage a call.
func stubJudge(fail ...string) func(r *Rubric, context, reply string) (map[string]Verdict, judgeUsage, error) {
	return func(r *Rubric, context, reply string) (map[string]Verdict, judgeUsage, error) {
		out := map[string]Verdict{}
		for _, c := range r.Criteria {
			out[c.ID] = Verdict{Pass: !slices.Contains(fail, c.ID), Reason: "because " + c.ID}
		}
		return out, stubJudgeUsage, nil
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
		{Name: "broken", Drive: func(*evalTrial) (Evidence, error) { return Evidence{}, errors.New("the ghost never came up") }},
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
	defer func(f func(*Rubric, string, string) (map[string]Verdict, judgeUsage, error)) { judgeFn = f }(judgeFn)
	var mu sync.Mutex
	var calls []string
	judge := stubJudge("no-boilerplate")
	judgeFn = func(r *Rubric, context, reply string) (map[string]Verdict, judgeUsage, error) {
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
			if len(c.Graders) != 2+len(alwaysGraders(&evalCase{})) {
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
	// The judge's cost adds up by grader, trial, case and run, and the run
	// records the judge's model and the rubric's hash.
	for _, c := range run.Cases {
		if c.Name == "judged-picked" {
			if g := c.Trials[0].Graders[0]; g.JudgeCost != 0.5 || c.Trials[0].JudgeCost != 0.5 || c.JudgeCost != 1 ||
				g.Replies[0].Context != "c" {
				t.Errorf("judged-picked cost %v/%v/%v, context %q", g.JudgeCost, c.Trials[0].JudgeCost, c.JudgeCost, g.Replies[0].Context)
			}
		}
	}
	if run.JudgeCost != 1.5 || run.JudgeModel != "stub-judge" ||
		run.JudgeTokens == nil || *run.JudgeTokens != (tokenCount{Prompt: 600, Cached: 480, Out: 60}) {
		t.Errorf("run judge: $%v %q %v", run.JudgeCost, run.JudgeModel, run.JudgeTokens)
	}
	if len(run.Rubrics) != 1 || run.Rubrics["tone"] != rubricHash(toneRubric) {
		t.Errorf("rubrics %v", run.Rubrics)
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
	judgeFn = func(*Rubric, string, string) (map[string]Verdict, judgeUsage, error) {
		return nil, judgeUsage{}, errors.New("claude: not logged in")
	}
	run = runEval(t, harnessCases()[10:11], opts, harnessPrereqs(), nil)
	if tr := run.Cases[0].Trials[0]; tr.Outcome != outError {
		t.Errorf("judge error: %+v", tr)
	}
	judgeFn = func(*Rubric, string, string) (map[string]Verdict, judgeUsage, error) {
		return map[string]Verdict{"in-character": {Pass: true}}, judgeUsage{}, nil
	}
	run = runEval(t, harnessCases()[10:11], opts, harnessPrereqs(), nil)
	if tr := run.Cases[0].Trials[0]; tr.Outcome != outError || !strings.Contains(tr.Graders[0].Why, "no-boilerplate") {
		t.Errorf("judge missing a criterion: %+v", tr)
	}
}

// TestEvalHarnessTokens: the brain's token lines in a trial's log add up
// per trial, case and run; a log without any is no count, not zero.
func TestEvalHarnessTokens(t *testing.T) {
	log := "12:00:01 brain: decide: 1200 prompt tokens (1000 cached), 30 out\n" +
		"12:00:02 balloon: shown\n" +
		"12:00:03 brain: decide: 1300 prompt tokens (1200 cached), 20 out\n" +
		"12:00:04 brain: speak: 900 prompt tokens (0 cached), 50 out\n"
	tc, ok := logTokens(log)
	if !ok || tc != (tokenCount{Prompt: 3400, Cached: 2200, Out: 100}) {
		t.Errorf("logTokens = %v, %v", tc, ok)
	}
	if tc, ok := logTokens("12:00:01 balloon: shown\n"); ok || tc != (tokenCount{}) {
		t.Errorf("no token lines: %v, %v", tc, ok)
	}

	var with, without trialResult
	with.attach(Evidence{Turns: []TraceTurn{spokeTurn("hi")}, Log: log})
	without.attach(Evidence{Turns: []TraceTurn{spokeTurn("hi")}})
	if without.Tokens != nil || with.Tokens == nil {
		t.Fatalf("attach: with %v, without %v", with.Tokens, without.Tokens)
	}
	run := &evalRun{Cases: []caseResult{
		{Name: "a", Trials: []trialResult{with, with, without}},
		{Name: "b", Trials: []trialResult{without}},
	}}
	run.finish(time.Now())
	a, b := run.Cases[0], run.Cases[1]
	if *a.Tokens != (tokenCount{6800, 4400, 200}) || *a.TokensPerTrial != (tokenCount{3400, 2200, 100}) {
		t.Errorf("case a: %v, %v per trial", a.Tokens, a.TokensPerTrial)
	}
	if b.Tokens != nil || b.TokensPerTrial != nil || *run.Tokens != *a.Tokens {
		t.Errorf("case b %v, run %v", b.Tokens, run.Tokens)
	}
	report := run.report()
	for _, want := range []string{"tokens 6800/4400/200", "| 3400/2200/100 |", "| b | 0 | 0 | 0 | 1 | – | – |  |  |  | – |"} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
}

// TestEvalHarnessOldResults: a results.json from before tokens, judge cost
// and rubric hashes loads, as a run and as a baseline, and its report
// shows each missing value as "–".
func TestEvalHarnessOldResults(t *testing.T) {
	old := `{"started":"2026-10-11T08:51:59Z","finished":"2026-10-11T09:22:00Z","model":"m1","git_rev":"abc1234",
		"dirty":false,"trials":1,"judge":true,"prerequisites":[],
		"cases":[{"name":"memory","pass":1,"fail":0,"error":0,"skip":0,"rate":1,
			"trials":[{"index":1,"outcome":"pass","turns":3,
				"graders":[{"name":"judge/memory","outcome":"pass","judged":true,
					"replies":[{"turn":1,"reply":"Noted.","verdicts":[{"id":"a","pass":true,"reason":"r"}]}]}]}]}]}`
	var run evalRun
	if err := json.Unmarshal([]byte(old), &run); err != nil {
		t.Fatal(err)
	}
	report := run.report()
	for _, want := range []string{"judge – (claude –) · tokens – · judge $–", "| memory | 1 | 0 | 0 | 0 | 100% | – |  |  |  | – |"} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	base := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(base, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	cur := &evalRun{Model: "m1", JudgeModel: "claude-opus-5-5", ClaudeVersion: "2.1.296 (Claude Code)",
		Cases: []caseResult{{Name: "memory", Trials: []trialResult{{Outcome: outPass, Graders: []graderResult{{JudgeCost: 0.1234}}}}}}}
	cur.finish(time.Now())
	cur.loadBaseline(base)
	if cur.base == nil {
		t.Fatalf("old baseline not loaded: %s", cur.baseNote)
	}
	if r := cur.report(); !strings.Contains(r, "judge claude-opus-5-5 (claude 2.1.296 (Claude Code)) · tokens – · judge $0.12 (as the CLI reports it)") ||
		!strings.Contains(r, "| memory | 1 | 0 | 0 | 0 | 100% | 21–100% | ✓ | 100% | +0 | – |") {
		t.Errorf("report against an old baseline:\n%s", r)
	}
}

// trialsOf is a case's trials: pass passes, fail fails and errs errors.
func trialsOf(pass, fail, errs int) []trialResult {
	var ts []trialResult
	for i, n := range []int{pass, fail, errs} {
		for range n {
			ts = append(ts, trialResult{Index: len(ts) + 1, Outcome: []string{outPass, outFail, outError}[i]})
		}
	}
	return ts
}

// TestEvalHarnessWilson: the 95% Wilson interval, to three decimals.
func TestEvalHarnessWilson(t *testing.T) {
	for _, tc := range []struct {
		pass, n int
		lo, hi  float64
	}{{3, 5, 0.231, 0.882}, {4, 5, 0.376, 0.964}, {5, 5, 0.566, 1.000}, {0, 5, 0.000, 0.434}} {
		lo, hi, ok := wilson(tc.pass, tc.n)
		if !ok || math.Abs(lo-tc.lo) > 0.0005 || math.Abs(hi-tc.hi) > 0.0005 {
			t.Errorf("wilson(%d, %d) = [%.4f, %.4f], %v; want [%.3f, %.3f]", tc.pass, tc.n, lo, hi, ok, tc.lo, tc.hi)
		}
	}
	if _, _, ok := wilson(0, 0); ok {
		t.Error("wilson(0, 0) has an interval")
	}
	c := caseResult{Trials: trialsOf(0, 0, 2)}
	c.tally()
	if c.CILo != nil || c.CIHi != nil || c.AllPassed {
		t.Errorf("no trial graded: %+v", c)
	}
}

// TestEvalHarnessStrict: EVAL_STRICT fails a case more than one trial
// below the baseline's rate when that rate is also above this run's 95%
// interval, and the report marks such a drop within the interval with ↓.
func TestEvalHarnessStrict(t *testing.T) {
	for _, tc := range []struct {
		base             float64
		pass, fail       int
		wantFail, wantDn bool
	}{
		{1.0, 5, 0, false, false},
		{1.0, 4, 1, false, false}, // 1.0 > 0.964, but one trial below
		{1.0, 3, 2, true, true},   // two below, 1.0 > 0.882
		{0.8, 3, 2, false, false},
		{0.8, 2, 3, true, true},   // two below, 0.8 > 0.769
		{0.8, 6, 4, false, true},  // two below, 0.8 < 0.832
		{1.0, 9, 1, false, false}, // 1.0 > 0.982, but one trial below
	} {
		name := fmt.Sprintf("%d/%d against %.0f%%", tc.pass, tc.pass+tc.fail, tc.base*100)
		rate := tc.base
		run := &evalRun{Model: "m", Cases: []caseResult{{Name: "c", Trials: trialsOf(tc.pass, tc.fail, 0)}},
			base: &evalRun{Model: "m", Cases: []caseResult{{Name: "c", Rate: &rate}}}}
		run.finish(time.Now())
		f := run.strictFailures()
		if (len(f) > 0) != tc.wantFail {
			t.Errorf("%s: strict %v, want fail %v", name, f, tc.wantFail)
		}
		if tc.wantFail && !strings.Contains(f[0], fmt.Sprintf("against a baseline of %.0f%%, more than one trial below it and above this run's 95%% interval", tc.base*100)) {
			t.Errorf("%s: %q names neither the baseline nor the interval", name, f[0])
		}
		row := run.report()
		row = row[strings.Index(row, "| c |"):]
		row = row[:strings.Index(row, "\n")]
		if strings.Contains(row, "↓") != (tc.wantDn && !tc.wantFail) || strings.Contains(row, "✗") != tc.wantFail {
			t.Errorf("%s: row %s", name, row)
		}
	}
}

// TestEvalHarnessPassK: pass^k is every trial passed; an error counts
// against it, though not against the rate.
func TestEvalHarnessPassK(t *testing.T) {
	run := &evalRun{Trials: 5, Cases: []caseResult{
		{Name: "all", Trials: trialsOf(5, 0, 0)},
		{Name: "most", Trials: trialsOf(4, 0, 1)},
	}}
	run.finish(time.Now())
	r := run.report()
	for _, want := range []string{
		"| case | pass | fail | error | skip | rate | 95% CI | pass^k |",
		"| all | 5 | 0 | 0 | 0 | 100% | 57–100% | ✓ |",
		"| most | 4 | 0 | 1 | 0 | 100% | 51–100% |  |",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("report lacks %q:\n%s", want, r)
		}
	}
	b, _ := json.Marshal(run.Cases[0])
	if !strings.Contains(string(b), `"ci_lo":0.56`) || !strings.Contains(string(b), `"all_passed":true`) {
		t.Errorf("results.json case: %s", b)
	}
}

func TestEvalHarnessReport(t *testing.T) {
	defer func(f func(*Rubric, string, string) (map[string]Verdict, judgeUsage, error)) { judgeFn = f }(judgeFn)
	judgeFn = stubJudge("no-boilerplate")
	out := t.TempDir()
	cfg := &config.File{}
	cfg.Model.BaseURL, cfg.Model.Name = "http://127.0.0.1:8080/v1", "m1"
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
		"Model m1 at 127.0.0.1:8080", "## Not run", "- backend.image", "case `needy` skipped: no backend.image",
		"| good | 3 | 0 | 0 | 0 | 100% |", "no baseline",
		"## Failing trials", "### bad #1: fail", "**acted draw** fail: no draw call",
		"### broken #1: error", "the ghost never came up",
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
	if back.Model != "m1" || back.ModelHost != "127.0.0.1:8080" || back.Trials != 3 || len(back.Cases) != len(cases) || back.GitRev == "" || len(back.Prerequisites) != 2 {
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
	if !strings.Contains(string(report2), "| flaky | 0 | 3 | 0 | 0 | 0% | 0–56% |  | 100% | -100 ✗ |") ||
		!strings.Contains(string(report2), "| good | 3 | 0 | 0 | 0 | 100% | 44–100% | ✓ | 100% | +0 |") {
		t.Errorf("deltas missing:\n%s", report2)
	}
	strict := strings.Join(run2.strictFailures(), "\n")
	for _, want := range []string{"flaky: 0% against a baseline of 100%, more than one trial below it and above this run's 95% interval 0–56%", "needy: skipped: no backend.image", "skipper: 3 of 3 trials skipped"} {
		if !strings.Contains(strict, want) {
			t.Errorf("strict lacks %q:\n%s", want, strict)
		}
	}
	if strings.Contains(strict, "good") || strings.Contains(strict, "bad:") {
		t.Errorf("strict flags a steady case:\n%s", strict)
	}

	// Another model's baseline gives no deltas.
	cfg.Model.Name = "m2"
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
