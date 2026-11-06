package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// eventChannel is the PostgreSQL NOTIFY channel shared by all instances.
// The payload is the session_id; a transaction that appends an event issues
// pg_notify inside the same transaction, so notifications are only delivered
// after commit.
const eventChannel = "ai_interview_events"

// notifier owns one dedicated LISTEN connection per process and fans the
// wake-up signal out to in-process SSE subscribers. It is not a message queue:
// readers always query session_events so ordering and Last-Event-ID replay
// remain authoritative.
type notifier struct {
	pool *pgxpool.Pool
	log  *slog.Logger

	mu     sync.Mutex
	subs   map[string]map[chan struct{}]struct{}
	closed bool

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	ready  chan struct{}
	once   sync.Once
}

func newNotifier(pool *pgxpool.Pool, log *slog.Logger) *notifier {
	ctx, cancel := context.WithCancel(context.Background())
	return &notifier{
		pool:   pool,
		log:    log,
		subs:   map[string]map[chan struct{}]struct{}{},
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
		ready:  make(chan struct{}),
	}
}

func (n *notifier) start() {
	go func() {
		defer close(n.done)
		n.listenLoop()
	}()
}

func (n *notifier) stop() {
	n.cancel()
	<-n.done
}

func (n *notifier) listenLoop() {
	for {
		err := n.listen()
		if n.ctx.Err() != nil {
			return
		}
		n.log.Warn("postgres event listener disconnected, retrying", "error", err)
		select {
		case <-n.ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (n *notifier) listen() error {
	conn, err := n.pool.Acquire(n.ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(n.ctx, "LISTEN "+eventChannel); err != nil {
		return err
	}
	n.once.Do(func() { close(n.ready) })

	for {
		notification, err := conn.Conn().WaitForNotification(n.ctx)
		if err != nil {
			return err
		}
		n.broadcast(notification.Payload)
	}
}

func (n *notifier) broadcast(sessionID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return
	}
	for ch := range n.subs[sessionID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Subscribe returns a coalescing wake-up channel for one session. Events that
// arrive between calls are recovered by the caller's initial ListAfter query,
// so this channel is intentionally lossy.
func (n *notifier) Subscribe(ctx context.Context, sessionID string) (<-chan struct{}, error) {
	// Wait until the LISTEN connection is registered so an event committed
	// right after Subscribe cannot slip past both the listener and the
	// caller's backlog query. Bounded so a broken listener does not pin SSE.
	readyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	select {
	case <-n.ready:
	case <-readyCtx.Done():
		return nil, fmt.Errorf("event notifier not ready: %w", readyCtx.Err())
	case <-n.ctx.Done():
		return nil, errors.New("event notifier is closed")
	}

	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil, errors.New("event notifier is closed")
	}
	if n.subs[sessionID] == nil {
		n.subs[sessionID] = map[chan struct{}]struct{}{}
	}
	ch := make(chan struct{}, 1)
	n.subs[sessionID][ch] = struct{}{}
	n.mu.Unlock()

	go func() {
		<-ctx.Done()
		n.mu.Lock()
		defer n.mu.Unlock()
		if subs, ok := n.subs[sessionID]; ok {
			if _, exists := subs[ch]; exists {
				delete(subs, ch)
				close(ch)
			}
			if len(subs) == 0 {
				delete(n.subs, sessionID)
			}
		}
	}()

	return ch, nil
}

func (n *notifier) close() {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return
	}
	n.closed = true
	for sessionID, subs := range n.subs {
		for ch := range subs {
			close(ch)
		}
		delete(n.subs, sessionID)
	}
	n.mu.Unlock()
	n.cancel()
	<-n.done
}
