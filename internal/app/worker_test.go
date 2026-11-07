package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

func seedRun(t *testing.T, store *memory.Store, id, sessionID string) *session.Run {
	t.Helper()
	now := fixedNow
	sess := &session.Session{
		ID:           sessionID,
		OwnerID:      "usr_worker_test",
		Status:       session.StatusActive,
		Phase:        session.PhaseAwaitingAnswer,
		StateVersion: 1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := store.Repos().Sessions.Create(context.Background(), sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	run := &session.Run{
		ID:             id,
		SessionID:      sessionID,
		Kind:           session.RunKindStart,
		Status:         session.RunStatusRunning,
		IdempotencyKey: id,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if created, _, err := store.Repos().Runs.Create(context.Background(), run); err != nil || !created {
		t.Fatalf("seed run: created=%v err=%v", created, err)
	}
	return run
}

// Two workers race for the same queued run: exactly one must win.
func TestClaimRunOnlyOnce(t *testing.T) {
	store := memory.New()
	seedRun(t, store, "run_once", "ses_once")
	now := fixedNow

	var wg sync.WaitGroup
	results := make([][]session.Run, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claimed, err := store.Repos().Runs.Claim(context.Background(),
				fmt.Sprintf("worker-%d", i), fmt.Sprintf("lease-%d", i), now, now.Add(time.Minute), 1)
			results[i], errs[i] = claimed, err
		}(i)
	}
	wg.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("claim errors: %v %v", errs[0], errs[1])
	}
	claimed := len(results[0]) + len(results[1])
	if claimed != 1 {
		t.Fatalf("expected exactly one claim, got %d", claimed)
	}

	// The active lease keeps the run out of the queue.
	again, err := store.Repos().Runs.Claim(context.Background(), "worker-3", "lease-3",
		now.Add(10*time.Second), now.Add(time.Minute), 1)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected an active lease to block claiming, got %d", len(again))
	}
}

// An expired lease must become claimable again with a new lease id.
func TestClaimReclaimsExpiredLease(t *testing.T) {
	store := memory.New()
	seedRun(t, store, "run_expired", "ses_expired")
	now := fixedNow

	first, err := store.Repos().Runs.Claim(context.Background(), "worker-a", "lease-a",
		now, now.Add(100*time.Millisecond), 1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: len=%d err=%v", len(first), err)
	}

	blocked, err := store.Repos().Runs.Claim(context.Background(), "worker-b", "lease-b",
		now.Add(50*time.Millisecond), now.Add(time.Minute), 1)
	if err != nil {
		t.Fatalf("blocked claim: %v", err)
	}
	if len(blocked) != 0 {
		t.Fatalf("lease should still be active, got %d claims", len(blocked))
	}

	second, err := store.Repos().Runs.Claim(context.Background(), "worker-b", "lease-b",
		now.Add(time.Second), now.Add(time.Minute), 1)
	if err != nil || len(second) != 1 {
		t.Fatalf("reclaim after expiry: len=%d err=%v", len(second), err)
	}
	if second[0].LeaseID != "lease-b" || second[0].LeaseWorker != "worker-b" {
		t.Fatalf("unexpected lease after reclaim: %+v", second[0])
	}
	if second[0].Attempt != 2 {
		t.Fatalf("expected attempt 2 after reclaim, got %d", second[0].Attempt)
	}
}

// Consecutive failures reach the dead-letter threshold; an explicit retry of a
// dead run gets a fresh budget and executes successfully.
func TestDeadLetterAndManualRetry(t *testing.T) {
	failing := true
	var mu sync.Mutex
	steps := StartSteps{
		Plan: func(_ context.Context, _ StartInput) (PlanResult, error) {
			mu.Lock()
			defer mu.Unlock()
			if failing {
				return PlanResult{}, errors.New("planner unavailable")
			}
			return PlanResult{
				Direction: "go",
				Items: []interview.PlanItemView{{
					Seq: 1, Topic: "并发", Objective: "解释 channel", ExpectedPoints: []string{"happens-before"},
				}},
			}, nil
		},
		Ask: func(_ context.Context, _ StartInput, plan PlanResult) (AskResult, error) {
			item := plan.Items[0]
			return AskResult{TurnID: "turn-1", Topic: item.Topic, Text: "说说 channel", Depth: 1, ExpectedPoints: item.ExpectedPoints}, nil
		},
	}
	svc, store := newTestService(t, Options{Steps: steps, MaxRunFailures: 3})

	sess, err := svc.CreateSession(context.Background(), CreateSessionInput{
		OwnerID:    "usr_dead",
		TargetRole: "Go 后端",
		ResumeText: "三年 Go 经验",
		JDText:     "熟悉并发",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	started, err := svc.Start(context.Background(), StartCommand{SessionID: sess.ID, RequestBody: []byte(`{}`)})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := svc.Retry(context.Background(), sess.ID, started.RunID); err != nil {
			t.Fatalf("retry %d: %v", i+1, err)
		}
	}

	run, err := store.Repos().Runs.Get(context.Background(), started.RunID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.Status != session.RunStatusDead {
		t.Fatalf("expected dead after 3 failures, got %s (count=%d)", run.Status, run.FailureCount)
	}
	if run.FailureCount != 3 || run.LastErrorClass != "internal" || run.LastError == "" {
		t.Fatalf("unexpected dead run: %+v", run)
	}

	deadRuns, err := svc.ListRuns(context.Background(), "usr_dead", "dead", 10)
	if err != nil || len(deadRuns) != 1 {
		t.Fatalf("list dead: len=%d err=%v", len(deadRuns), err)
	}
	otherRuns, err := svc.ListRuns(context.Background(), "usr_other", "dead", 10)
	if err != nil {
		t.Fatalf("list other dead: %v", err)
	}
	if len(otherRuns) != 0 {
		t.Fatalf("dead runs must be owner scoped, got %d", len(otherRuns))
	}

	mu.Lock()
	failing = false
	mu.Unlock()
	if err := svc.Retry(context.Background(), sess.ID, started.RunID); err != nil {
		t.Fatalf("retry dead run: %v", err)
	}

	run, err = store.Repos().Runs.Get(context.Background(), started.RunID)
	if err != nil {
		t.Fatalf("get retried run: %v", err)
	}
	if run.Status != session.RunStatusSucceeded {
		t.Fatalf("expected succeeded after manual retry, got %s", run.Status)
	}
}

// Worker claims trigger execution while leases are renewed; this is the end-to-
// end path used by the server (commands enqueue, workers consume).
func TestWorkerPoolClaimsAndExecutes(t *testing.T) {
	steps := StartSteps{
		Plan: func(_ context.Context, _ StartInput) (PlanResult, error) {
			return PlanResult{Direction: "go", Items: []interview.PlanItemView{{
				Seq: 1, Topic: "并发", ExpectedPoints: []string{"happens-before"},
			}}}, nil
		},
		Ask: func(_ context.Context, _ StartInput, plan PlanResult) (AskResult, error) {
			return AskResult{TurnID: "turn-1", Topic: plan.Items[0].Topic, Text: "说说 channel", Depth: 1}, nil
		},
	}
	svc, store := newTestService(t, Options{Steps: steps})
	svc.synchronous = false

	metrics := obs.NewMetrics()
	pool := NewWorkerPool(svc, WorkerOptions{
		WorkerID:          "test-worker",
		Concurrency:       1,
		LeaseTTL:          2 * time.Second,
		HeartbeatInterval: 100 * time.Millisecond,
		PollInterval:      10 * time.Millisecond,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:           metrics,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool.Start(ctx)
	defer pool.Stop()

	sess, err := svc.CreateSession(ctx, CreateSessionInput{
		OwnerID:    "usr_worker",
		TargetRole: "Go 后端",
		ResumeText: "三年 Go 经验",
		JDText:     "熟悉并发",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.Start(ctx, StartCommand{SessionID: sess.ID, RequestBody: []byte(`{}`)}); err != nil {
		t.Fatalf("start: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := store.Repos().Sessions.Get(ctx, sess.ID); err == nil && got.Phase == session.PhaseAwaitingAnswer {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	sessionNow, err := store.Repos().Sessions.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sessionNow.Phase != session.PhaseAwaitingAnswer {
		t.Fatalf("worker did not execute run, phase=%s", sessionNow.Phase)
	}
	if metrics.WorkerClaimTotal() == 0 {
		t.Fatalf("expected worker claim metric")
	}
	pool.Stop()
	pool.Wait()
}
