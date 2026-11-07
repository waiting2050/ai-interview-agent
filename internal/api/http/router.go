package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/auth"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

type Deps struct {
	Service *app.Service
	// Auth backs /v1/auth/* and verifies bearer tokens for every other /v1
	// route. Nil makes protected routes answer 503 rather than run unauthenticated.
	Auth   *auth.Service
	Health func(ctx context.Context) error
	Logger *slog.Logger
	// DemoLoginLimit caps anonymous demo logins per source IP inside
	// DemoLoginWindow. Zero falls back to five per hour.
	DemoLoginLimit  int
	DemoLoginWindow time.Duration
	// Metrics backs GET /metrics. Nil falls back to a fresh empty registry.
	Metrics *obs.Metrics
	// ResumeAnalyzer backs POST /v1/resume/analyze. Nil falls back to the
	// embedded deterministic rules engine.
	ResumeAnalyzer ports.ResumeAnalyzer
	// JDAnalyzer backs POST /v1/jd/analyze, GET /v1/jd/{id} and the M16
	// application kit. Nil disables those routes with 503.
	JDAnalyzer ports.JDAnalyzer
	// Reviewer backs the M11 spaced-repetition routes. Nil disables them with 503.
	Reviewer ports.Reviewer
	// RateLimiter backs the three per-user command quotas. Nil disables them.
	RateLimiter ports.RateLimiter
	// RateLimits holds the per-command quotas (demo accounts are stricter).
	RateLimits RateLimitConfig
	// EvalResultsDir backs the read-only /eval panel. Empty falls back to
	// docs/eval-results relative to the process working directory.
	EvalResultsDir string
}

// NewRouter wires the HTTP surface: health, metrics, session commands, answers
// and SSE.
func NewRouter(deps Deps) http.Handler {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Metrics == nil {
		deps.Metrics = obs.NewMetrics()
	}

	h := &handlers{deps: deps}
	limit := deps.DemoLoginLimit
	window := deps.DemoLoginWindow
	if limit <= 0 {
		limit = 5
	}
	if window <= 0 {
		window = time.Hour
	}
	h.demoLimiter = newDemoLimiter(limit, window)
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /metrics", h.metrics)
	mux.HandleFunc("GET /demo", h.demoPage)
	mux.Handle("GET /demo/", demoStatic())
	mux.HandleFunc("GET /eval", h.evalPanel)
	mux.HandleFunc("POST /v1/auth/register", h.register)
	mux.HandleFunc("POST /v1/auth/login", h.login)
	mux.HandleFunc("POST /v1/auth/demo", h.demoLogin)
	mux.HandleFunc("POST /v1/resume/parse", h.parseResume)
	mux.HandleFunc("POST /v1/resume/analyze", h.analyzeResume)
	mux.HandleFunc("POST /v1/jd/analyze", h.analyzeJD)
	mux.HandleFunc("GET /v1/jd/{id}", h.getJD)
	mux.HandleFunc("POST /v1/jd/{id}/kit", h.generateApplyKit)
	mux.HandleFunc("GET /v1/review/due", h.listDueReviews)
	mux.HandleFunc("GET /v1/review", h.listReviews)
	mux.HandleFunc("GET /v1/review/{id}", h.getReview)
	mux.HandleFunc("GET /v1/review/{id}/retest", h.retestReview)
	mux.HandleFunc("GET /v1/review/{id}/variant", h.variantReview)
	mux.HandleFunc("POST /v1/review/{id}/variant/answer", h.answerVariantReview)
	mux.HandleFunc("POST /v1/review/{id}/grade", h.gradeReview)
	mux.HandleFunc("GET /v1/domains", h.listDomains)
	mux.HandleFunc("POST /v1/sessions", h.createSession)
	mux.HandleFunc("GET /v1/sessions/{id}", h.getSession)
	mux.HandleFunc("POST /v1/sessions/{id}/start", h.startSession)
	mux.HandleFunc("POST /v1/sessions/{id}/answers", h.submitAnswer)
	mux.HandleFunc("POST /v1/sessions/{id}/abort", h.abortSession)
	mux.HandleFunc("POST /v1/sessions/{id}/finish", h.finishSession)
	mux.HandleFunc("POST /v1/sessions/{id}/report", h.generateReport)
	mux.HandleFunc("GET /v1/sessions/{id}/report", h.getReport)
	mux.HandleFunc("GET /v1/sessions/{id}/events", h.subscribeEvents)
	mux.HandleFunc("POST /v1/sessions/{id}/runs/{run_id}/retry", h.retryRun)
	mux.HandleFunc("GET /v1/runs", h.listRuns)

	return withRequestID(withAuth(mux, deps.Auth), deps.Logger)
}
