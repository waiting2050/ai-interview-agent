package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// FailureClass is a stable taxonomy persisted in logs and used by retry
// decisions. Callers must not parse provider error strings themselves.
type FailureClass string

const (
	FailureTimeout     FailureClass = "timeout"
	FailureRateLimited FailureClass = "rate_limited"
	FailureUnavailable FailureClass = "unavailable"
	FailureNetwork     FailureClass = "network"
	FailureAuth        FailureClass = "auth"
	FailureBadRequest  FailureClass = "bad_request"
	FailureCanceled    FailureClass = "canceled"
	FailureUnknown     FailureClass = "unknown"
)

// CallError wraps a provider error with its class so retries and fallbacks are
// explainable.
type CallError struct {
	Class      FailureClass
	StatusCode int
	Retryable  bool
	Attempts   int
	Err        error
}

func (e *CallError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("model call failed: class=%s status=%d attempts=%d: %v",
			e.Class, e.StatusCode, e.Attempts, e.Err)
	}
	return fmt.Sprintf("model call failed: class=%s attempts=%d: %v", e.Class, e.Attempts, e.Err)
}

func (e *CallError) Unwrap() error { return e.Err }

// Stats is a snapshot of one client's counters since process start.
type Stats struct {
	Calls            int64
	Retries          int64
	Failures         int64
	PromptTokens     int64
	CompletionTokens int64
}

type statsCounters struct {
	calls            atomic.Int64
	retries          atomic.Int64
	failures         atomic.Int64
	promptTokens     atomic.Int64
	completionTokens atomic.Int64
}

// ResilientClient adds a per-attempt timeout, bounded retries with backoff,
// token accounting and failure classification around any Client.
type ResilientClient struct {
	inner       Client
	timeout     time.Duration
	maxRetries  int
	baseBackoff time.Duration
	log         *slog.Logger

	stats statsCounters
}

var _ Client = (*ResilientClient)(nil)

// NewResilientClient wraps inner. maxRetries is the number of retries after the
// first attempt, so 2 means at most 3 calls.
func NewResilientClient(inner Client, timeout time.Duration, maxRetries int, log *slog.Logger) *ResilientClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	if maxRetries > 5 {
		maxRetries = 5
	}
	if log == nil {
		log = slog.Default()
	}
	return &ResilientClient{
		inner:       inner,
		timeout:     timeout,
		maxRetries:  maxRetries,
		baseBackoff: 300 * time.Millisecond,
		log:         log,
	}
}

func (c *ResilientClient) Generate(ctx context.Context, req Request) (Response, error) {
	var lastErr error
	var lastClass FailureClass

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			c.stats.retries.Add(1)
			delay := c.backoff(attempt)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				c.stats.failures.Add(1)
				return Response{}, &CallError{Class: FailureCanceled, Retryable: false, Attempts: attempt, Err: ctx.Err()}
			case <-timer.C:
			}
		}

		callCtx, cancel := context.WithTimeout(ctx, c.timeout)
		resp, err := c.inner.Generate(callCtx, req)
		cancel()
		if err == nil {
			c.stats.calls.Add(1)
			c.stats.promptTokens.Add(int64(resp.PromptTokens))
			c.stats.completionTokens.Add(int64(resp.CompletionTokens))
			return resp, nil
		}

		lastErr = err
		class, status, retryable := Classify(err)
		lastClass = class
		if !retryable || attempt == c.maxRetries {
			attempts := attempt + 1
			c.stats.failures.Add(1)
			c.log.Warn("model call failed",
				"class", string(class), "status", status, "attempts", attempts, "error", err)
			return Response{}, &CallError{
				Class:      class,
				StatusCode: status,
				Retryable:  retryable,
				Attempts:   attempts,
				Err:        err,
			}
		}
		c.log.Warn("model call retrying",
			"class", string(class), "status", status, "attempt", attempt+1, "error", err)
	}

	c.stats.failures.Add(1)
	return Response{}, &CallError{Class: lastClass, Retryable: false, Err: lastErr}
}

func (c *ResilientClient) Stats() Stats {
	return Stats{
		Calls:            c.stats.calls.Load(),
		Retries:          c.stats.retries.Load(),
		Failures:         c.stats.failures.Load(),
		PromptTokens:     c.stats.promptTokens.Load(),
		CompletionTokens: c.stats.completionTokens.Load(),
	}
}

func (c *ResilientClient) backoff(attempt int) time.Duration {
	delay := c.baseBackoff << (attempt - 1)
	if delay > 2*time.Second {
		delay = 2 * time.Second
	}
	return delay
}

var statusPattern = regexp.MustCompile(`(?i)(?:status(?:\s*code)?|http(?:\s*status)?)\s*[:= ]?\s*([1-5]\d\d)`)

// Classify maps an error to a failure class, HTTP status (when present) and
// whether a retry may succeed.
func Classify(err error) (FailureClass, int, bool) {
	if err == nil {
		return FailureUnknown, 0, false
	}
	if errors.Is(err, context.Canceled) {
		return FailureCanceled, 0, false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return FailureTimeout, 0, true
	}
	var timeoutErr interface{ Timeout() bool }
	if errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
		return FailureTimeout, 0, true
	}

	text := strings.ToLower(err.Error())
	if match := statusPattern.FindStringSubmatch(text); len(match) == 2 {
		status, _ := strconv.Atoi(match[1])
		class, retryable := classForStatus(status)
		return class, status, retryable
	}

	switch {
	case strings.Contains(text, "rate limit"), strings.Contains(text, "too many requests"):
		return FailureRateLimited, 429, true
	case strings.Contains(text, "connection refused"), strings.Contains(text, "connection reset"),
		strings.Contains(text, "broken pipe"), strings.Contains(text, "no such host"),
		strings.Contains(text, "unexpected eof"), strings.Contains(text, "eof"):
		return FailureNetwork, 0, true
	case strings.Contains(text, "timeout"), strings.Contains(text, "timed out"):
		return FailureTimeout, 0, true
	}

	return FailureUnknown, 0, false
}

func classForStatus(status int) (FailureClass, bool) {
	switch {
	case status == 408:
		return FailureTimeout, true
	case status == 429:
		return FailureRateLimited, true
	case status == 401 || status == 403:
		return FailureAuth, false
	case status >= 500:
		return FailureUnavailable, true
	case status >= 400:
		return FailureBadRequest, false
	default:
		return FailureUnknown, false
	}
}
