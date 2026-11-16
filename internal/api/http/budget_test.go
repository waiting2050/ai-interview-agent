package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

// TestStartRejectsExceededBudgetWith429 pins the wire contract: a budget
// rejection must be distinguishable from a generic 500 by both status and code.
func TestStartRejectsExceededBudgetWith429(t *testing.T) {
	store := memory.New()
	metrics := obs.NewMetrics()
	svc := app.New(store, app.Options{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Synchronous: true,
		Evaluator:   fixedEvaluator{},
		Metrics:     metrics,
	})
	authSvc, token, userID := testAuthFor(t, store)
	ctx := context.Background()

	sess, err := svc.CreateSession(ctx, app.CreateSessionInput{
		OwnerID:    userID,
		TargetRole: "Go 后端开发",
		ResumeText: "三年 Go 经验",
		JDText:     "熟悉分布式系统",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := &session.Run{
		ID:             "run-budget-http",
		SessionID:      sess.ID,
		Kind:           session.RunKindAnswer,
		Status:         session.RunStatusSucceeded,
		IdempotencyKey: "run-budget-http",
		RequestHash:    "hash",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if _, _, err := store.Repos().Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := store.Repos().Runs.AddUsage(ctx, run.ID, 60, 40, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Repos().Budgets.SetLimit(ctx, ports.BudgetScopeUser, userID, 100); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(withTestAuthorization(NewRouter(Deps{
		Auth:    authSvc,
		Service: svc,
		Health:  store.Ping,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics: metrics,
	}), token))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions/"+sess.ID+"/start", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Idempotency-Key", "budget-start")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	var body errorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "user_budget_exceeded" {
		t.Fatalf("code = %q", body.Code)
	}
	if got := metrics.BudgetRejectionTotal(ports.BudgetScopeUser); got != 1 {
		t.Fatalf("user rejections = %d", got)
	}
}
