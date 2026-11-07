package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

// RateLimiter is a fixed-window counter backed by Redis. The window starts at
// the first request for a key; INCR plus PEXPIRE run in one Lua script so two
// instances cannot leave a key without a TTL.
type RateLimiter struct {
	client *goredis.Client
}

var rateLimitScript = goredis.NewScript(`
local current = redis.call('INCR', KEYS[1])
if current == 1 then
	redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('PTTL', KEYS[1])
if ttl < 0 then
	redis.call('PEXPIRE', KEYS[1], ARGV[1])
	ttl = tonumber(ARGV[1])
end
return {current, ttl}
`)

func NewRateLimiter(addr, password string, db int) *RateLimiter {
	client := goredis.NewClient(&goredis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	return &RateLimiter{client: client}
}

func NewRateLimiterWithClient(client *goredis.Client) *RateLimiter {
	return &RateLimiter{client: client}
}

func (r *RateLimiter) Ping(ctx context.Context) error {
	if err := r.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping redis rate limiter: %w", err)
	}
	return nil
}

func (r *RateLimiter) Close() error {
	return r.client.Close()
}

func (r *RateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (ports.RateLimitResult, error) {
	if limit <= 0 {
		return ports.RateLimitResult{Allowed: true, Limit: limit}, nil
	}
	if window <= 0 {
		window = time.Hour
	}
	values, err := rateLimitScript.Run(ctx, r.client, []string{key}, window.Milliseconds()).Int64Slice()
	if err != nil {
		return ports.RateLimitResult{}, fmt.Errorf("redis rate limit %q: %w", key, err)
	}
	if len(values) != 2 {
		return ports.RateLimitResult{}, fmt.Errorf("redis rate limit %q: unexpected reply", key)
	}
	count := int(values[0])
	reset := time.Duration(values[1]) * time.Millisecond
	if reset < 0 {
		reset = window
	}

	remaining := limit - count
	if remaining < 0 {
		remaining = 0
	}
	return ports.RateLimitResult{
		Allowed:    count <= limit,
		Limit:      limit,
		Remaining:  remaining,
		ResetAfter: reset,
	}, nil
}

// BucketKey builds a stable key. The action and user id are separated so the
// three command classes hold independent quotas.
func BucketKey(action, userID string) string {
	return "rl:" + action + ":" + userID
}
