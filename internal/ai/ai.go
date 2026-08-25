// Package ai is the driver-owned AI service layer. Brains never hold API
// keys or talk to backends; they emit intents, the driver calls the
// user-configured backend within budget, and results come back as events.
// Local-first: any OpenAI-compatible endpoint works (Ollama, llama.cpp
// server, vLLM, cloud providers).
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// Config is the [ai] portion of the user config. The *user* chooses
// backends; ghosts only declare that they want the capability.
type Config struct {
	LLM    LLMConfig    `toml:"llm"`
	Image  ImageConfig  `toml:"image"`
	Embed  EmbedConfig  `toml:"embed"`
	Vision VisionConfig `toml:"vision"`
	Budget BudgetConfig `toml:"budget"`

	// ExpressionInference chooses how dialogue is mapped to shell poses:
	// "lexicon" (default) uses the driver's free word table, "llm" adds a
	// classifying request per generated line and lets the model decide,
	// and "off" leaves the face alone entirely.
	//
	// "llm" costs one extra request per generated line — nothing against a
	// local model, a doubled bill against a cloud one — and wants a model
	// competent at a one-word classification. Very small models do this
	// badly enough to be worse than the word table, so the default stays
	// free and this is opt-in.
	ExpressionInference string `toml:"expression_inference"`

	// ToolTimeout is how long one tool call may take, in seconds (default
	// 120), and MaxToolCalls how many tool calls one exchange may make
	// before the model has to answer with what it has (default 6).
	ToolTimeout  int `toml:"tool_timeout"`
	MaxToolCalls int `toml:"max_tool_calls"`

	// FirstTokenTimeout is how long a streaming chat may go without a
	// single token before the driver gives up on it, in seconds (default
	// 30). The deadline is on the first token rather than the whole reply:
	// a long answer arriving steadily is a backend working, while silence
	// is a backend that has wedged — and the ghost answers one request at a
	// time, so waiting out an HTTP timeout costs it every request behind
	// this one.
	FirstTokenTimeout int `toml:"first_token_timeout"`

	// LogPrompts dumps every request this layer sends — the full message
	// array, the resolved image parameters — to the driver log. Off by
	// default: prompts carry the conversation and the ghost's journal, so
	// this is opt-in and never reaches session recordings. HAUNT_AI_DEBUG=1
	// turns it on without editing the config.
	LogPrompts bool `toml:"log_prompts"`
}

// LLMConfig points at an OpenAI-compatible chat completions endpoint.
type LLMConfig struct {
	// BaseURL like "http://localhost:11434/v1" (Ollama) — required to
	// enable the llm capability at runtime.
	BaseURL string `toml:"base_url"`
	Model   string `toml:"model"`
	// APIKeyEnv names an environment variable holding the key, so the key
	// itself never sits in the config file.
	APIKeyEnv string `toml:"api_key_env"`
	// Temperature overrides the backend's own default for dialogue. Nil
	// leaves the field out of the request entirely.
	Temperature *float64 `toml:"temperature"`
	// SilentTemperature is used for brain-internal calls (see Options.
	// Silent) instead of Temperature. These calls are machinery, not
	// conversation — a prompt rewrite that reads differently every time
	// misses the generation cache — so they default cold rather than
	// inheriting the chatty default.
	SilentTemperature *float64 `toml:"silent_temperature"`
}

// defaultSilentTemperature keeps brain-internal answers reproducible.
const defaultSilentTemperature = 0.2

// temperature resolves the sampling temperature for a call, and whether to
// send one at all.
func (c *LLMConfig) temperature(silent bool) (float64, bool) {
	if silent {
		if c.SilentTemperature != nil {
			return *c.SilentTemperature, true
		}
		return defaultSilentTemperature, true
	}
	if c.Temperature != nil {
		return *c.Temperature, true
	}
	return 0, false
}

// ImageConfig points at an image generation backend.
type ImageConfig struct {
	// Backend selects the protocol: "openai" (default; any OpenAI-compatible
	// /images/generations endpoint) or "comfyui" (workflow-graph API).
	Backend string `toml:"backend"`
	BaseURL string `toml:"base_url"`
	// Model is the model name (openai) or checkpoint filename (comfyui).
	Model     string `toml:"model"`
	APIKeyEnv string `toml:"api_key_env"`
	// Size like "512x512"; backend-defined default when empty.
	Size string `toml:"size"`

	// ComfyUI tuning; sensible defaults when zero.
	Steps    int     `toml:"steps"`
	CFG      float64 `toml:"cfg"`
	Sampler  string  `toml:"sampler"`
	Negative string  `toml:"negative"`
}

// VisionConfig points at a model that can be shown a picture. Empty
// disables looking at the screen regardless of what a ghost asks for: the
// capability says a ghost may look, this says there is anything to look
// with.
type VisionConfig struct {
	// BaseURL defaults to the LLM base URL.
	BaseURL string `toml:"base_url"`
	// Model must be vision-capable. Screen understanding is a task where
	// the difference between a frontier model and a small local one is
	// stark, so this is configured separately from dialogue.
	Model     string `toml:"model"`
	APIKeyEnv string `toml:"api_key_env"`
}

func (c *VisionConfig) apiKey() string {
	if c.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(c.APIKeyEnv)
}

// BudgetConfig caps how fast ghosts may burn backend calls. Zero values get
// defaults; the driver enforces these, not the ghost.
type BudgetConfig struct {
	LLMRequestsPerHour   int `toml:"llm_requests_per_hour"`
	ImageRequestsPerHour int `toml:"image_requests_per_hour"`
	// ToolRequestsPerHour caps how fast a ghost may *act*. Separate from
	// the LLM allowance because it rations something else: an LLM call
	// spends money and a tool call changes the world, and one exchange can
	// contain several of the second.
	ToolRequestsPerHour int `toml:"tool_requests_per_hour"`
	// EmbedRequestsPerHour is separate and generous: memory work rides
	// along with dialogue (a recall on the way in, a write on the way out)
	// and must not consume the conversation's own allowance.
	EmbedRequestsPerHour int `toml:"embed_requests_per_hour"`
}

const (
	defaultLLMPerHour   = 120
	defaultImagePerHour = 12
	defaultEmbedPerHour = 600
	// defaultToolPerHour is a working afternoon's worth of doing things
	// and nothing like enough for a loop.
	defaultToolPerHour = 60

	// defaultFirstTokenTimeout is generous enough for a local model being
	// loaded from disk on the first request of the day, and short enough
	// that a backend which will never answer is noticed while the person
	// who asked is still there.
	defaultFirstTokenTimeout = 30 * time.Second

	// defaultToolTimeout is long enough for a build or a slow network call
	// and short enough that a wedged server is noticed the same afternoon.
	defaultToolTimeout = 2 * time.Minute
	// defaultMaxToolCalls is enough for look-then-act-then-say, which is
	// the shape nearly every useful exchange has, and few enough that a
	// model going in circles stops quickly.
	defaultMaxToolCalls = 6
)

// WithDefaults returns cfg with the budget defaults filled in. The [ai]
// section is read for it by internal/config, which owns the file; this is
// only the part that knows what an absent budget should mean.
func WithDefaults(cfg Config) *Config {
	if cfg.Budget.LLMRequestsPerHour == 0 {
		cfg.Budget.LLMRequestsPerHour = defaultLLMPerHour
	}
	if cfg.Budget.ImageRequestsPerHour == 0 {
		cfg.Budget.ImageRequestsPerHour = defaultImagePerHour
	}
	if cfg.Budget.EmbedRequestsPerHour == 0 {
		cfg.Budget.EmbedRequestsPerHour = defaultEmbedPerHour
	}
	if cfg.Budget.ToolRequestsPerHour == 0 {
		cfg.Budget.ToolRequestsPerHour = defaultToolPerHour
	}
	return &cfg
}

func (c *LLMConfig) apiKey() string {
	if c.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(c.APIKeyEnv)
}

func (c *ImageConfig) apiKey() string {
	if c.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(c.APIKeyEnv)
}

// Message is one chat turn. The three tool fields are empty for every
// message in an ordinary conversation and are only ever set inside a single
// exchange — an assistant turn that asked for a tool, and the tool turns
// answering it. None of them are saved into the ghost's history: what
// outlives the exchange is what was asked and what was finally said, not
// the machinery in between. See execChat.
type Message struct {
	Role    string `json:"role"` // system, user, assistant, tool
	Content string `json:"content"`
	// ToolCalls is what an assistant turn asked to run.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID ties a tool turn back to the call it answers.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Name is the tool a tool turn is the result of. Not every backend
	// wants it and none mind it.
	Name string `json:"name,omitempty"`
}

// Tool is one thing the model may ask to have done, in the shape the chat
// completions API wants. Haunt fills these in from the MCP servers a
// ghost's manifest declares and the user has approved; the schema is the
// server's own, passed through unchanged.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

// ToolCall is the model asking for one. Arguments is a JSON object as a
// string, which is how the API sends it and how it is streamed — in
// fragments that have to be concatenated before they are valid JSON at all.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Reply is what one chat request produced: words, or a request to do
// something first, and occasionally both. A model that asks for a tool has
// not finished answering, so the caller runs what it asked for, appends the
// results, and asks again — see the tool loop in internal/ghost.
type Reply struct {
	Text  string
	Calls []ToolCall
}

// Budgets is the hourly ration a Service spends against. It is a value of
// its own rather than three fields inside Service because it is the one
// part of a Service that several ghosts may need to *share*: the rate
// limits are the user's money, and three resident ghosts each with a
// private 120 calls an hour is 360 calls an hour. Ghosts running alone get
// their own, which is the same thing seen from one side.
type Budgets struct {
	llm   *budget
	image *budget
	embed *budget
	tool  *budget
}

// NewBudgets builds an hourly ration from config. Defaults are filled in
// here as well as in WithDefaults, so a Config built directly — tests,
// embedders — gets a working allowance rather than one that refuses
// everything.
func NewBudgets(cfg BudgetConfig) *Budgets {
	return &Budgets{
		llm:   newBudget(orDefault(cfg.LLMRequestsPerHour, defaultLLMPerHour)),
		image: newBudget(orDefault(cfg.ImageRequestsPerHour, defaultImagePerHour)),
		embed: newBudget(orDefault(cfg.EmbedRequestsPerHour, defaultEmbedPerHour)),
		tool:  newBudget(orDefault(cfg.ToolRequestsPerHour, defaultToolPerHour)),
	}
}

// Service executes AI requests within budget.
type Service struct {
	cfg     *Config
	budgets *Budgets
	log     *hlog.Logger
}

// NewService builds a Service from config, with a ration of its own. The
// logger receives prompt traces on the prompt topic when they are enabled;
// nil discards them.
func NewService(cfg *Config, log *hlog.Logger) *Service {
	return NewServiceWith(cfg, log, NewBudgets(cfg.Budget))
}

// NewServiceWith is NewService against a ration that may be shared with
// other ghosts. A nil ration means one of its own.
func NewServiceWith(cfg *Config, log *hlog.Logger, budgets *Budgets) *Service {
	// The config switch is an older spelling of "enable the prompt topic",
	// so make it one: otherwise tracing is decided twice, and the second
	// decision — the topic filter on the way out — silently wins.
	if cfg.LogPrompts {
		log = log.With(hlog.Prompt)
	}
	if budgets == nil {
		budgets = NewBudgets(cfg.Budget)
	}
	return &Service{cfg: cfg, budgets: budgets, log: log}
}

// orDefault fills in an unconfigured allowance.
func orDefault(configured, fallback int) int {
	if configured <= 0 {
		return fallback
	}
	return configured
}

// logPrompts reports whether request tracing is on.
func (s *Service) logPrompts() bool {
	return s.log.Enabled(hlog.Prompt, hlog.LevelDebug)
}

// trace emits one prompt trace. Only request content is traced; API keys
// live in headers and are never part of it.
func (s *Service) trace(format string, args ...any) {
	if s.logPrompts() {
		s.log.Debug(hlog.Prompt, format, args...)
	}
}

// traceMessages renders a chat request the way it goes out: the system
// prompt (persona plus journal), each history turn, then the new utterance.
func (s *Service) traceMessages(msgs []Message, opts Options) {
	if !s.logPrompts() {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "chat → %s model=%s", s.cfg.LLM.BaseURL, s.cfg.LLM.Model)
	if temp, ok := s.cfg.LLM.temperature(opts.Silent); ok {
		fmt.Fprintf(&b, " temperature=%g", temp)
	}
	if opts.Silent {
		b.WriteString(" silent")
	}
	if opts.Tag != "" {
		fmt.Fprintf(&b, " tag=%s", opts.Tag)
	}
	if n := len(opts.Tools); n > 0 {
		names := make([]string, 0, n)
		for _, t := range opts.Tools {
			names = append(names, t.Name)
		}
		fmt.Fprintf(&b, " tools=[%s]", strings.Join(names, " "))
	}
	fmt.Fprintf(&b, " (%d messages)", len(msgs))
	for _, m := range msgs {
		fmt.Fprintf(&b, "\n  [%s] %s", m.Role, m.Content)
	}
	s.log.Debug(hlog.Prompt, "%s", b.String())
}

// Options describes one LLM call beyond its messages.
type Options struct {
	// Silent marks a brain-internal call: no balloon (the caller's
	// concern), colder sampling, and labelled as such in traces.
	Silent bool
	// Tag is the brain's label for the request, echoed into traces.
	Tag string
	// Tools are what the model may ask to have done. Empty leaves the
	// field out of the request entirely, which is what every call that is
	// only a conversation wants — and what a backend that has never heard
	// of tool calling requires.
	Tools []Tool
}

// Expression modes.
const (
	ExpressionOff     = "off"
	ExpressionLexicon = "lexicon"
	ExpressionLLM     = "llm"
)

// ExpressionMode is how dialogue should be mapped to faces, defaulting to
// the free tier: a ghost should animate out of the box, but nobody should
// discover a doubled model bill because their ghost has eyebrows.
func (s *Service) ExpressionMode() string {
	switch s.cfg.ExpressionInference {
	case ExpressionOff, ExpressionLexicon, ExpressionLLM:
		return s.cfg.ExpressionInference
	default:
		return ExpressionLexicon
	}
}

// VisionEnabled reports whether a vision model is configured.
func (s *Service) VisionEnabled() bool {
	return s.cfg.Vision.Model != "" && s.visionBaseURL() != ""
}

func (s *Service) visionBaseURL() string {
	if s.cfg.Vision.BaseURL != "" {
		return s.cfg.Vision.BaseURL
	}
	return s.cfg.LLM.BaseURL
}

// FirstTokenTimeout is how long to wait for a streaming reply to begin.
func (s *Service) FirstTokenTimeout() time.Duration {
	if s.cfg.FirstTokenTimeout > 0 {
		return time.Duration(s.cfg.FirstTokenTimeout) * time.Second
	}
	return defaultFirstTokenTimeout
}

// LLMEnabled reports whether the user configured an LLM backend.
func (s *Service) LLMEnabled() bool { return s.cfg.LLM.BaseURL != "" }

// ImageEnabled reports whether the user configured an image backend.
func (s *Service) ImageEnabled() bool { return s.cfg.Image.BaseURL != "" }

// Chat streams a completion for the conversation. onDelta receives text
// fragments as they arrive; the full reply is returned. Budget is charged
// per request, before the call.
func (s *Service) Chat(ctx context.Context, msgs []Message, onDelta func(string), opts Options) (string, error) {
	reply, err := s.ChatWithTools(ctx, msgs, onDelta, opts)
	return reply.Text, err
}

// ChatWithTools is Chat that can also come back asking for something to be
// done. With Options.Tools empty it is exactly Chat, which is why Chat is
// one line: the tool path is not a second implementation of the chat path.
func (s *Service) ChatWithTools(ctx context.Context, msgs []Message, onDelta func(string), opts Options) (Reply, error) {
	if !s.LLMEnabled() {
		return Reply{}, fmt.Errorf("no LLM backend configured (set [ai.llm] base_url in the haunt config)")
	}
	if err := s.budgets.llm.spend(); err != nil {
		return Reply{}, fmt.Errorf("llm budget: %w", err)
	}
	s.traceMessages(msgs, opts)
	reply, err := chatCompletion(ctx, &s.cfg.LLM, msgs, onDelta, opts)
	if err != nil {
		s.trace("chat failed: %v", err)
		return reply, err
	}
	for _, c := range reply.Calls {
		s.trace("wants to run %s(%s)", c.Function.Name, c.Function.Arguments)
	}
	if reply.Text != "" {
		s.trace("reply (%d chars)\n  %s", len(reply.Text), reply.Text)
	}
	return reply, nil
}

// SpendToolCall charges one tool call against the hourly ration. It is here
// rather than beside the call itself because the ration is the user's
// money and their attention, and both are counted in one place.
func (s *Service) SpendToolCall() error {
	if err := s.budgets.tool.spend(); err != nil {
		return fmt.Errorf("tool budget: %w", err)
	}
	return nil
}

// ToolTimeout is how long one tool call may take. Generous — a tool that
// builds something or waits on a network is doing what it was asked — and
// finite, because the ghost holds its one AI slot for the whole exchange
// and a tool that never returns would make it mute.
func (s *Service) ToolTimeout() time.Duration {
	if s.cfg.ToolTimeout > 0 {
		return time.Duration(s.cfg.ToolTimeout) * time.Second
	}
	return defaultToolTimeout
}

// MaxToolCalls caps how many tool calls one exchange may make before the
// model is made to answer with what it has. Without a stop, a model that
// keeps asking for the same tool is an unbounded spend that nobody asked
// for.
func (s *Service) MaxToolCalls() int {
	if s.cfg.MaxToolCalls > 0 {
		return s.cfg.MaxToolCalls
	}
	return defaultMaxToolCalls
}

// ImageCacheKey identifies the image backend configuration for
// content-addressed caching: same key + prompt = reusable result.
func (s *Service) ImageCacheKey() string {
	i := &s.cfg.Image
	return fmt.Sprintf("%s|%s|%s|%s|%d|%g|%s|%s",
		i.Backend, i.BaseURL, i.Model, i.Size, i.Steps, i.CFG, i.Sampler, i.Negative)
}

// GenerateImage produces PNG bytes for a prompt.
func (s *Service) GenerateImage(ctx context.Context, prompt string) ([]byte, error) {
	if !s.ImageEnabled() {
		return nil, fmt.Errorf("no image backend configured (set [ai.image] base_url in the haunt config)")
	}
	if err := s.budgets.image.spend(); err != nil {
		return nil, fmt.Errorf("image budget: %w", err)
	}
	s.traceImage(prompt)
	switch s.cfg.Image.Backend {
	case "", "openai":
		return generateImage(ctx, &s.cfg.Image, prompt)
	case "comfyui":
		return comfyGenerate(ctx, &s.cfg.Image, prompt)
	default:
		return nil, fmt.Errorf("unknown image backend %q (openai or comfyui)", s.cfg.Image.Backend)
	}
}

// traceImage renders an image request with the parameters as resolved,
// since most of them are defaulted rather than configured.
func (s *Service) traceImage(prompt string) {
	if !s.logPrompts() {
		return
	}
	i := &s.cfg.Image
	backend := i.Backend
	if backend == "" {
		backend = "openai"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "image → %s %s model=%s", backend, i.BaseURL, i.Model)
	if backend == "comfyui" {
		w, h := parseSize(i.Size, comfyDefaultSize)
		steps, cfgScale, sampler, negative := comfyDefaults(i)
		fmt.Fprintf(&b, " %dx%d steps=%d cfg=%g sampler=%s\n  [negative] %s",
			w, h, steps, cfgScale, sampler, negative)
	} else if i.Size != "" {
		fmt.Fprintf(&b, " size=%s", i.Size)
	}
	fmt.Fprintf(&b, "\n  [prompt] %s", prompt)
	s.log.Debug(hlog.Prompt, "%s", b.String())
}
