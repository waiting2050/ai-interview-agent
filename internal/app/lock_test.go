package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
	"github.com/waiting2050/ai-interview-agent/internal/store/redis"
)

// TestSessionLockSurvivesLongInlineRun proves the watchdog keeps the session
// lock alive while a synchronous run outlives the locker TTL. Without renewal
// the key expires mid-run and a second command can acquire the session lock.
func TestSessionLockSurvivesLongInlineRun(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	locker := redis.NewLockerWithClient(client, redis.LockOptions{
		TTL:            150 * time.Millisecond,
		AcquireTimeout: 80 * time.Millisecond,
		RetryInterval:  10 * time.Millisecond,
	})

	started := make(chan struct{})
	generator := interview.NewQuestionGenerator(interview.NewHeuristicClient(), "fake-heuristic")
	steps := DefaultStartSteps(generator)
	plan := steps.Plan
	steps.Plan = func(ctx context.Context, in StartInput) (PlanResult, error) {
		close(started)
		// Far longer than the 150ms TTL: the watchdog must refresh in between.
		time.Sleep(600 * time.Millisecond)
		return plan(ctx, in)
	}

	svc, _ := newTestService(t, Options{
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Locker:            locker,
		LockRenewInterval: 50 * time.Millisecond,
		Steps:             steps,
	})
	sess := createSession(t, svc)

	done := make(chan error, 1)
	go func() {
		_, err := svc.Start(context.Background(), StartCommand{
			SessionID:      sess.ID,
			IdempotencyKey: "start-long",
			RequestBody:    []byte(`{"action":"start"}`),
		})
		done <- err
	}()

	<-started
	// Well past the TTL: a stale lock would already be gone here.
	time.Sleep(300 * time.Millisecond)

	acquireCtx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := locker.Acquire(acquireCtx, "lock:session:"+sess.ID, "intruder"); !errors.Is(err, ports.ErrLockBusy) {
		t.Fatalf("lock was stolen while the run executed: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := locker.Acquire(context.Background(), "lock:session:"+sess.ID, "intruder"); err != nil {
		t.Fatalf("lock not released after the run: %v", err)
	}
}

// TestRunPersistsHTTPTraceID is the handoff between the synchronous command
// context and the worker: the run row must carry the trace so a later claim can
// restore it.
func TestRunPersistsHTTPTraceID(t *testing.T) {
	svc, store := newTestService(t, Options{})
	sess := createSession(t, svc)

	ctx := reqctx.WithTrace(context.Background(), "trace-http-1")
	result, err := svc.Start(ctx, StartCommand{
		SessionID:      sess.ID,
		IdempotencyKey: "start-trace",
		RequestBody:    []byte(`{"action":"start"}`),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	run, err := store.Repos().Runs.Get(context.Background(), result.RunID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if run.TraceID != "trace-http-1" {
		t.Fatalf("run trace_id = %q", run.TraceID)
	}
}
