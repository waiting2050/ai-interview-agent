// Package config loads runtime configuration from environment variables.
// PostgreSQL is the only runtime storage; Redis is reserved for the
// cross-process lock / idempotency fast path introduced in M5.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	RedisAddr       string
	RedisPassword   string
	RedisDB         int
	ShutdownTimeout time.Duration

	// RunStaleAfter is how long a running run may go without an update before
	// startup reconciliation treats it as interrupted by a crash.
	RunStaleAfter time.Duration
	// LockTTL bounds how long a crashed instance can hold a session lock.
	LockTTL time.Duration
	// LockAcquireTimeout bounds how long a command waits for a busy lock.
	LockAcquireTimeout time.Duration

	// JWTSecret signs access tokens. It comes from JWT_SECRET; when unset a
	// random per-process secret is generated and JWTSecretGenerated is true so
	// startup can warn that tokens will not survive a restart.
	JWTSecret string
	// JWTSecretGenerated reports that JWTSecret was not provided by the
	// environment.
	JWTSecretGenerated bool
	// JWTTTL is the access-token lifetime.
	JWTTTL time.Duration
	// DemoLoginLimit caps anonymous demo logins per source IP inside
	// DemoLoginWindow.
	DemoLoginLimit  int
	DemoLoginWindow time.Duration

	// WorkerID identifies this instance in runs.lease_worker and logs. Empty
	// means hostname-pid-ulid is generated at startup.
	WorkerID string
	// WorkerConcurrency caps runs executed at the same time by this instance.
	WorkerConcurrency int
	// WorkerLeaseTTL is how long a claim survives without a heartbeat.
	WorkerLeaseTTL time.Duration
	// WorkerHeartbeatInterval renews a claim while the run executes.
	WorkerHeartbeatInterval time.Duration
	// WorkerPollInterval is the idle claim interval.
	WorkerPollInterval time.Duration
	// RunMaxFailures is the consecutive-failure threshold for dead letters.
	RunMaxFailures int

	// RateLimitWindow is the fixed user quota window.
	RateLimitWindow time.Duration
	// Per-user command quotas: create session, submit answer, generate report.
	RateLimitSession int
	RateLimitAnswer  int
	RateLimitReport  int
	// Demo accounts use stricter quotas.
	DemoRateLimitSession int
	DemoRateLimitAnswer  int
	DemoRateLimitReport  int
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:                env("HTTP_ADDR", ":8080"),
		DatabaseURL:             env("DATABASE_URL", "postgres://interview:interview@localhost:5433/interview?sslmode=disable"),
		RedisAddr:               env("REDIS_ADDR", "localhost:6380"),
		RedisPassword:           os.Getenv("REDIS_PASSWORD"),
		ShutdownTimeout:         10 * time.Second,
		RunStaleAfter:           60 * time.Second,
		LockTTL:                 10 * time.Second,
		LockAcquireTimeout:      3 * time.Second,
		JWTTTL:                  24 * time.Hour,
		DemoLoginLimit:          5,
		DemoLoginWindow:         time.Hour,
		WorkerConcurrency:       2,
		WorkerLeaseTTL:          30 * time.Second,
		WorkerHeartbeatInterval: 10 * time.Second,
		WorkerPollInterval:      time.Second,
		RunMaxFailures:          3,
		RateLimitWindow:         time.Hour,
		RateLimitSession:        20,
		RateLimitAnswer:         120,
		RateLimitReport:         10,
		DemoRateLimitSession:    3,
		DemoRateLimitAnswer:     20,
		DemoRateLimitReport:     3,
	}

	var err error
	if cfg.RunStaleAfter, err = durationEnv("RUN_STALE_AFTER", cfg.RunStaleAfter); err != nil {
		return Config{}, err
	}
	if cfg.LockTTL, err = durationEnv("LOCK_TTL", cfg.LockTTL); err != nil {
		return Config{}, err
	}
	if cfg.LockAcquireTimeout, err = durationEnv("LOCK_ACQUIRE_TIMEOUT", cfg.LockAcquireTimeout); err != nil {
		return Config{}, err
	}
	if cfg.RedisDB, err = intEnv("REDIS_DB", 0); err != nil {
		return Config{}, err
	}
	if cfg.JWTTTL, err = durationEnv("JWT_TTL", cfg.JWTTTL); err != nil {
		return Config{}, err
	}
	if cfg.DemoLoginLimit, err = intEnv("DEMO_LOGIN_LIMIT", cfg.DemoLoginLimit); err != nil {
		return Config{}, err
	}
	if cfg.DemoLoginWindow, err = durationEnv("DEMO_LOGIN_WINDOW", cfg.DemoLoginWindow); err != nil {
		return Config{}, err
	}
	if cfg.WorkerLeaseTTL, err = durationEnv("WORKER_LEASE_TTL", cfg.WorkerLeaseTTL); err != nil {
		return Config{}, err
	}
	if cfg.WorkerHeartbeatInterval, err = durationEnv("WORKER_HEARTBEAT_INTERVAL", cfg.WorkerHeartbeatInterval); err != nil {
		return Config{}, err
	}
	if cfg.WorkerPollInterval, err = durationEnv("WORKER_POLL_INTERVAL", cfg.WorkerPollInterval); err != nil {
		return Config{}, err
	}
	if cfg.WorkerConcurrency, err = intEnv("WORKER_CONCURRENCY", cfg.WorkerConcurrency); err != nil {
		return Config{}, err
	}
	if cfg.RunMaxFailures, err = intEnv("RUN_MAX_FAILURES", cfg.RunMaxFailures); err != nil {
		return Config{}, err
	}
	if cfg.RateLimitWindow, err = durationEnv("RATE_LIMIT_WINDOW", cfg.RateLimitWindow); err != nil {
		return Config{}, err
	}
	if cfg.RateLimitSession, err = intEnv("RATE_LIMIT_SESSION", cfg.RateLimitSession); err != nil {
		return Config{}, err
	}
	if cfg.RateLimitAnswer, err = intEnv("RATE_LIMIT_ANSWER", cfg.RateLimitAnswer); err != nil {
		return Config{}, err
	}
	if cfg.RateLimitReport, err = intEnv("RATE_LIMIT_REPORT", cfg.RateLimitReport); err != nil {
		return Config{}, err
	}
	if cfg.DemoRateLimitSession, err = intEnv("DEMO_RATE_LIMIT_SESSION", cfg.DemoRateLimitSession); err != nil {
		return Config{}, err
	}
	if cfg.DemoRateLimitAnswer, err = intEnv("DEMO_RATE_LIMIT_ANSWER", cfg.DemoRateLimitAnswer); err != nil {
		return Config{}, err
	}
	if cfg.DemoRateLimitReport, err = intEnv("DEMO_RATE_LIMIT_REPORT", cfg.DemoRateLimitReport); err != nil {
		return Config{}, err
	}
	cfg.WorkerID = os.Getenv("WORKER_ID")
	if cfg.WorkerConcurrency <= 0 {
		cfg.WorkerConcurrency = 2
	}
	if cfg.RunMaxFailures <= 0 {
		cfg.RunMaxFailures = 3
	}

	cfg.JWTSecret = os.Getenv("JWT_SECRET")
	if cfg.JWTSecret == "" {
		// Never commit a default secret. A random temporary one keeps
		// local demos running while startup logs a loud warning.
		secret, err := randomSecret()
		if err != nil {
			return Config{}, fmt.Errorf("generate temporary JWT secret: %w", err)
		}
		cfg.JWTSecret = secret
		cfg.JWTSecretGenerated = true
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL must not be empty")
	}

	return cfg, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func randomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %s", key, d)
	}
	return d, nil
}

func intEnv(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s must not be negative, got %d", key, n)
	}
	return n, nil
}
