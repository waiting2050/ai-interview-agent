package ports

import (
	"context"
	"time"
)

// RateLimitResult is the outcome of one fixed-window check.
type RateLimitResult struct {
	Allowed    bool
	Limit      int
	Remaining  int
	ResetAfter time.Duration
}

// RateLimiter is a per-key fixed-window counter. Implementations may return an
// error when the backend is unavailable; callers decide the degradation policy
// (this project fails open and records a metric instead of blocking the site).
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (RateLimitResult, error)
}
