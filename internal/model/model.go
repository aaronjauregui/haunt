// Package model is haunt's client for an OpenAI-compatible chat
// completions endpoint: streaming, tool calls, assistant prefill, sampling
// and thinking control, with trust derived from where the endpoint runs.
//
// The user's config picks the model ([model] and [model.ghost.<name>]); a
// ghost's manifest never names one. See docs/user/model.md.
package model

import (
	"context"
	"encoding/json"
)

// Config is the [model] section and a per-ghost override of it.
type Config struct {
	BaseURL       string
	Name          string
	APIKeyEnv     string
	ContextLength int
	Trust         string // "", "local", "cloud"
	FoldOverride  *bool  // the fold key; nil: by trust
	Thinking      string // "", "off", "low", "medium", "high"
	Sampling      Sampling
}

// Sampling is the per-request sampling fields. Nil means the provider's
// default. The llama.cpp extras are passed through and ignored elsewhere.
type Sampling struct {
	Temperature    *float64
	TopP           *float64
	MinP           *float64
	RepeatPenalty  *float64
	DRYMultiplier  *float64
	XTCProbability *float64
}

// Message is one chat message in a request or reply.
type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	Name       string
}

// ToolCall is a model's call of a tool, arguments as the JSON text sent.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// Tool is a tool offered to the model.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Request is one chat completion call.
type Request struct {
	Messages []Message
	Tools    []Tool
	Prefill  string // appended as a trailing assistant message
	Thinking bool
	Sampling Sampling
	Stop     []string
}

// EventKind says what an Event carries.
type EventKind int

const (
	EventDelta EventKind = iota
	EventToolCall
	EventUsage
	EventFinish
	EventError
)

// Usage is the token count the provider reports.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// Event is one item of a streamed reply.
type Event struct {
	Kind     EventKind
	Delta    string
	ToolCall *ToolCall
	Usage    *Usage
	Finish   string
	Err      error
}

// Client streams chat completions.
type Client interface {
	Stream(ctx context.Context, r Request) (<-chan Event, error)
}

// IsLocal reports whether the model runs on this machine or the private
// network: Trust if set, else derived from BaseURL's host.
func (c Config) IsLocal() bool { panic("unimplemented") }

// Fold reports whether the words go in the decide call: FoldOverride if
// set, else true for cloud and false for local. The field is not named
// Fold because a field and a method cannot share a name.
func (c Config) Fold() bool { panic("unimplemented") }
