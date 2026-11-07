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
//
// Timing note: go-redis floors sub-second TTLs to 1s, so this test uses an
// explicitly injected multi-second TTL instead of a tight one. Expiry is
// awaited by polling the key, never by assuming that a fixed sleep is
// shorter than the TTL; that keeps the assertion independent of machine load.
func TestLockerAgainstRealRedis(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR is not set; real Redis integration test skipped")
	}

	client := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

	const ttl = 3 * time.Second
	opts := LockOptions{
		TTL:            ttl,
		AcquireTimeout: 200 * time.Millisecond,
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

	// TTL expires without an explicit release. Wait for the key to disappear
	// instead of sleeping for a duration guessed to be shorter than the TTL;
	// a stale release from the expired owner must not delete the next owner.
	if err := locker.Acquire(ctx, key, "token-c"); err != nil {
		t.Fatalf("acquire c: %v", err)
	}
	waitForKeyGone(t, client, key, ttl+2*time.Second)
	if err := locker.Acquire(ctx, key, "token-d"); err != nil {
		t.Fatalf("acquire d after ttl: %v", err)
	}
	if err := locker.Release(ctx, key, "token-c"); err != nil {
		t.Fatalf("stale release c: %v", err)
	}
	held, err = client.Get(ctx, key).Result()
	if err != nil || held != "token-d" {
		t.Fatalf("stale release removed new owner: held=%q err=%v", held, err)
	}
	if err := locker.Release(ctx, key, "token-d"); err != nil {
		t.Fatalf("release d: %v", err)
	}
}

// waitForKeyGone polls until the key is absent. The timeout is only a test
// safety net; success does not depend on how fast the machine is.
func waitForKeyGone(t *testing.T, client *goredis.Client, key string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		_, err := client.Get(context.Background(), key).Result()
		if errors.Is(err, goredis.Nil) {
			return
		}
		if err != nil {
			t.Fatalf("poll key %s: %v", key, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("key %s still present after %s", key, timeout)
		}
		time.Sleep(10 * time.Millisecond)
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
