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

	"github.com/BurntSushi/toml"

	"github.com/aaronjauregui/haunt/internal/quiet"
)

// Config is the [ai] portion of the user config. The *user* chooses
// backends; ghosts only declare that they want the capability.
type Config struct {
	LLM    LLMConfig    `toml:"llm"`
	Image  ImageConfig  `toml:"image"`
	Budget BudgetConfig `toml:"budget"`
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
}

const (
	defaultLLMPerHour   = 120
	defaultImagePerHour = 12
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
}

// NewService builds a Service from config.
func NewService(cfg *Config) *Service {
	return &Service{
		cfg:         cfg,
		llmBudget:   newBudget(cfg.Budget.LLMRequestsPerHour),
		imageBudget: newBudget(cfg.Budget.ImageRequestsPerHour),
	}
}

// LLMEnabled reports whether the user configured an LLM backend.
func (s *Service) LLMEnabled() bool { return s.cfg.LLM.BaseURL != "" }

// ImageEnabled reports whether the user configured an image backend.
func (s *Service) ImageEnabled() bool { return s.cfg.Image.BaseURL != "" }

// Chat streams a completion for the conversation. onDelta receives text
// fragments as they arrive; the full reply is returned. Budget is charged
// per request, before the call.
func (s *Service) Chat(ctx context.Context, msgs []Message, onDelta func(string)) (string, error) {
	if !s.LLMEnabled() {
		return "", fmt.Errorf("no LLM backend configured (set [ai.llm] base_url in the haunt config)")
	}
	if err := s.llmBudget.spend(); err != nil {
		return "", fmt.Errorf("llm budget: %w", err)
	}
	return chatCompletion(ctx, &s.cfg.LLM, msgs, onDelta)
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
	switch s.cfg.Image.Backend {
	case "", "openai":
		return generateImage(ctx, &s.cfg.Image, prompt)
	case "comfyui":
		return comfyGenerate(ctx, &s.cfg.Image, prompt)
	default:
		return nil, fmt.Errorf("unknown image backend %q (openai or comfyui)", s.cfg.Image.Backend)
	}
}
