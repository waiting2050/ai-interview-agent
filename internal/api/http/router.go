package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

type Deps struct {
	Service *app.Service
	Health  func(ctx context.Context) error
	Logger  *slog.Logger
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
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /metrics", h.metrics)
	mux.HandleFunc("GET /demo", h.demoPage)
	mux.Handle("GET /demo/", demoStatic())
	mux.HandleFunc("POST /v1/resume/parse", h.parseResume)
	mux.HandleFunc("POST /v1/resume/analyze", h.analyzeResume)
	mux.HandleFunc("POST /v1/jd/analyze", h.analyzeJD)
	mux.HandleFunc("GET /v1/jd/{id}", h.getJD)
	mux.HandleFunc("POST /v1/jd/{id}/kit", h.generateApplyKit)
	mux.HandleFunc("GET /v1/review/due", h.listDueReviews)
	mux.HandleFunc("GET /v1/review", h.listReviews)
	mux.HandleFunc("GET /v1/review/{id}", h.getReview)
	mux.HandleFunc("GET /v1/review/{id}/retest", h.retestReview)
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

	return withRequestID(mux, deps.Logger)
}
