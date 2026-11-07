package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

type fakeRateLimiter struct {
	mu      sync.Mutex
	allow   bool
	err     error
	limits  []int
	keys    []string
	actions []string
	reset   time.Duration
}

func (f *fakeRateLimiter) Allow(_ context.Context, key string, limit int, _ time.Duration) (ports.RateLimitResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	f.limits = append(f.limits, limit)
	f.actions = append(f.actions, key)
	if f.err != nil {
		return ports.RateLimitResult{}, f.err
	}
	return ports.RateLimitResult{Allowed: f.allow, Limit: limit, ResetAfter: f.reset}, nil
}

func rateLimitServer(t *testing.T, limiter ports.RateLimiter, cfg RateLimitConfig) (*httptest.Server, *obs.Metrics, string) {
	t.Helper()
	store := memory.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authSvc, token, _ := testAuthFor(t, store)
	metrics := obs.NewMetrics()
	srv := httptest.NewServer(NewRouter(Deps{
		Auth:        authSvc,
		Service:     app.New(store, app.Options{Logger: logger, Synchronous: true}),
		Logger:      logger,
		Metrics:     metrics,
		RateLimiter: limiter,
		RateLimits:  cfg,
	}))
	t.Cleanup(srv.Close)
	return srv, metrics, token
}

func createSessionBody() map[string]any {
	return map[string]any{
		"target_role": "Go 后端",
		"resume_text": "三年 Go 经验",
		"jd_text":     "熟悉并发",
	}
}

func seedOwnedRun(t *testing.T, store *memory.Store, runID, sessionID, owner string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	sess := &session.Session{
		ID: sessionID, OwnerID: owner, Status: session.StatusCreated,
		StateVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Repos().Sessions.Create(ctx, sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	run := &session.Run{
		ID: runID, SessionID: sessionID, Kind: session.RunKindStart,
		Status: session.RunStatusRunning, IdempotencyKey: runID,
		CreatedAt: now, UpdatedAt: now,
	}
	if created, _, err := store.Repos().Runs.Create(ctx, run); err != nil || !created {
		t.Fatalf("seed run: created=%v err=%v", created, err)
	}
}

func TestRateLimitRejectsWith429(t *testing.T) {
	limiter := &fakeRateLimiter{allow: false, reset: 30 * time.Second}
	srv, metrics, token := rateLimitServer(t, limiter, RateLimitConfig{
		Window: time.Hour, Session: 2, DemoSession: 1,
	})

	resp, body := postJSON(t, srv, "/v1/sessions", token, createSessionBody())
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"rate_limited"`) || !strings.Contains(string(body), `"create_session"`) {
		t.Fatalf("unexpected 429 body: %s", body)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatalf("expected Retry-After header")
	}
	if metrics.RateLimitRejectionTotal(rateActionSession) != 1 {
		t.Fatalf("expected one rejection metric, got %d", metrics.RateLimitRejectionTotal(rateActionSession))
	}
}

func TestRateLimiterBackendErrorFailsOpen(t *testing.T) {
	limiter := &fakeRateLimiter{allow: false, err: context.DeadlineExceeded}
	srv, metrics, token := rateLimitServer(t, limiter, RateLimitConfig{
		Window: time.Hour, Session: 2, DemoSession: 1,
	})

	resp, body := postJSON(t, srv, "/v1/sessions", token, createSessionBody())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected fail-open 201, got %d: %s", resp.StatusCode, body)
	}
	if metrics.RateLimitDegradedTotal(rateActionSession) != 1 {
		t.Fatalf("expected degraded metric, got %d", metrics.RateLimitDegradedTotal(rateActionSession))
	}
}

func TestDemoUsesStricterRateLimit(t *testing.T) {
	limiter := &fakeRateLimiter{allow: true}
	srv, _, token := rateLimitServer(t, limiter, RateLimitConfig{
		Window: time.Hour, Session: 20, Answer: 120, Report: 10,
		DemoSession: 3, DemoAnswer: 20, DemoReport: 3,
	})

	resp, body := postJSON(t, srv, "/v1/sessions", token, createSessionBody())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, body)
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if len(limiter.limits) != 1 || limiter.limits[0] != 3 {
		t.Fatalf("expected demo session limit 3, got %v", limiter.limits)
	}
}

func TestListRunsIsOwnerScopedAndReportsReason(t *testing.T) {
	store := memory.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authSvc, token, userID := testAuthFor(t, store)
	svc := app.New(store, app.Options{Logger: logger, Synchronous: true})
	srv := httptest.NewServer(NewRouter(Deps{Auth: authSvc, Service: svc, Logger: logger}))
	t.Cleanup(srv.Close)

	// Seed a dead run for the caller and one for another account.
	seedOwnedRun(t, store, "run_dead", "ses_dead", userID)
	seedOwnedRun(t, store, "run_other", "ses_other", "usr_other")
	if dead, _, err := store.Repos().Runs.RecordFailure(context.Background(),
		"run_dead", "", "step_failed", "internal", "planner exploded", time.Now().UTC(), 1); err != nil || !dead {
		t.Fatalf("seed dead run: dead=%v err=%v", dead, err)
	}
	if _, _, err := store.Repos().Runs.RecordFailure(context.Background(),
		"run_other", "", "step_failed", "internal", "other exploded", time.Now().UTC(), 1); err != nil {
		t.Fatalf("seed other dead run: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/runs?status=dead", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var payload struct {
		Runs []struct {
			RunID      string `json:"run_id"`
			Status     string `json:"status"`
			ErrorClass string `json:"error_class"`
			LastError  string `json:"last_error"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode body: %v (%s)", err, body)
	}
	if len(payload.Runs) != 1 || payload.Runs[0].RunID != "run_dead" {
		t.Fatalf("expected only the caller's dead run, got %+v", payload.Runs)
	}
	if payload.Runs[0].Status != "dead" || payload.Runs[0].ErrorClass != "internal" ||
		payload.Runs[0].LastError != "planner exploded" {
		t.Fatalf("unexpected dead run payload: %+v", payload.Runs[0])
	}
}
