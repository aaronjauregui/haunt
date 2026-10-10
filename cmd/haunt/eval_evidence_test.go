//go:build eval

package main

import (
	"time"

	"github.com/aaronjauregui/haunt/internal/store"
)

// This file holds the types the eval harness, the real-hermes rig and the
// judge share, so each can be built without the others: the rig fills an
// Evidence, the harness runs Graders over it, and a judged Grader carries a
// Rubric for the judge.

// TraceTurn is one record of `haunt history -json`: one turn of the
// ghost's conversation with hermes, most recent last.
type TraceTurn struct {
	Time       time.Time   `json:"time"`
	ProvokedBy string      `json:"provoked_by"` // "person" or "event"
	Input      string      `json:"input"`
	Reply      string      `json:"reply"`
	Silent     bool        `json:"silent"`
	Failed     bool        `json:"failed"` // hermes closed the turn as failed
	SelfPrompt bool        `json:"self_prompt"`
	Acted      []TraceCall `json:"acted"`  // haunt's own tools, mcp__haunt__ stripped
	Looked     []TraceCall `json:"looked"` // hermes's own tools: memory, skill_view, …
	Echoes     []TraceEcho `json:"echoes"` // memory writes repeating a page or a file
	Window     *struct {
		App   string `json:"app"`
		Title string `json:"title"`
	} `json:"window,omitempty"`
}

// TraceCall is one tool call within a turn, with its arguments (JSON text)
// and its result.
type TraceCall struct {
	Tool   string `json:"tool"`
	Args   string `json:"args"`
	Result string `json:"result"`
}

// TraceEcho is a memory write whose words repeat what the ghost read from
// the web or a file.
type TraceEcho struct {
	Tool     string   `json:"tool"`
	Saved    string   `json:"saved"`
	Read     string   `json:"read"`
	ReadArgs string   `json:"read_args"`
	TurnsAgo int      `json:"turns_ago"`
	Shared   []string `json:"shared"`
}

// Evidence is everything a trial leaves behind for its graders.
type Evidence struct {
	Turns      []TraceTurn       // history -json
	Intentions string            // journal -intentions
	Store      *store.Store      // the trial's state, still open
	Log        string            // haunt's stderr, HAUNT_DEBUG=balloon,input
	Files      map[string]string // e.g. the notes file, by name
	Marks      map[string]int    // turn index when the driver did X ("injected", "restarted", …)
}

// Grader checks one thing about a trial. A code grader has only Check; a
// judged grader also has Judge, the rubric its replies are held to.
type Grader struct {
	Name  string
	Judge *Rubric // nil for code graders
	Check func(Evidence) (pass bool, why string)
	// Fault marks a harness check ("the event reached the ghost"): its
	// failure makes the trial an error, not a fail.
	Fault bool
	// Pick chooses the replies a judged grader holds to its rubric and what
	// the judge is told about each. Left nil, the judge is given every
	// spoken reply with what provoked it.
	Pick func(Evidence) []judgeItem
}

// Rubric is a judged grader's fixed list of yes/no questions. The trial
// passes the grader when every criterion does.
type Rubric struct {
	Name     string
	Criteria []Criterion
}

// Criterion is one question with one right answer, phrased so that "yes"
// is the passing answer, e.g. "Does the reply avoid claiming it can read
// email?". ID names it in the judge's schema and the report.
type Criterion struct {
	ID       string
	Question string
}

// Verdict is the judge's answer to one criterion.
type Verdict struct {
	Pass   bool   `json:"pass"`
	Reason string `json:"reason"`
}
