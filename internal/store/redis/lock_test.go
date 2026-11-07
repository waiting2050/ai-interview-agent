package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

// newTestLocker starts miniredis as an in-process Redis substitute, so the
// cross-instance lock semantics are verifiable without Docker.
func newTestLocker(t *testing.T, opts LockOptions) (*Locker, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewLockerWithClient(client, opts), mr
}

func TestLockerExcludesSecondOwner(t *testing.T) {
	opts := LockOptions{TTL: time.Second, AcquireTimeout: 80 * time.Millisecond, RetryInterval: 10 * time.Millisecond}
	locker, _ := newTestLocker(t, opts)
	ctx := context.Background()

	if err := locker.Acquire(ctx, "lock:session:s1", "token-a"); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := locker.Acquire(ctx, "lock:session:s1", "token-b"); !errors.Is(err, ports.ErrLockBusy) {
		t.Fatalf("second acquire = %v, want ErrLockBusy", err)
	}
}

func TestLockerReentrantSameTokenExtendsTTL(t *testing.T) {
	opts := LockOptions{TTL: time.Second, AcquireTimeout: 50 * time.Millisecond, RetryInterval: 10 * time.Millisecond}
	locker, mr := newTestLocker(t, opts)
	ctx := context.Background()

	if err := locker.Acquire(ctx, "lock:session:s2", "token-a"); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	mr.FastForward(700 * time.Millisecond)
	if err := locker.Acquire(ctx, "lock:session:s2", "token-a"); err != nil {
		t.Fatalf("re-entrant acquire: %v", err)
	}
	mr.FastForward(700 * time.Millisecond)
	// Without the refresh the 1s TTL would have expired here.
	if err := locker.Acquire(ctx, "lock:session:s2", "token-b"); !errors.Is(err, ports.ErrLockBusy) {
		t.Fatalf("other owner after refresh = %v, want ErrLockBusy", err)
	}
}

func TestLockerExpiresAndReleaseChecksToken(t *testing.T) {
	opts := LockOptions{TTL: 500 * time.Millisecond, AcquireTimeout: 20 * time.Millisecond, RetryInterval: 5 * time.Millisecond}
	locker, mr := newTestLocker(t, opts)
	ctx := context.Background()

	if err := locker.Acquire(ctx, "lock:session:s3", "token-a"); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// A foreign release must not remove the current owner's lock.
	if err := locker.Release(ctx, "lock:session:s3", "token-b"); err != nil {
		t.Fatalf("foreign release: %v", err)
	}
	if err := locker.Acquire(ctx, "lock:session:s3", "token-b"); !errors.Is(err, ports.ErrLockBusy) {
		t.Fatalf("foreign release removed lock: %v", err)
	}

	mr.FastForward(time.Second)
	if err := locker.Acquire(ctx, "lock:session:s3", "token-b"); err != nil {
		t.Fatalf("acquire after ttl: %v", err)
	}
	if err := locker.Release(ctx, "lock:session:s3", "token-b"); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func TestLockerRefreshOnlyForCurrentOwner(t *testing.T) {
	opts := LockOptions{TTL: 500 * time.Millisecond, AcquireTimeout: 20 * time.Millisecond, RetryInterval: 5 * time.Millisecond}
	locker, mr := newTestLocker(t, opts)
	ctx := context.Background()

	if err := locker.Acquire(ctx, "lock:session:s5", "token-a"); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := locker.Refresh(ctx, "lock:session:s5", "token-b"); !errors.Is(err, ports.ErrLockBusy) {
		t.Fatalf("foreign refresh = %v, want ErrLockBusy", err)
	}
	mr.FastForward(400 * time.Millisecond)
	if err := locker.Refresh(ctx, "lock:session:s5", "token-a"); err != nil {
		t.Fatalf("refresh before expiry: %v", err)
	}
	mr.FastForward(400 * time.Millisecond)
	// The refresh above restarted the 500ms TTL, so the lock is still ours.
	if err := locker.Refresh(ctx, "lock:session:s5", "token-a"); err != nil {
		t.Fatalf("refresh after renewed ttl: %v", err)
	}

	mr.FastForward(time.Second)
	if err := locker.Refresh(ctx, "lock:session:s5", "token-a"); !errors.Is(err, ports.ErrLockBusy) {
		t.Fatalf("refresh after expiry = %v, want ErrLockBusy", err)
	}
}

func TestLockerBackendFailureIsNotBusy(t *testing.T) {
	locker, _ := newTestLocker(t, DefaultLockOptions())
	if err := locker.client.Close(); err != nil {
		t.Fatal(err)
	}
	err := locker.Acquire(context.Background(), "lock:session:s4", "token-a")
	if !errors.Is(err, ports.ErrLockBackend) {
		t.Fatalf("acquire on closed backend = %v, want ErrLockBackend", err)
	}
}
