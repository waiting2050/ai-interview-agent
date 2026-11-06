package llm

import (
	"context"
	"testing"
	"time"
)

func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{EnvProvider, EnvOpenAIBase, EnvOpenAIKey, EnvOpenAIModel, EnvModelTimeout, EnvMaxRetries} {
		t.Setenv(key, "")
	}
}

func TestLoadProviderConfigWithoutKeyIsHeuristic(t *testing.T) {
	clearProviderEnv(t)
	cfg, err := LoadProviderConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Provider != ProviderHeuristic {
		t.Fatalf("provider = %s", cfg.Provider)
	}
	if cfg.Model != defaultOpenAIModel {
		t.Fatalf("model default = %s", cfg.Model)
	}
}

func TestLoadProviderConfigUsesEnv(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv(EnvProvider, "openai")
	t.Setenv(EnvOpenAIKey, "test-key")
	t.Setenv(EnvOpenAIBase, "http://127.0.0.1:9999/v1")
	t.Setenv(EnvOpenAIModel, "custom-model")
	t.Setenv(EnvModelTimeout, "5s")
	t.Setenv(EnvMaxRetries, "1")

	cfg, err := LoadProviderConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Provider != ProviderOpenAI || cfg.APIKey != "test-key" ||
		cfg.BaseURL != "http://127.0.0.1:9999/v1" || cfg.Model != "custom-model" ||
		cfg.Timeout != 5*time.Second || cfg.MaxRetries != 1 {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestNewClientOpenAIWithoutKeyFallsBack(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv(EnvProvider, "openai")

	fallback := NewEinoClient(Scripted("fake", ""), "fake-heuristic")
	client, info, err := NewClientFromEnv(context.Background(), fallback, discardLogger())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if client != fallback {
		t.Fatal("expected fallback client")
	}
	if !info.Fallback || info.Model != "fake-heuristic" {
		t.Fatalf("info = %+v", info)
	}
}

func TestLoadProviderConfigRejectsBadValues(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv(EnvModelTimeout, "thirty")
	if _, err := LoadProviderConfig(); err == nil {
		t.Fatal("expected timeout parse error")
	}

	t.Setenv(EnvModelTimeout, "")
	t.Setenv(EnvMaxRetries, "9")
	if _, err := LoadProviderConfig(); err == nil {
		t.Fatal("expected retries range error")
	}
}

func TestNewClientRejectsUnknownProvider(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv(EnvProvider, "unknown")
	fallback := NewEinoClient(Scripted("fake", ""), "fake-heuristic")
	if _, _, err := NewClientFromEnv(context.Background(), fallback, discardLogger()); err == nil {
		t.Fatal("expected unknown provider error")
	}
}
