package ports

import (
	"context"
	"errors"
)

var (
	// ErrLockBusy means another owner holds the session lock. Callers should
	// surface this as a retryable conflict instead of silently dropping work.
	ErrLockBusy = errors.New("session lock is busy")
	// ErrLockBackend means the lock backend (Redis) is unreachable or failed.
	ErrLockBackend = errors.New("lock backend unavailable")
)

// Locker serialises commands for one session across processes.
//
// Semantics:
//   - Acquire is keyed by a caller-chosen token. A different live token gets
//     ErrLockBusy (possibly after a bounded wait); the same token may re-enter
//     and extends the TTL, which is the only reentrant case.
//   - Implementations must set an expiry so a crashed owner cannot block a
//     session forever.
//   - Release only deletes the key when the token still matches. Releasing an
//     expired or foreign lock is a no-op, not an error.
type Locker interface {
	Acquire(ctx context.Context, key, token string) error
	Release(ctx context.Context, key, token string) error
}
