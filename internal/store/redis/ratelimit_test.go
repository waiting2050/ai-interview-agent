package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func newTestRateLimiter(t *testing.T) (*RateLimiter, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewRateLimiterWithClient(client), mr
}

func TestRateLimiterFixedWindow(t *testing.T) {
	limiter, mr := newTestRateLimiter(t)
	ctx := context.Background()
	key := BucketKey("create_session", "usr-1")

	for i := 0; i < 2; i++ {
		result, err := limiter.Allow(ctx, key, 2, time.Hour)
		if err != nil {
			t.Fatalf("allow %d: %v", i+1, err)
		}
		if !result.Allowed {
			t.Fatalf("request %d should be allowed: %+v", i+1, result)
		}
	}
	blocked, err := limiter.Allow(ctx, key, 2, time.Hour)
	if err != nil {
		t.Fatalf("third allow: %v", err)
	}
	if blocked.Allowed {
		t.Fatalf("third request should be rejected: %+v", blocked)
	}
	if blocked.Remaining != 0 || blocked.ResetAfter <= 0 {
		t.Fatalf("unexpected rejection result: %+v", blocked)
	}

	// The quota resets after the window expires.
	mr.FastForward(time.Hour)
	after, err := limiter.Allow(ctx, key, 2, time.Hour)
	if err != nil || !after.Allowed {
		t.Fatalf("after window: allowed=%v err=%v", after.Allowed, err)
	}
}

func TestRateLimiterSeparatesActionsAndUsers(t *testing.T) {
	limiter, _ := newTestRateLimiter(t)
	ctx := context.Background()

	if result, err := limiter.Allow(ctx, BucketKey("create_session", "usr-1"), 1, time.Hour); err != nil || !result.Allowed {
		t.Fatalf("first bucket: %+v err=%v", result, err)
	}
	if result, err := limiter.Allow(ctx, BucketKey("create_session", "usr-1"), 1, time.Hour); err != nil || result.Allowed {
		t.Fatalf("same bucket should be blocked: %+v err=%v", result, err)
	}
	if result, err := limiter.Allow(ctx, BucketKey("submit_answer", "usr-1"), 1, time.Hour); err != nil || !result.Allowed {
		t.Fatalf("different action should be allowed: %+v err=%v", result, err)
	}
	if result, err := limiter.Allow(ctx, BucketKey("create_session", "usr-2"), 1, time.Hour); err != nil || !result.Allowed {
		t.Fatalf("different user should be allowed: %+v err=%v", result, err)
	}
}
