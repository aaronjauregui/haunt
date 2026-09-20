// Package backend is the driver-owned backend layer for the verbs the driver
// performs itself: drawing a picture and looking at the screen, plus the
// hourly ration on tool calls. Brains never hold API keys or talk to
// backends; the driver calls the user-configured backend within budget.
// Conversation is hermes's (internal/hermes). Local-first: any
// OpenAI-compatible endpoint works (Ollama, llama.cpp server, vLLM, cloud
// providers), and ComfyUI for images.
package backend

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// Config is the [backend] portion of the user config. The *user* chooses
// backends; ghosts only declare that they want the capability.
//
// Conversation is not here: a ghost talks through hermes, which owns the
// model, its key and its bill ([hermes] in the config). What is left are the
// driver's own verbs that need a backend — drawing and looking — and the
// ration on acting through tools.
type Config struct {
	Image  ImageConfig  `toml:"image"`
	Vision VisionConfig `toml:"vision"`
	Search SearchConfig `toml:"search"`
	Budget BudgetConfig `toml:"budget"`

	// ToolTimeout is how long one tool call may take, in seconds (default
	// 120).
	ToolTimeout int `toml:"tool_timeout"`

	// LogPrompts dumps every request this layer sends — the vision question,
	// the resolved image parameters — to the driver log. Off by default and
	// never in session recordings. HAUNT_BACKEND_DEBUG=1 turns it on without
	// editing the config.
	LogPrompts bool `toml:"log_prompts"`
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
	// Workflow is a path to a ComfyUI graph exported with "Save (API
	// format)", with {{prompt}} and friends where the varying parts go. It
	// replaces the built-in graph, and with it Model, Steps, CFG and
	// Sampler: the workflow owns them. ComfyUI only.
	Workflow string `toml:"workflow"`
}

// SearchConfig points at a web search provider. Empty disables search
// regardless of what a ghost asks for, the way VisionConfig does for
// looking: the capability says a ghost may search, this says there is
// anything to search with.
type SearchConfig struct {
	// Backend selects the provider: "searxng" (a SearXNG instance, which
	// needs no key and can be the person's own) or "brave" (the Brave
	// Search API, which has a fixed URL and needs a key).
	Backend string `toml:"backend"`
	// BaseURL is the SearXNG instance. Brave ignores it.
	BaseURL string `toml:"base_url"`
	// APIKeyEnv names the environment variable holding the key. Brave only.
	APIKeyEnv string `toml:"api_key_env"`
	// Results is how many results a search returns, 1 to 10.
	Results int `toml:"results"`
	// SafeSearch is "off", "moderate" (default) or "strict".
	SafeSearch string `toml:"safesearch"`
	// Unprompted says when a ghost may search on a turn the person did not
	// start: "never", "skills" (default) or "any". The driver enforces it
	// by the turn's event kind, since it cannot see which skill the model
	// loaded.
	Unprompted string `toml:"unprompted"`
}

func (c *SearchConfig) apiKey() string {
	if c.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(c.APIKeyEnv)
}

// VisionConfig points at a model that can be shown a picture. Empty
// disables looking at the screen regardless of what a ghost asks for: the
// capability says a ghost may look, this says there is anything to look
// with.
type VisionConfig struct {
	// BaseURL is an OpenAI-compatible endpoint, like
	// "http://localhost:11434/v1". Required: there is no dialogue backend
	// to fall back on any more.
	BaseURL string `toml:"base_url"`
	// Model must be vision-capable. Screen understanding is a task where
	// the difference between a frontier model and a small local one is
	// stark, so this is configured separately from the ghost's own model.
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
	ImageRequestsPerHour int `toml:"image_requests_per_hour"`
	// VisionRequestsPerHour caps looks at the screen. A look costs about
	// what a reply costs, so it is rationed like one.
	VisionRequestsPerHour int `toml:"vision_requests_per_hour"`
	// ToolRequestsPerHour caps how fast a ghost may *act*: a tool call
	// changes the world, and one turn can contain several.
	ToolRequestsPerHour int `toml:"tool_requests_per_hour"`
	// SearchRequestsPerHour caps searches and page reads together. Both
	// leave the machine and both cost the provider something.
	SearchRequestsPerHour int `toml:"search_requests_per_hour"`
	// UnpromptedSearchPerHour is the slice of that ration a ghost may
	// spend on turns the person did not start. Looking things up while
	// nobody is watching is the part worth keeping small.
	UnpromptedSearchPerHour int `toml:"unprompted_search_per_hour"`
}

const (
	defaultImagePerHour  = 12
	defaultVisionPerHour = 60
	// defaultToolPerHour is a working afternoon's worth of doing things
	// and nothing like enough for a loop.
	defaultToolPerHour = 60
	// defaultSearchPerHour is enough to follow a conversation that keeps
	// needing a fact, and not enough to crawl.
	defaultSearchPerHour = 30
	// defaultUnpromptedSearchPerHour is deliberately a handful: a ghost
	// looking things up unwatched should be an occasional touch, not a
	// habit.
	defaultUnpromptedSearchPerHour = 4

	// defaultToolTimeout is long enough for a build or a slow network call
	// and short enough that a wedged server is noticed the same afternoon.
	defaultToolTimeout = 2 * time.Minute
)

// WithDefaults returns cfg with the budget defaults filled in. The [backend]
// section is read for it by internal/config, which owns the file; this is
// only the part that knows what an absent budget should mean.
func WithDefaults(cfg Config) *Config {
	if cfg.Budget.ImageRequestsPerHour == 0 {
		cfg.Budget.ImageRequestsPerHour = defaultImagePerHour
	}
	if cfg.Budget.VisionRequestsPerHour == 0 {
		cfg.Budget.VisionRequestsPerHour = defaultVisionPerHour
	}
	if cfg.Budget.ToolRequestsPerHour == 0 {
		cfg.Budget.ToolRequestsPerHour = defaultToolPerHour
	}
	if cfg.Budget.SearchRequestsPerHour == 0 {
		cfg.Budget.SearchRequestsPerHour = defaultSearchPerHour
	}
	if cfg.Budget.UnpromptedSearchPerHour == 0 {
		cfg.Budget.UnpromptedSearchPerHour = defaultUnpromptedSearchPerHour
	}
	return &cfg
}

func (c *ImageConfig) apiKey() string {
	if c.APIKeyEnv == "" {
		return ""
	}
	return os.Getenv(c.APIKeyEnv)
}

// Budgets is the hourly ration a Service spends against. It is a value of
// its own rather than fields inside Service because it is the one part of a
// Service that several ghosts may need to *share*: the rate limits are the
// user's money, and three resident ghosts each with a private 12 pictures
// an hour is 36 an hour. Ghosts running alone get their own, which is the
// same thing seen from one side.
type Budgets struct {
	image  *budget
	vision *budget
	tool   *budget
	// search rations search and page reads together; unprompted is the
	// part of it that turns the person did not start may spend, and such a
	// call spends both.
	search     *budget
	unprompted *budget
}

// NewBudgets builds an hourly ration from config. Defaults are filled in
// here as well as in WithDefaults, so a Config built directly — tests,
// embedders — gets a working allowance rather than one that refuses
// everything.
func NewBudgets(cfg BudgetConfig) *Budgets {
	return &Budgets{
		image:  newBudget(orDefault(cfg.ImageRequestsPerHour, defaultImagePerHour)),
		vision: newBudget(orDefault(cfg.VisionRequestsPerHour, defaultVisionPerHour)),
		tool:   newBudget(orDefault(cfg.ToolRequestsPerHour, defaultToolPerHour)),
		search: newBudget(orDefault(cfg.SearchRequestsPerHour, defaultSearchPerHour)),
		unprompted: newBudget(orDefault(cfg.UnpromptedSearchPerHour,
			defaultUnpromptedSearchPerHour)),
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
	return NewServiceWith(cfg, log, nil)
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

// VisionEnabled reports whether a vision model is configured.
func (s *Service) VisionEnabled() bool {
	return s.cfg.Vision.Model != "" && s.cfg.Vision.BaseURL != ""
}

// ImageEnabled reports whether the user configured an image backend.
func (s *Service) ImageEnabled() bool { return s.cfg.Image.BaseURL != "" }

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
// finite, because a tool that never returns holds the turn that asked for
// it.
func (s *Service) ToolTimeout() time.Duration {
	if s.cfg.ToolTimeout > 0 {
		return time.Duration(s.cfg.ToolTimeout) * time.Second
	}
	return defaultToolTimeout
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
		return nil, fmt.Errorf("no image backend configured (set [backend.image] base_url in the haunt config)")
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
