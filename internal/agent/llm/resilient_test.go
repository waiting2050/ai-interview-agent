package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"
)

type scriptedClient struct {
	calls int
	fn    func(attempt int) (Response, error)
}

func (s *scriptedClient) Generate(context.Context, Request) (Response, error) {
	s.calls++
	return s.fn(s.calls)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestResilientClientRetriesRetryableErrors(t *testing.T) {
	inner := &scriptedClient{fn: func(attempt int) (Response, error) {
		if attempt < 3 {
			return Response{}, fmt.Errorf("status 503: unavailable")
		}
		return Response{Text: "ok", PromptTokens: 10, CompletionTokens: 20}, nil
	}}
	client := NewResilientClient(inner, time.Second, 2, discardLogger())
	client.baseBackoff = time.Millisecond

	resp, err := client.Generate(context.Background(), Request{})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if resp.Text != "ok" {
		t.Fatalf("response = %+v", resp)
	}
	stats := client.Stats()
	if stats.Calls != 1 || stats.Retries != 2 || stats.Failures != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.PromptTokens != 10 || stats.CompletionTokens != 20 {
		t.Fatalf("tokens = %+v", stats)
	}
}

func TestResilientClientDoesNotRetryAuth(t *testing.T) {
	inner := &scriptedClient{fn: func(int) (Response, error) {
		return Response{}, fmt.Errorf("status 401: unauthorized")
	}}
	client := NewResilientClient(inner, time.Second, 3, discardLogger())
	client.baseBackoff = time.Millisecond

	_, err := client.Generate(context.Background(), Request{})
	if err == nil {
		t.Fatal("expected error")
	}
	var callErr *CallError
	if !errors.As(err, &callErr) {
		t.Fatalf("error type = %T", err)
	}
	if callErr.Class != FailureAuth || callErr.Retryable || callErr.Attempts != 1 {
		t.Fatalf("call error = %+v", callErr)
	}
	if inner.calls != 1 {
		t.Fatalf("calls = %d", inner.calls)
	}
}

func TestResilientClientTimesOutEachAttempt(t *testing.T) {
	inner := &scriptedClient{fn: func(int) (Response, error) {
		return Response{}, context.DeadlineExceeded
	}}
	client := NewResilientClient(inner, time.Second, 0, discardLogger())

	_, err := client.Generate(context.Background(), Request{})
	var callErr *CallError
	if !errors.As(err, &callErr) || callErr.Class != FailureTimeout {
		t.Fatalf("expected timeout, got %v", err)
	}
}

type blockingClient struct{}

func (blockingClient) Generate(ctx context.Context, _ Request) (Response, error) {
	<-ctx.Done()
	return Response{}, ctx.Err()
}

func TestResilientClientEnforcesAttemptTimeout(t *testing.T) {
	client := NewResilientClient(blockingClient{}, 20*time.Millisecond, 0, discardLogger())

	start := time.Now()
	_, err := client.Generate(context.Background(), Request{})
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("attempt timeout not enforced, elapsed=%s", elapsed)
	}
	var callErr *CallError
	if !errors.As(err, &callErr) || callErr.Class != FailureTimeout {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		err       error
		class     FailureClass
		retryable bool
	}{
		{fmt.Errorf("status code: 429"), FailureRateLimited, true},
		{fmt.Errorf("status 500"), FailureUnavailable, true},
		{fmt.Errorf("status 403"), FailureAuth, false},
		{fmt.Errorf("status 422"), FailureBadRequest, false},
		{fmt.Errorf("connection reset by peer"), FailureNetwork, true},
		{context.Canceled, FailureCanceled, false},
		{errors.New("boom"), FailureUnknown, false},
	}
	for _, tt := range tests {
		class, _, retryable := Classify(tt.err)
		if class != tt.class || retryable != tt.retryable {
			t.Fatalf("%v => %s/%v, want %s/%v", tt.err, class, retryable, tt.class, tt.retryable)
		}
	}
}
