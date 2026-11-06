// Package redis implements the cross-process session lock with Redis.
//
// The lock is a single key per session, acquired with SET NX PX and released
// with a compare-and-delete Lua script so an expired/reassigned lock can never
// be deleted by its previous owner.
package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

// LockOptions controls ownership and contention behaviour.
type LockOptions struct {
	// TTL bounds how long a crashed owner can block the session.
	TTL time.Duration
	// AcquireTimeout bounds how long Acquire waits for a busy lock before
	// returning ErrLockBusy.
	AcquireTimeout time.Duration
	// RetryInterval is the pause between acquisition attempts.
	RetryInterval time.Duration
}

func DefaultLockOptions() LockOptions {
	return LockOptions{
		TTL:            10 * time.Second,
		AcquireTimeout: 3 * time.Second,
		RetryInterval:  50 * time.Millisecond,
	}
}

type Locker struct {
	client         *goredis.Client
	ttl            time.Duration
	acquireTimeout time.Duration
	retryInterval  time.Duration
}

func NewLocker(addr, password string, db int, opts LockOptions) *Locker {
	client := goredis.NewClient(&goredis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	return NewLockerWithClient(client, opts)
}

// NewLockerWithClient is used by tests with miniredis.
func NewLockerWithClient(client *goredis.Client, opts LockOptions) *Locker {
	if opts.TTL <= 0 {
		opts.TTL = DefaultLockOptions().TTL
	}
	if opts.AcquireTimeout <= 0 {
		opts.AcquireTimeout = DefaultLockOptions().AcquireTimeout
	}
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = DefaultLockOptions().RetryInterval
	}
	return &Locker{
		client:         client,
		ttl:            opts.TTL,
		acquireTimeout: opts.AcquireTimeout,
		retryInterval:  opts.RetryInterval,
	}
}

func (l *Locker) Ping(ctx context.Context) error {
	if err := l.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("%w: %v", ports.ErrLockBackend, err)
	}
	return nil
}

func (l *Locker) Close() error { return l.client.Close() }

// Acquire takes the key for token. Re-entering with the same token extends
// the TTL; any other live token yields ErrLockBusy after the bounded wait.
func (l *Locker) Acquire(ctx context.Context, key, token string) error {
	waitCtx, cancel := context.WithTimeout(ctx, l.acquireTimeout)
	defer cancel()

	for {
		ok, err := l.client.SetNX(waitCtx, key, token, l.ttl).Result()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return fmt.Errorf("%w: %v", ports.ErrLockBusy, err)
			}
			return fmt.Errorf("%w: %v", ports.ErrLockBackend, err)
		}
		if ok {
			return nil
		}

		// Reentrancy: the same owner may refresh instead of deadlocking on
		// itself. There is no nesting count, so one Release releases the key.
		current, err := l.client.Get(waitCtx, key).Result()
		if err == nil && current == token {
			if err := l.client.Expire(waitCtx, key, l.ttl).Err(); err != nil {
				return fmt.Errorf("%w: %v", ports.ErrLockBackend, err)
			}
			return nil
		}
		if err != nil && !errors.Is(err, goredis.Nil) {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return fmt.Errorf("%w: %v", ports.ErrLockBusy, err)
			}
			return fmt.Errorf("%w: %v", ports.ErrLockBackend, err)
		}

		timer := time.NewTimer(l.retryInterval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return fmt.Errorf("%w: wait %s for %s", ports.ErrLockBusy, l.acquireTimeout, key)
		case <-timer.C:
		}
	}
}

var releaseScript = goredis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("del", KEYS[1])
end
return 0
`)

func (l *Locker) Release(ctx context.Context, key, token string) error {
	if err := releaseScript.Run(ctx, l.client, []string{key}, token).Err(); err != nil {
		return fmt.Errorf("%w: %v", ports.ErrLockBackend, err)
	}
	return nil
}
