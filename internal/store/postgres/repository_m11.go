package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/waiting2050/ai-interview-agent/internal/domain/review"
)

type reviewRepository struct{ q querier }

const reviewColumns = `id, session_id, turn_id, topic, question, answer_digest, key_points,
	dimension, ease_factor, interval_days, repetitions, due_at, last_grade,
	last_reviewed_at, last_calculation, created_at, updated_at`

func (r *reviewRepository) SaveMany(ctx context.Context, cards []*review.Card) error {
	for _, card := range cards {
		keyPoints, err := json.Marshal(card.KeyPoints)
		if err != nil {
			return fmt.Errorf("marshal review key points: %w", err)
		}
		calc, err := json.Marshal(card.LastCalculation)
		if err != nil {
			return fmt.Errorf("marshal review calculation: %w", err)
		}
		_, err = r.q.Exec(ctx, `
			INSERT INTO review_cards (`+reviewColumns+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
			ON CONFLICT (session_id, turn_id, topic) DO NOTHING`,
			card.ID, card.SessionID, card.TurnID, card.Topic, card.Question, card.AnswerDigest,
			string(keyPoints), card.Dimension, card.EaseFactor, card.IntervalDays,
			card.Repetitions, card.DueAt, card.LastGrade, card.LastReviewedAt, string(calc),
			card.CreatedAt, card.UpdatedAt)
		if err != nil {
			return fmt.Errorf("save review card: %w", err)
		}
	}
	return nil
}

func (r *reviewRepository) Get(ctx context.Context, id string) (*review.Card, error) {
	row := r.q.QueryRow(ctx, `SELECT `+reviewColumns+` FROM review_cards WHERE id = $1`, id)
	card, err := scanReviewCard(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, review.ErrNotFound
		}
		return nil, fmt.Errorf("get review card: %w", err)
	}
	return card, nil
}

func (r *reviewRepository) ListBySession(ctx context.Context, sessionID string) ([]*review.Card, error) {
	rows, err := r.q.Query(ctx, `SELECT `+reviewColumns+` FROM review_cards
		WHERE session_id = $1 ORDER BY created_at, id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list review cards: %w", err)
	}
	defer rows.Close()
	return collectReviewCards(rows)
}

func (r *reviewRepository) ListDue(ctx context.Context, before time.Time, limit int) ([]*review.Card, error) {
	rows, err := r.q.Query(ctx, `SELECT `+reviewColumns+` FROM review_cards
		WHERE due_at < $1 ORDER BY due_at, id LIMIT $2`, before, limit)
	if err != nil {
		return nil, fmt.Errorf("list due review cards: %w", err)
	}
	defer rows.Close()
	return collectReviewCards(rows)
}

func (r *reviewRepository) Update(ctx context.Context, card *review.Card) error {
	keyPoints, err := json.Marshal(card.KeyPoints)
	if err != nil {
		return fmt.Errorf("marshal review key points: %w", err)
	}
	calc, err := json.Marshal(card.LastCalculation)
	if err != nil {
		return fmt.Errorf("marshal review calculation: %w", err)
	}
	tag, err := r.q.Exec(ctx, `
		UPDATE review_cards SET
			key_points = $1, ease_factor = $2, interval_days = $3, repetitions = $4,
			due_at = $5, last_grade = $6, last_reviewed_at = $7, last_calculation = $8,
			updated_at = $9
		WHERE id = $10`,
		string(keyPoints), card.EaseFactor, card.IntervalDays, card.Repetitions,
		card.DueAt, card.LastGrade, card.LastReviewedAt, string(calc), card.UpdatedAt, card.ID)
	if err != nil {
		return fmt.Errorf("update review card: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return review.ErrNotFound
	}
	return nil
}

func collectReviewCards(rows pgx.Rows) ([]*review.Card, error) {
	cards := make([]*review.Card, 0)
	for rows.Next() {
		card, err := scanReviewCard(rows)
		if err != nil {
			return nil, err
		}
		cards = append(cards, card)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate review cards: %w", err)
	}
	return cards, nil
}

func scanReviewCard(row rowScanner) (*review.Card, error) {
	var (
		card       review.Card
		keyPoints  []byte
		calc       []byte
		lastReview pgtype.Timestamptz
	)
	if err := row.Scan(&card.ID, &card.SessionID, &card.TurnID, &card.Topic, &card.Question,
		&card.AnswerDigest, &keyPoints, &card.Dimension, &card.EaseFactor, &card.IntervalDays,
		&card.Repetitions, &card.DueAt, &card.LastGrade, &lastReview, &calc,
		&card.CreatedAt, &card.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(keyPoints, &card.KeyPoints); err != nil {
		return nil, fmt.Errorf("unmarshal review key points: %w", err)
	}
	if lastReview.Valid {
		at := lastReview.Time
		card.LastReviewedAt = &at
	}
	if len(calc) > 0 && card.LastGrade >= 0 {
		var parsed review.Calculation
		if err := json.Unmarshal(calc, &parsed); err != nil {
			return nil, fmt.Errorf("unmarshal review calculation: %w", err)
		}
		card.LastCalculation = &parsed
	}
	return &card, nil
}
