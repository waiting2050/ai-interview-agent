package llm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	openai "github.com/cloudwego/eino-ext/components/model/openai"
)

// Provider names exposed by MODEL_PROVIDER.
const (
	ProviderHeuristic = "heuristic"
	ProviderOpenAI    = "openai"
)

// Environment variables read by LoadProviderConfig. Base URL, model and API
// key all come from the environment; nothing is hard-coded or committed.
const (
	EnvProvider     = "MODEL_PROVIDER"
	EnvOpenAIBase   = "OPENAI_BASE_URL"
	EnvOpenAIKey    = "OPENAI_API_KEY"
	EnvOpenAIModel  = "OPENAI_MODEL"
	EnvModelTimeout = "MODEL_TIMEOUT"
	EnvMaxRetries   = "MODEL_MAX_RETRIES"
)

// Defaults target DeepSeek's OpenAI-compatible endpoint. DeepSeek retired
// deepseek-chat / deepseek-reasoner on 2026-07-24; deepseek-flash is the
// current model id (the legacy deepseek-v4-flash alias still resolves but
// points at the same V4.1-Flash model). OpenAI or other compatible services
// must set OPENAI_BASE_URL and OPENAI_MODEL explicitly.
const (
	defaultOpenAIModel = "deepseek-flash"
	defaultOpenAIBase  = "https://api.deepseek.com/v1"
)

// ProviderConfig is the resolved model selection. APIKey is never logged.
type ProviderConfig struct {
	Provider   string
	BaseURL    string
	APIKey     string
	Model      string
	Timeout    time.Duration
	MaxRetries int
}

// ProviderInfo describes the client that was actually built, for startup logs
// and the /health payload.
type ProviderInfo struct {
	Provider string
	Model    string
	BaseURL  string
	Fallback bool
	Reason   string
}

// LoadProviderConfig reads model settings from the environment. An unset
// MODEL_PROVIDER with a present OPENAI_API_KEY means openai; otherwise the
// deterministic heuristic model is selected so demos and tests need no key.
func LoadProviderConfig() (ProviderConfig, error) {
	cfg := ProviderConfig{
		Provider:   strings.ToLower(strings.TrimSpace(os.Getenv(EnvProvider))),
		BaseURL:    strings.TrimSpace(os.Getenv(EnvOpenAIBase)),
		APIKey:     strings.TrimSpace(os.Getenv(EnvOpenAIKey)),
		Model:      strings.TrimSpace(os.Getenv(EnvOpenAIModel)),
		Timeout:    30 * time.Second,
		MaxRetries: 2,
	}
	if cfg.Model == "" {
		cfg.Model = defaultOpenAIModel
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultOpenAIBase
	}
	if cfg.Provider == "" {
		if cfg.APIKey != "" {
			cfg.Provider = ProviderOpenAI
		} else {
			cfg.Provider = ProviderHeuristic
		}
	}

	if raw := strings.TrimSpace(os.Getenv(EnvModelTimeout)); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return ProviderConfig{}, fmt.Errorf("%s must be a duration such as 30s: %w", EnvModelTimeout, err)
		}
		cfg.Timeout = d
	}
	if raw := strings.TrimSpace(os.Getenv(EnvMaxRetries)); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 5 {
			return ProviderConfig{}, fmt.Errorf("%s must be an integer in [0,5], got %q", EnvMaxRetries, raw)
		}
		cfg.MaxRetries = n
	}
	return cfg, nil
}

// NewClientFromEnv assembles the model client. When a real provider is
// configured but unusable (for example a missing key), it degrades to fallback
// instead of failing startup; the returned ProviderInfo records the reason.
func NewClientFromEnv(ctx context.Context, fallback Client, log *slog.Logger) (Client, ProviderInfo, error) {
	cfg, err := LoadProviderConfig()
	if err != nil {
		return nil, ProviderInfo{}, err
	}
	return NewClient(ctx, cfg, fallback, log)
}

// NewClient builds the provider client according to cfg. fallback must be
// non-nil when the provider is heuristic or degrades.
func NewClient(ctx context.Context, cfg ProviderConfig, fallback Client, log *slog.Logger) (Client, ProviderInfo, error) {
	switch cfg.Provider {
	case ProviderHeuristic:
		if fallback == nil {
			return nil, ProviderInfo{}, fmt.Errorf("heuristic provider requires a fallback client")
		}
		return fallback, ProviderInfo{Provider: ProviderHeuristic, Model: "fake-heuristic"}, nil

	case ProviderOpenAI:
		if cfg.APIKey == "" {
			if fallback == nil {
				return nil, ProviderInfo{}, fmt.Errorf("openai provider requires %s", EnvOpenAIKey)
			}
			return fallback, ProviderInfo{
				Provider: ProviderOpenAI,
				Model:    "fake-heuristic",
				BaseURL:  cfg.BaseURL,
				Fallback: true,
				Reason:   EnvOpenAIKey + " is empty, using deterministic heuristic model",
			}, nil
		}

		model, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			Model:   cfg.Model,
			Timeout: cfg.Timeout,
			// Every prompt in this service asks for a single JSON object, so
			// constrain the provider rather than relying on prompt text alone.
			ResponseFormat: &openai.ChatCompletionResponseFormat{
				Type: openai.ChatCompletionResponseFormatTypeJSONObject,
			},
		})
		if err != nil {
			return nil, ProviderInfo{}, fmt.Errorf("create openai chat model: %w", err)
		}

		inner := NewEinoClient(model, "openai/"+cfg.Model)
		client := NewResilientClient(inner, cfg.Timeout, cfg.MaxRetries, log)
		return client, ProviderInfo{
			Provider: ProviderOpenAI,
			Model:    cfg.Model,
			BaseURL:  cfg.BaseURL,
		}, nil

	default:
		return nil, ProviderInfo{}, fmt.Errorf("unsupported %s %q (want heuristic or openai)", EnvProvider, cfg.Provider)
	}
}
