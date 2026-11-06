package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
)

type scriptedOutcomeClient struct {
	resp Response
	err  error
}

func (c scriptedOutcomeClient) Generate(context.Context, Request) (Response, error) {
	return c.resp, c.err
}

func TestInstrumentedClientRecordsCallsTokensAndSessionUsage(t *testing.T) {
	metrics := obs.NewMetrics()
	usage := obs.NewUsageTracker()

	ctx := reqctx.WithSession(context.Background(), "sess-1")
	ctx = reqctx.WithRun(ctx, "run-1")
	ctx = reqctx.WithTurn(ctx, "turn-1")

	client := NewInstrumentedClient(
		scriptedOutcomeClient{resp: Response{Model: "test-model", PromptTokens: 200, CompletionTokens: 80}},
		"test-provider", "test-model", "evaluate", metrics, usage,
	)

	if _, err := client.Generate(ctx, Request{User: "secret answer"}); err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := metrics.ModelCallTotal("test-provider", "test-model", "success"); got != 1 {
		t.Fatalf("success calls = %d", got)
	}
	if got := metrics.TokenTotal("test-provider", "test-model", "input"); got != 200 {
		t.Fatalf("input tokens = %d", got)
	}
	if got := metrics.TokenTotal("test-provider", "test-model", "output"); got != 80 {
		t.Fatalf("output tokens = %d", got)
	}

	snap := usage.Snapshot("sess-1")
	if snap.ModelCalls != 1 || snap.PromptTokens != 200 || snap.CompletionTokens != 80 || snap.TotalTokens != 280 {
		t.Fatalf("usage = %+v", snap)
	}
	if snap.ModelCallBreakdown["evaluate"] != 1 {
		t.Fatalf("breakdown = %+v", snap.ModelCallBreakdown)
	}
}

func TestInstrumentedClientCountsErrorsWithoutTokens(t *testing.T) {
	metrics := obs.NewMetrics()
	usage := obs.NewUsageTracker()
	ctx := reqctx.WithSession(context.Background(), "sess-1")

	client := NewInstrumentedClient(
		scriptedOutcomeClient{err: &CallError{Class: FailureTimeout, Retryable: true, Attempts: 1, Err: errors.New("timeout")}},
		"test-provider", "test-model", "question", metrics, usage,
	)

	if _, err := client.Generate(ctx, Request{}); err == nil {
		t.Fatal("expected error")
	}
	if got := metrics.ModelCallTotal("test-provider", "test-model", "error"); got != 1 {
		t.Fatalf("error calls = %d", got)
	}
	if got := metrics.TokenTotal("test-provider", "test-model", "input"); got != 0 {
		t.Fatalf("failed call must not record tokens: %d", got)
	}
	if snap := usage.Snapshot("sess-1"); snap.ModelCalls != 0 {
		t.Fatalf("failed call must not record usage: %+v", snap)
	}
}
