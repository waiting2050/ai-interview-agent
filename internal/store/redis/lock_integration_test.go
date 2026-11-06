//go:build integration

package redis

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

// TestLockerAgainstRealRedis verifies the same lock contract as the miniredis
// tests, but against a real server: SET NX PX, the compare-and-delete Lua
// release, TTL expiry and same-token TTL refresh.
func TestLockerAgainstRealRedis(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR is not set; real Redis integration test skipped")
	}

	client := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping redis %s: %v", addr, err)
	}
	version, err := client.Do(ctx, "INFO", "server").Text()
	if err != nil {
		t.Fatalf("redis INFO: %v", err)
	}
	t.Logf("real redis %s: %s", addr, firstLine(version))

	key := "lock:session:integration-" + time.Now().Format("150405.000000000")
	t.Cleanup(func() { _ = client.Del(context.Background(), key).Err() })

	opts := LockOptions{
		TTL:            800 * time.Millisecond,
		AcquireTimeout: 60 * time.Millisecond,
		RetryInterval:  5 * time.Millisecond,
	}
	locker := NewLockerWithClient(client, opts)

	if err := locker.Acquire(ctx, key, "token-a"); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := locker.Acquire(ctx, key, "token-b"); !errors.Is(err, ports.ErrLockBusy) {
		t.Fatalf("second owner = %v, want ErrLockBusy", err)
	}

	// Same token refreshes the TTL instead of deadlocking on itself.
	time.Sleep(500 * time.Millisecond)
	if err := locker.Acquire(ctx, key, "token-a"); err != nil {
		t.Fatalf("re-entrant acquire: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if err := locker.Acquire(ctx, key, "token-b"); !errors.Is(err, ports.ErrLockBusy) {
		t.Fatalf("after refresh, other owner = %v, want ErrLockBusy", err)
	}

	// A foreign release must not remove the live lock (Lua compare-and-delete).
	if err := locker.Release(ctx, key, "token-b"); err != nil {
		t.Fatalf("foreign release: %v", err)
	}
	held, err := client.Get(ctx, key).Result()
	if err != nil {
		t.Fatalf("get after foreign release: %v", err)
	}
	if held != "token-a" {
		t.Fatalf("foreign release changed owner to %q", held)
	}

	if err := locker.Release(ctx, key, "token-a"); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	if _, err := client.Get(ctx, key).Result(); !errors.Is(err, goredis.Nil) {
		t.Fatalf("owner release left key: %v", err)
	}
	if err := locker.Acquire(ctx, key, "token-b"); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := locker.Release(ctx, key, "token-b"); err != nil {
		t.Fatalf("release b: %v", err)
	}

	// TTL expires without an explicit release, and a stale release from the
	// expired owner must not delete the next owner's lock.
	shortOpts := opts
	shortOpts.TTL = 300 * time.Millisecond
	shortLocker := NewLockerWithClient(client, shortOpts)
	if err := shortLocker.Acquire(ctx, key, "token-c"); err != nil {
		t.Fatalf("acquire c: %v", err)
	}
	time.Sleep(450 * time.Millisecond)
	if _, err := client.Get(ctx, key).Result(); !errors.Is(err, goredis.Nil) {
		t.Fatalf("ttl did not expire the lock: %v", err)
	}
	if err := shortLocker.Acquire(ctx, key, "token-d"); err != nil {
		t.Fatalf("acquire d after ttl: %v", err)
	}
	if err := shortLocker.Release(ctx, key, "token-c"); err != nil {
		t.Fatalf("stale release c: %v", err)
	}
	held, err = client.Get(ctx, key).Result()
	if err != nil || held != "token-d" {
		t.Fatalf("stale release removed new owner: held=%q err=%v", held, err)
	}
	if err := shortLocker.Release(ctx, key, "token-d"); err != nil {
		t.Fatalf("release d: %v", err)
	}
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' || s[i] == '\r' {
			return s[:i]
		}
	}
	return s
}
