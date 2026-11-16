package app

import (
	"context"
	"errors"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

func budgetSession(t *testing.T, svc *Service, owner string) *session.Session {
	t.Helper()
	sess, err := svc.CreateSession(context.Background(), CreateSessionInput{
		OwnerID:    owner,
		TargetRole: "Go 后端开发",
		ResumeText: "三年 Go 经验",
		JDText:     "熟悉分布式系统",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

func addSpend(t *testing.T, st *memory.Store, sessionID, runID string, prompt, completion int) {
	t.Helper()
	ctx := context.Background()
	run := &session.Run{
		ID:             runID,
		SessionID:      sessionID,
		Kind:           session.RunKindAnswer,
		Status:         session.RunStatusSucceeded,
		IdempotencyKey: runID,
		RequestHash:    "hash",
		CreatedAt:      fixedNow,
		UpdatedAt:      fixedNow,
	}
	if _, _, err := st.Repos().Runs.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.Repos().Runs.AddUsage(ctx, runID, prompt, completion, 1, 0); err != nil {
		t.Fatalf("add usage: %v", err)
	}
}

func TestBudgetRejectsExhaustedUserEvenWhenSessionHasRoom(t *testing.T) {
	metrics := obs.NewMetrics()
	svc, st := newTestService(t, Options{Metrics: metrics})
	ctx := context.Background()
	sess := budgetSession(t, svc, "usr_budget_u1")
	addSpend(t, st, sess.ID, "run-u1", 60, 40)

	setLimit := func(scope, id string, limit int64) {
		if err := st.Repos().Budgets.SetLimit(ctx, scope, id, limit); err != nil {
			t.Fatal(err)
		}
	}
	setLimit(ports.BudgetScopeUser, "usr_budget_u1", 100)
	setLimit(ports.BudgetScopeSession, sess.ID, 100000)

	_, err := svc.Start(ctx, StartCommand{SessionID: sess.ID, IdempotencyKey: "start-u1"})
	if !errors.Is(err, ErrUserBudgetExceeded) {
		t.Fatalf("err = %v, want ErrUserBudgetExceeded", err)
	}
	if got := metrics.BudgetRejectionTotal(ports.BudgetScopeUser); got != 1 {
		t.Fatalf("user rejections = %d, want 1", got)
	}
	if got := metrics.BudgetRejectionTotal(ports.BudgetScopeSession); got != 0 {
		t.Fatalf("session rejections = %d, want 0 (user is checked first)", got)
	}
}

func TestBudgetRejectsExhaustedSessionEvenWhenUserHasRoom(t *testing.T) {
	metrics := obs.NewMetrics()
	svc, st := newTestService(t, Options{Metrics: metrics})
	ctx := context.Background()
	sess := budgetSession(t, svc, "usr_budget_u2")
	addSpend(t, st, sess.ID, "run-u2", 50, 50)

	if err := st.Repos().Budgets.SetLimit(ctx, ports.BudgetScopeUser, "usr_budget_u2", 100000); err != nil {
		t.Fatal(err)
	}
	if err := st.Repos().Budgets.SetLimit(ctx, ports.BudgetScopeSession, sess.ID, 100); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Start(ctx, StartCommand{SessionID: sess.ID, IdempotencyKey: "start-u2"})
	if !errors.Is(err, ErrSessionBudgetExceeded) {
		t.Fatalf("err = %v, want ErrSessionBudgetExceeded", err)
	}
	if got := metrics.BudgetRejectionTotal(ports.BudgetScopeSession); got != 1 {
		t.Fatalf("session rejections = %d, want 1", got)
	}
	if got := metrics.BudgetRejectionTotal(ports.BudgetScopeUser); got != 0 {
		t.Fatalf("user rejections = %d, want 0", got)
	}
}

func TestBudgetUserScopeWinsWhenBothAreExhausted(t *testing.T) {
	metrics := obs.NewMetrics()
	svc, st := newTestService(t, Options{Metrics: metrics})
	ctx := context.Background()
	sess := budgetSession(t, svc, "usr_budget_u3")
	addSpend(t, st, sess.ID, "run-u3", 80, 20)

	for _, scope := range []struct {
		name string
		id   string
	}{{ports.BudgetScopeUser, "usr_budget_u3"}, {ports.BudgetScopeSession, sess.ID}} {
		if err := st.Repos().Budgets.SetLimit(ctx, scope.name, scope.id, 100); err != nil {
			t.Fatal(err)
		}
	}

	_, err := svc.Start(ctx, StartCommand{SessionID: sess.ID, IdempotencyKey: "start-u3"})
	if !errors.Is(err, ErrUserBudgetExceeded) {
		t.Fatalf("err = %v, want user scope to win", err)
	}
}

func TestBudgetWithoutLimitsAllowsStart(t *testing.T) {
	svc, st := newTestService(t, Options{})
	ctx := context.Background()
	sess := budgetSession(t, svc, "usr_budget_u4")
	addSpend(t, st, sess.ID, "run-u4", 10, 10)

	if _, err := svc.Start(ctx, StartCommand{SessionID: sess.ID, IdempotencyKey: "start-u4"}); err != nil {
		t.Fatalf("start without limits: %v", err)
	}
}
