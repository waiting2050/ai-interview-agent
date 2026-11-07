// Package lock provides process-local and Redis-backed implementations of
// ports.Locker.
package lock

import (
	"context"
	"sync"

	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

// ProcessLocker is the in-process test double. It has no TTL and does not
// survive a crash or span processes; runtime wiring must use the Redis locker.
type ProcessLocker struct {
	mu   sync.Mutex
	held map[string]string
}

func NewProcessLocker() *ProcessLocker {
	return &ProcessLocker{held: map[string]string{}}
}

func (l *ProcessLocker) Acquire(_ context.Context, key, token string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if owner, ok := l.held[key]; ok && owner != token {
		return ports.ErrLockBusy
	}
	l.held[key] = token
	return nil
}

// Refresh is a no-op for the in-process lock: it has no TTL, so ownership
// cannot expire while the holder is alive.
func (l *ProcessLocker) Refresh(_ context.Context, key, token string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if owner, ok := l.held[key]; !ok || owner != token {
		return ports.ErrLockBusy
	}
	return nil
}

func (l *ProcessLocker) Release(_ context.Context, key, token string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if owner, ok := l.held[key]; ok && owner == token {
		delete(l.held, key)
	}
	return nil
}
