package llm

import (
	"context"
	"log/slog"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
)

// InstrumentedClient records model metrics, per-session usage and one
// structured log line per call. Purpose is evaluate, question or report.
type InstrumentedClient struct {
	inner    Client
	provider string
	model    string
	purpose  string
	metrics  *obs.Metrics
	usage    *obs.UsageTracker
}

// NewInstrumentedClient wraps a client without changing its behaviour.
func NewInstrumentedClient(inner Client, provider, model, purpose string, metrics *obs.Metrics, usage *obs.UsageTracker) *InstrumentedClient {
	if metrics == nil {
		metrics = obs.NewMetrics()
	}
	if usage == nil {
		usage = obs.NewUsageTracker()
	}
	return &InstrumentedClient{
		inner:    inner,
		provider: provider,
		model:    model,
		purpose:  purpose,
		metrics:  metrics,
		usage:    usage,
	}
}

// Generate implements Client.
func (c *InstrumentedClient) Generate(ctx context.Context, req Request) (Response, error) {
	start := time.Now()
	resp, err := c.inner.Generate(ctx, req)
	latency := time.Since(start)

	result := "success"
	class := ""
	if err != nil {
		result = "error"
		if fc, _, _ := Classify(err); fc != "" {
			class = string(fc)
		} else {
			class = string(FailureUnknown)
		}
	}

	c.metrics.IncModelCall(c.provider, c.model, result)
	if err == nil {
		c.metrics.AddModelTokens(c.provider, c.model, resp.PromptTokens, resp.CompletionTokens)
		c.usage.Record(reqctx.SessionID(ctx), c.purpose, resp.PromptTokens, resp.CompletionTokens)
	}

	args := append(reqctx.Attrs(ctx),
		"provider", c.provider,
		"model", c.model,
		"purpose", c.purpose,
		"result", result,
		"latency_ms", latency.Milliseconds(),
	)
	if class != "" {
		args = append(args, "error_class", class)
	}
	if err == nil {
		args = append(args,
			"prompt_tokens", resp.PromptTokens,
			"completion_tokens", resp.CompletionTokens,
		)
	}
	slog.Default().Info("model call", args...)

	return resp, err
}
