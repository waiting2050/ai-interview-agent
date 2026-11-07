package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/waiting2050/ai-interview-agent/internal/domain/user"
)

type userRepository struct{ q querier }

const userColumns = `id, email, password_hash, is_demo, created_at`

func (r *userRepository) Create(ctx context.Context, u *user.User) error {
	_, err := r.q.Exec(ctx, `INSERT INTO users (`+userColumns+`) VALUES ($1,$2,$3,$4,$5)`,
		u.ID, u.Email, u.PasswordHash, u.IsDemo, u.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return user.ErrEmailTaken
		}
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (r *userRepository) GetByID(ctx context.Context, id string) (*user.User, error) {
	return r.scanOne(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id)
}

func (r *userRepository) GetByEmail(ctx context.Context, email string) (*user.User, error) {
	return r.scanOne(ctx, `SELECT `+userColumns+` FROM users WHERE lower(email) = lower($1)`, email)
}

func (r *userRepository) scanOne(ctx context.Context, sql string, arg any) (*user.User, error) {
	var u user.User
	if err := r.q.QueryRow(ctx, sql, arg).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.IsDemo, &u.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, user.ErrNotFound
		}
		return nil, fmt.Errorf("get user: %w", err)
	}
	return &u, nil
}
