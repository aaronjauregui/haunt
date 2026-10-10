// Package model is haunt's client for an OpenAI-compatible chat
// completions endpoint: streaming, tool calls, assistant prefill, sampling
// and thinking control, with trust derived from where the endpoint runs.
//
// One protocol covers the servers worth reaching: llama.cpp's
// llama-server, z.ai, OpenAI, OpenRouter, Ollama and vLLM all answer
// POST <base_url>/chat/completions with the same SSE stream. Fields one
// of them does not know (the llama.cpp sampling extras, the two ways of
// switching thinking off) are sent anyway; a provider ignores what it
// does not read, which keeps one request shape for every provider.
//
// The user's config picks the model ([model] and [model.ghost.<name>]); a
// ghost's manifest never names one. See docs/user/model.md.
package model

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"time"
)

// Config is the [model] section and a per-ghost override of it.
type Config struct {
	// BaseURL is the endpoint up to and including its version,
	// "http://127.0.0.1:8080/v1"; /chat/completions is appended.
	BaseURL string `toml:"base_url"`
	// Name is the model as the server knows it.
	Name string `toml:"name"`
	// APIKeyEnv names the environment variable holding the key. The key
	// itself is never in the config. Empty sends no Authorization header,
	// which is what a local server wants.
	APIKeyEnv string `toml:"api_key_env"`
	// ContextLength is the model's context window in tokens.
	ContextLength int    `toml:"context_length"`
	Trust         string `toml:"trust"` // "", "local", "cloud"
	// FoldOverride is the fold key; nil: by trust.
	FoldOverride *bool    `toml:"fold"`
	Thinking     string   `toml:"thinking"` // "", "off", "low", "medium", "high"
	Sampling     Sampling `toml:"sampling"`
	// FirstTokenSeconds is how long a call may go without a sign of life
	// (a word, reasoning, a tool call) before it is given up on; 0 is
	// DefaultFirstTokenLocal or DefaultFirstTokenCloud by IsLocal. The
	// deadline is on the first sign rather than the
	// whole call because a long answer is working, and a server that has
	// said nothing at all has wedged or cannot reach its model.
	FirstTokenSeconds int `toml:"first_token_timeout"`
	// StallSeconds is how long a call that has shown a sign of life may
	// then go with nothing on its stream; 0 is DefaultStall.
	StallSeconds int `toml:"stall_timeout"`
}

// Default deadlines. A local server processes a cold prompt at a few
// hundred tokens a second (about 500 for Gemma 4 31B on llama-server), so
// a long transcript takes it a minute or more before its first word; a
// cloud provider starts within seconds. The stall deadline is far more
// than any model needs between two tokens, and is there for a provider
// that went away mid-call.
const (
	DefaultFirstTokenLocal = 120 * time.Second
	DefaultFirstTokenCloud = 30 * time.Second
	DefaultStall           = 120 * time.Second
)

// Trust values.
const (
	TrustLocal = "local"
	TrustCloud = "cloud"
)

// Thinking values. Empty is the provider's default.
const (
	ThinkingOff    = "off"
	ThinkingLow    = "low"
	ThinkingMedium = "medium"
	ThinkingHigh   = "high"
)

// Sampling is the per-request sampling fields. Nil means the provider's
// default. The llama.cpp extras are passed through and ignored elsewhere.
type Sampling struct {
	Temperature    *float64 `toml:"temperature"`
	TopP           *float64 `toml:"top_p"`
	MinP           *float64 `toml:"min_p"`
	RepeatPenalty  *float64 `toml:"repeat_penalty"`
	DRYMultiplier  *float64 `toml:"dry_multiplier"`
	XTCProbability *float64 `toml:"xtc_probability"`
}

// Over is s with every field o sets replacing s's: o is the more specific
// of the two (a ghost's over the troupe's, a request's over the config's).
func (s Sampling) Over(o Sampling) Sampling {
	pick := func(a, b *float64) *float64 {
		if b != nil {
			return b
		}
		return a
	}
	return Sampling{
		Temperature:    pick(s.Temperature, o.Temperature),
		TopP:           pick(s.TopP, o.TopP),
		MinP:           pick(s.MinP, o.MinP),
		RepeatPenalty:  pick(s.RepeatPenalty, o.RepeatPenalty),
		DRYMultiplier:  pick(s.DRYMultiplier, o.DRYMultiplier),
		XTCProbability: pick(s.XTCProbability, o.XTCProbability),
	}
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
// Args is what the model wrote, which a small model does not always make
// valid JSON; the caller checks it and tells the model when it is not.
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
	// Thinking allows the model to reason before answering. False switches
	// it off; true leaves it to Config.Thinking, which may still say off.
	Thinking bool
	// Sampling is laid over Config.Sampling field by field.
	Sampling Sampling
	Stop     []string
}

// EventKind says what an Event carries.
type EventKind int

const (
	// EventDelta is words of the reply, in order. With a prefill they
	// continue it; the prefill itself is not repeated.
	EventDelta EventKind = iota
	// EventToolCall is one whole tool call, its streamed fragments joined.
	EventToolCall
	// EventUsage is the provider's token count, when it reports one.
	EventUsage
	// EventFinish ends a stream that completed, with the finish reason.
	EventFinish
	// EventError ends a stream that did not.
	EventError
)

// Usage is the token count the provider reports.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	// CachedTokens is how many of PromptTokens the server reused from
	// its cache rather than processed, when it says (llama.cpp and
	// OpenAI do, in prompt_tokens_details).
	CachedTokens int
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
// network: Trust if set, else derived from BaseURL's host. Loopback,
// localhost, the unspecified address and the private ranges (RFC 1918 and
// IPv6 unique local) are local; every other host is cloud, including a
// name that would resolve to a private address, since resolving it would
// make trust depend on the resolver of the moment. trust = "local" says
// so for such a host.
func (c Config) IsLocal() bool {
	switch c.Trust {
	case TrustLocal:
		return true
	case TrustCloud:
		return false
	}
	return localHost(c.BaseURL)
}

// localHost is whether a URL's host is this machine or the private
// network, by its text alone.
func localHost(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()
}

// Fold reports whether the words go in the decide call: FoldOverride if
// set, else true for cloud and false for local. The field is not named
// Fold because a field and a method cannot share a name.
func (c Config) Fold() bool {
	if c.FoldOverride != nil {
		return *c.FoldOverride
	}
	return !c.IsLocal()
}

// Over is c with every field o sets replacing c's, sampling field by
// field: how [model.ghost.<name>] is laid over [model]. A field cannot be
// set back to empty this way; a ghost that needs a different endpoint
// names it.
func (c Config) Over(o Config) Config {
	if o.BaseURL != "" {
		c.BaseURL = o.BaseURL
	}
	if o.Name != "" {
		c.Name = o.Name
	}
	if o.APIKeyEnv != "" {
		c.APIKeyEnv = o.APIKeyEnv
	}
	if o.ContextLength != 0 {
		c.ContextLength = o.ContextLength
	}
	if o.Trust != "" {
		c.Trust = o.Trust
	}
	if o.FoldOverride != nil {
		c.FoldOverride = o.FoldOverride
	}
	if o.Thinking != "" {
		c.Thinking = o.Thinking
	}
	if o.FirstTokenSeconds != 0 {
		c.FirstTokenSeconds = o.FirstTokenSeconds
	}
	if o.StallSeconds != 0 {
		c.StallSeconds = o.StallSeconds
	}
	c.Sampling = c.Sampling.Over(o.Sampling)
	return c
}

// FirstToken is FirstTokenSeconds with its default, which depends on
// where the model runs.
func (c Config) FirstToken() time.Duration {
	if c.FirstTokenSeconds > 0 {
		return time.Duration(c.FirstTokenSeconds) * time.Second
	}
	if c.IsLocal() {
		return DefaultFirstTokenLocal
	}
	return DefaultFirstTokenCloud
}

// Stall is StallSeconds with its default.
func (c Config) Stall() time.Duration {
	if c.StallSeconds > 0 {
		return time.Duration(c.StallSeconds) * time.Second
	}
	return DefaultStall
}
