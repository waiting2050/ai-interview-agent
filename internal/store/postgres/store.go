// Package postgres implements ports.Store with pgx. It is the only runtime
// storage implementation; the memory store is a test double used by unit tests.
package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

// querier is satisfied by both *pgxpool.Pool and pgx.Tx, so repositories work
// identically inside and outside a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
	repos    ports.Repositories
	pool     *pgxpool.Pool
	notifier *notifier
}

func (s *Store) Repos() ports.Repositories { return s.repos }

var _ ports.Store = (*Store)(nil)

func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = 10

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pgx pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	n := newNotifier(pool, slog.Default())
	n.start()

	return &Store{
		repos:    newRepositories(pool),
		pool:     pool,
		notifier: n,
	}, nil
}

// Pool exposes the pool for the migration runner. Application code must not
// use it; repositories are the intended boundary.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func (s *Store) WithTx(ctx context.Context, fn func(ctx context.Context, repos ports.Repositories) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(ctx, newRepositories(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Store) Close() {
	s.notifier.close()
	s.pool.Close()
}

func (s *Store) SubscribeEvents(ctx context.Context, sessionID string) (<-chan struct{}, error) {
	return s.notifier.Subscribe(ctx, sessionID)
}

func newRepositories(q querier) ports.Repositories {
	return ports.Repositories{
		Sessions:    &sessionRepository{q: q},
		Events:      &eventRepository{q: q},
		Runs:        &runRepository{q: q},
		Steps:       &stepRepository{q: q},
		Turns:       &turnRepository{q: q},
		Plans:       &planRepository{q: q},
		Evaluations: &evaluationRepository{q: q},
		Reports:     &reportRepository{q: q},
		JDs:         &jdRepository{q: q},
	}
}
