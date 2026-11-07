package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/agent/report"
	httpapi "github.com/waiting2050/ai-interview-agent/internal/api/http"
	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/auth"
	"github.com/waiting2050/ai-interview-agent/internal/domain/jd"
	"github.com/waiting2050/ai-interview-agent/internal/domain/review"
	"github.com/waiting2050/ai-interview-agent/internal/platform/config"
	"github.com/waiting2050/ai-interview-agent/internal/platform/jobfetch"
	platformlock "github.com/waiting2050/ai-interview-agent/internal/platform/lock"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
	"github.com/waiting2050/ai-interview-agent/internal/store/postgres"
	"github.com/waiting2050/ai-interview-agent/internal/store/redis"
	"github.com/waiting2050/ai-interview-agent/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server exited", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	connectCtx, cancelConnect := context.WithTimeout(ctx, 10*time.Second)
	store, err := postgres.Open(connectCtx, cfg.DatabaseURL)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer store.Close()

	if err := migrations.Apply(ctx, store.Pool()); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	logger.Info("database ready", "migrations", "applied")

	// The answer graph is compiled at startup so wiring problems fail fast.
	// The model client is a real OpenAI-compatible provider when configured;
	// without a key it degrades to the deterministic heuristic model so demos
	// and unit tests need no credentials.
	modelClient, provider, err := llm.NewClientFromEnv(ctx, interview.NewHeuristicClient(), logger)
	if err != nil {
		return fmt.Errorf("assemble model client: %w", err)
	}
	if provider.Fallback {
		logger.Warn("model provider degraded", "provider", provider.Provider,
			"model", provider.Model, "base_url", provider.BaseURL, "reason", provider.Reason)
	} else {
		logger.Info("model provider ready", "provider", provider.Provider,
			"model", provider.Model, "base_url", provider.BaseURL)
	}

	answerGraph := interview.NewAnswerGraph(ctx)
	if err := answerGraph.Err(); err != nil {
		return fmt.Errorf("compile answer graph: %w", err)
	}
	logger.Info("answer graph compiled", "nodes", 5)

	metrics := obs.NewMetrics()
	usage := obs.NewUsageTracker()

	evaluator := interview.NewEvaluator(
		llm.NewInstrumentedClient(modelClient, provider.Provider, provider.Model, "evaluate", metrics, usage),
		provider.Model,
	)
	generator := interview.NewQuestionGenerator(
		llm.NewInstrumentedClient(modelClient, provider.Provider, provider.Model, "question", metrics, usage),
		provider.Model,
	)
	reportGenerator := report.NewBuilder(
		llm.NewInstrumentedClient(modelClient, provider.Provider, provider.Model, "report", metrics, usage),
		provider.Model,
	)

	// The session lock must be shared across instances. Without a reachable
	// Redis the process falls back to its in-process lock, which keeps a single
	// instance correct but does NOT protect concurrent writers in a cluster.
	var locker ports.Locker = platformlock.NewProcessLocker()
	redisLocker := redis.NewLocker(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB, redis.LockOptions{
		TTL:            cfg.LockTTL,
		AcquireTimeout: cfg.LockAcquireTimeout,
	})
	pingCtx, cancelPing := context.WithTimeout(ctx, 2*time.Second)
	pingErr := redisLocker.Ping(pingCtx)
	cancelPing()
	if pingErr != nil {
		logger.Warn("redis unavailable, falling back to in-process session lock; multi-instance locking disabled",
			"addr", cfg.RedisAddr, "error", pingErr)
		_ = redisLocker.Close()
	} else {
		locker = redisLocker
		defer redisLocker.Close()
		logger.Info("session lock ready", "backend", "redis", "addr", cfg.RedisAddr)
	}

	svc := app.New(store, app.Options{
		Logger:            logger,
		RunStaleAfter:     cfg.RunStaleAfter,
		Evaluator:         evaluator,
		QuestionGenerator: generator,
		AnswerGraph:       answerGraph,
		ReportGenerator:   reportGenerator,
		Locker:            locker,
		Metrics:           metrics,
		Usage:             usage,
		MaxRunFailures:    cfg.RunMaxFailures,
	})

	// Runs are executed by a PostgreSQL-backed worker pool: commands only
	// enqueue a run, and every instance claims work with SKIP LOCKED.
	workers := app.NewWorkerPool(svc, app.WorkerOptions{
		WorkerID:          cfg.WorkerID,
		Concurrency:       cfg.WorkerConcurrency,
		LeaseTTL:          cfg.WorkerLeaseTTL,
		HeartbeatInterval: cfg.WorkerHeartbeatInterval,
		PollInterval:      cfg.WorkerPollInterval,
		MaxFailures:       cfg.RunMaxFailures,
		Logger:            logger,
		Metrics:           metrics,
	})
	defer workers.Stop()

	// Reconcile before starting workers: stale runs are marked interrupted
	// (event preserved) and then claimed from the same queue as fresh work.
	if resumed, err := svc.Reconcile(ctx); err != nil {
		logger.Error("startup reconciliation failed", "error", err)
	} else if resumed > 0 {
		logger.Warn("interrupted runs queued for workers", "count", resumed)
	}
	workers.Start(ctx)
	logger.Info("worker pool started", "concurrency", cfg.WorkerConcurrency,
		"lease_ttl", cfg.WorkerLeaseTTL, "heartbeat", cfg.WorkerHeartbeatInterval)

	// One fetcher instance keeps the per-host rate limiter and robots cache
	// shared across requests, so frequent analyses cannot exceed the limit.
	jobFetcher := jobfetch.NewHTTPFetcher(jobfetch.Options{})

	signer, err := auth.NewSigner([]byte(cfg.JWTSecret), cfg.JWTTTL)
	if err != nil {
		return fmt.Errorf("create jwt signer: %w", err)
	}
	if cfg.JWTSecretGenerated {
		logger.Warn("JWT_SECRET is not set; using a random per-process secret. " +
			"Issued tokens stop working after a restart. Set JWT_SECRET in production.")
	}
	authSvc := auth.New(store.Repos().Users, signer)

	// User quotas live in Redis. If Redis is unreachable the limiter returns
	// errors and the HTTP layer explicitly fails open, logging and counting the
	// degradation (see RATE_LIMIT_DEGRADED metrics).
	rateLimiter := redis.NewRateLimiter(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	defer rateLimiter.Close()
	ratePingCtx, cancelRatePing := context.WithTimeout(ctx, 2*time.Second)
	if err := rateLimiter.Ping(ratePingCtx); err != nil {
		logger.Warn("redis unavailable, user rate limiting will fail open and be counted",
			"addr", cfg.RedisAddr, "error", err)
	} else {
		logger.Info("user rate limiter ready", "backend", "redis", "addr", cfg.RedisAddr)
	}
	cancelRatePing()

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Service:         svc,
			Auth:            authSvc,
			Health:          store.Ping,
			Logger:          logger,
			Metrics:         metrics,
			DemoLoginLimit:  cfg.DemoLoginLimit,
			DemoLoginWindow: cfg.DemoLoginWindow,
			RateLimiter:     rateLimiter,
			RateLimits: httpapi.RateLimitConfig{
				Window:      cfg.RateLimitWindow,
				Session:     cfg.RateLimitSession,
				Answer:      cfg.RateLimitAnswer,
				Report:      cfg.RateLimitReport,
				DemoSession: cfg.DemoRateLimitSession,
				DemoAnswer:  cfg.DemoRateLimitAnswer,
				DemoReport:  cfg.DemoRateLimitReport,
			},
			JDAnalyzer: jd.New(store.Repos().JDs, func(ctx context.Context, rawURL string) (jd.Fetched, error) {
				result, err := jobFetcher.Fetch(ctx, rawURL)
				if err != nil {
					return jd.Fetched{}, err
				}
				return jd.Fetched{URL: result.URL, Text: result.Text, FetchedAt: result.FetchedAt}, nil
			}),
			Reviewer:       review.New(store.Repos().Reviews),
			EvalResultsDir: cfg.EvalResultsDir,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		// ListenAndServe returns http.ErrServerClosed after a graceful shutdown.
		errCh <- srv.ListenAndServe()
	}()

	logger.Info("http server started", "addr", cfg.HTTPAddr, "storage", "postgres")

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	logger.Info("http server shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown http server: %w", err)
	}

	workers.Stop()
	waitDone := make(chan struct{})
	go func() {
		workers.Wait()
		svc.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		logger.Warn("timed out waiting for in-flight runs")
	}

	logger.Info("http server stopped")
	return nil
}
