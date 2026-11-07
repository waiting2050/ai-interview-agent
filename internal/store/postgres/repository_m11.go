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

const reviewColumns = `id, user_id, session_id, turn_id, topic, question, answer_digest, key_points,
	dimension, source_score, key_topic, last_objective_score, last_objective_band,
	last_self_grade, last_divergence, last_scoring_mode, ease_factor, interval_days, repetitions, due_at, last_grade,
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
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)
			ON CONFLICT (session_id, turn_id, topic) DO NOTHING`,
			card.ID, card.UserID, card.SessionID, card.TurnID, card.Topic, card.Question, card.AnswerDigest,
			string(keyPoints), card.Dimension, card.SourceScore, card.KeyTopic,
			nullableInt(card.LastObjectiveScore), nullableInt(card.LastObjectiveBand),
			nullableInt(card.LastSelfGrade), nullableInt(card.LastDivergence), card.LastScoringMode,
			card.EaseFactor, card.IntervalDays, card.Repetitions, card.DueAt, card.LastGrade,
			card.LastReviewedAt, string(calc), card.CreatedAt, card.UpdatedAt)
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

func (r *reviewRepository) ListDue(ctx context.Context, userID string, before time.Time, limit int) ([]*review.Card, error) {
	query := `SELECT ` + reviewColumns + ` FROM review_cards
		WHERE user_id = $1 AND due_at < $2 ORDER BY due_at, id`
	args := []any{userID, before}
	if limit > 0 {
		query += ` LIMIT $3`
		args = append(args, limit)
	}
	rows, err := r.q.Query(ctx, query, args...)
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
			last_objective_score = $9, last_objective_band = $10,
			last_self_grade = $11, last_divergence = $12, last_scoring_mode = $13,
			updated_at = $14
		WHERE id = $15`,
		string(keyPoints), card.EaseFactor, card.IntervalDays, card.Repetitions,
		card.DueAt, card.LastGrade, card.LastReviewedAt, string(calc),
		nullableInt(card.LastObjectiveScore), nullableInt(card.LastObjectiveBand),
		nullableInt(card.LastSelfGrade), nullableInt(card.LastDivergence),
		card.LastScoringMode, card.UpdatedAt, card.ID)
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
		objScore   pgtype.Int4
		objBand    pgtype.Int4
		selfGrade  pgtype.Int4
		divergence pgtype.Int4
		scoring    pgtype.Text
	)
	if err := row.Scan(&card.ID, &card.UserID, &card.SessionID, &card.TurnID, &card.Topic, &card.Question,
		&card.AnswerDigest, &keyPoints, &card.Dimension, &card.SourceScore, &card.KeyTopic,
		&objScore, &objBand, &selfGrade, &divergence, &scoring,
		&card.EaseFactor, &card.IntervalDays, &card.Repetitions, &card.DueAt, &card.LastGrade,
		&lastReview, &calc, &card.CreatedAt, &card.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(keyPoints, &card.KeyPoints); err != nil {
		return nil, fmt.Errorf("unmarshal review key points: %w", err)
	}
	if lastReview.Valid {
		at := lastReview.Time
		card.LastReviewedAt = &at
	}
	if objScore.Valid {
		v := int(objScore.Int32)
		card.LastObjectiveScore = &v
	}
	if objBand.Valid {
		v := int(objBand.Int32)
		card.LastObjectiveBand = &v
	}
	if selfGrade.Valid {
		v := int(selfGrade.Int32)
		card.LastSelfGrade = &v
	}
	if divergence.Valid {
		v := int(divergence.Int32)
		card.LastDivergence = &v
	}
	if scoring.Valid {
		card.LastScoringMode = scoring.String
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

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
