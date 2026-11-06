// Package config loads runtime configuration from environment variables.
// PostgreSQL is the only runtime storage; Redis is reserved for the
// cross-process lock / idempotency fast path introduced in M5.
package config

import (
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
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:           env("HTTP_ADDR", ":8080"),
		DatabaseURL:        env("DATABASE_URL", "postgres://interview:interview@localhost:5433/interview?sslmode=disable"),
		RedisAddr:          env("REDIS_ADDR", "localhost:6380"),
		RedisPassword:      os.Getenv("REDIS_PASSWORD"),
		ShutdownTimeout:    10 * time.Second,
		RunStaleAfter:      60 * time.Second,
		LockTTL:            10 * time.Second,
		LockAcquireTimeout: 3 * time.Second,
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
