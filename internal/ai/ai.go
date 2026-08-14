// Package ai is the driver-owned AI service layer. Brains never hold API
// keys or talk to backends; they emit intents, the driver calls the
// user-configured backend within budget, and results come back as events.
// Local-first: any OpenAI-compatible endpoint works (Ollama, llama.cpp
// server, vLLM, cloud providers).
package ai

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/aaronjauregui/haunt/internal/quiet"
)

// Config is the [ai] portion of the user config. The *user* chooses
// backends; ghosts only declare that they want the capability.
type Config struct {
	LLM    LLMConfig    `toml:"llm"`
	Image  ImageConfig  `toml:"image"`
	Embed  EmbedConfig  `toml:"embed"`
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

// BudgetConfig caps how fast ghosts may burn backend calls. Zero values get
// defaults; the driver enforces these, not the ghost.
type BudgetConfig struct {
	LLMRequestsPerHour   int `toml:"llm_requests_per_hour"`
	ImageRequestsPerHour int `toml:"image_requests_per_hour"`
	// EmbedRequestsPerHour is separate and generous: memory work rides
	// along with dialogue (a recall on the way in, a write on the way out)
	// and must not consume the conversation's own allowance.
	EmbedRequestsPerHour int `toml:"embed_requests_per_hour"`
}

const (
	defaultLLMPerHour   = 120
	defaultImagePerHour = 12
	defaultEmbedPerHour = 600
)

// LoadConfig reads the [ai] section of the user config file.
func LoadConfig() (*Config, error) {
	path, err := quiet.ConfigPath()
	if err != nil {
		return nil, err
	}
	var root struct {
		AI Config `toml:"ai"`
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := toml.Unmarshal(data, &root); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
	}
	cfg := root.AI
	if cfg.Budget.LLMRequestsPerHour == 0 {
		cfg.Budget.LLMRequestsPerHour = defaultLLMPerHour
	}
	if cfg.Budget.ImageRequestsPerHour == 0 {
		cfg.Budget.ImageRequestsPerHour = defaultImagePerHour
	}
	if cfg.Budget.EmbedRequestsPerHour == 0 {
		cfg.Budget.EmbedRequestsPerHour = defaultEmbedPerHour
	}
	return &cfg, nil
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

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"` // system, user, assistant
	Content string `json:"content"`
}

// Service executes AI requests within budget.
type Service struct {
	cfg         *Config
	llmBudget   *budget
	imageBudget *budget
	embedBudget *budget
	log         func(string)
}

// NewService builds a Service from config. log receives prompt traces when
// they are enabled; nil discards them.
func NewService(cfg *Config, log func(string)) *Service {
	return &Service{
		cfg: cfg,
		// Defaulted here as well as in LoadConfig, so a Config built
		// directly — tests, embedders — gets a working allowance rather
		// than one that refuses everything.
		llmBudget:   newBudget(orDefault(cfg.Budget.LLMRequestsPerHour, defaultLLMPerHour)),
		imageBudget: newBudget(orDefault(cfg.Budget.ImageRequestsPerHour, defaultImagePerHour)),
		embedBudget: newBudget(orDefault(cfg.Budget.EmbedRequestsPerHour, defaultEmbedPerHour)),
		log:         log,
	}
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
	return s.log != nil && (s.cfg.LogPrompts || os.Getenv("HAUNT_AI_DEBUG") == "1")
}

// trace emits one prompt trace. Only request content is traced; API keys
// live in headers and are never part of it.
func (s *Service) trace(format string, args ...any) {
	if s.logPrompts() {
		s.log("haunt: ai: " + fmt.Sprintf(format, args...))
	}
}

// traceMessages renders a chat request the way it goes out: the system
// prompt (persona plus journal), each history turn, then the new utterance.
func (s *Service) traceMessages(msgs []Message, opts Options) {
	if !s.logPrompts() {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "haunt: ai: chat → %s model=%s", s.cfg.LLM.BaseURL, s.cfg.LLM.Model)
	if temp, ok := s.cfg.LLM.temperature(opts.Silent); ok {
		fmt.Fprintf(&b, " temperature=%g", temp)
	}
	if opts.Silent {
		b.WriteString(" silent")
	}
	if opts.Tag != "" {
		fmt.Fprintf(&b, " tag=%s", opts.Tag)
	}
	fmt.Fprintf(&b, " (%d messages)", len(msgs))
	for _, m := range msgs {
		fmt.Fprintf(&b, "\n  [%s] %s", m.Role, m.Content)
	}
	s.log(b.String())
}

// Options describes one LLM call beyond its messages.
type Options struct {
	// Silent marks a brain-internal call: no balloon (the caller's
	// concern), colder sampling, and labelled as such in traces.
	Silent bool
	// Tag is the brain's label for the request, echoed into traces.
	Tag string
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

// LLMEnabled reports whether the user configured an LLM backend.
func (s *Service) LLMEnabled() bool { return s.cfg.LLM.BaseURL != "" }

// ImageEnabled reports whether the user configured an image backend.
func (s *Service) ImageEnabled() bool { return s.cfg.Image.BaseURL != "" }

// Chat streams a completion for the conversation. onDelta receives text
// fragments as they arrive; the full reply is returned. Budget is charged
// per request, before the call.
func (s *Service) Chat(ctx context.Context, msgs []Message, onDelta func(string), opts Options) (string, error) {
	if !s.LLMEnabled() {
		return "", fmt.Errorf("no LLM backend configured (set [ai.llm] base_url in the haunt config)")
	}
	if err := s.llmBudget.spend(); err != nil {
		return "", fmt.Errorf("llm budget: %w", err)
	}
	s.traceMessages(msgs, opts)
	full, err := chatCompletion(ctx, &s.cfg.LLM, msgs, onDelta, opts)
	if err != nil {
		s.trace("chat failed: %v", err)
		return full, err
	}
	s.trace("reply (%d chars)\n  %s", len(full), full)
	return full, nil
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
	if err := s.imageBudget.spend(); err != nil {
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
	fmt.Fprintf(&b, "haunt: ai: image → %s %s model=%s", backend, i.BaseURL, i.Model)
	if backend == "comfyui" {
		w, h := parseSize(i.Size, comfyDefaultSize)
		steps, cfgScale, sampler, negative := comfyDefaults(i)
		fmt.Fprintf(&b, " %dx%d steps=%d cfg=%g sampler=%s\n  [negative] %s",
			w, h, steps, cfgScale, sampler, negative)
	} else if i.Size != "" {
		fmt.Fprintf(&b, " size=%s", i.Size)
	}
	fmt.Fprintf(&b, "\n  [prompt] %s", prompt)
	s.log(b.String())
}
