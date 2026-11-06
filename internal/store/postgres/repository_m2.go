package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

type turnRepository struct{ q querier }

const turnColumns = `id, session_id, seq, kind, parent_turn_id, topic, depth, question,
	expected_points, status, answer_text, answer_hash, answered_at, created_at, updated_at`

func (r *turnRepository) Create(ctx context.Context, turn *session.Turn) error {
	points, err := json.Marshal(turn.ExpectedPoints)
	if err != nil {
		return fmt.Errorf("marshal expected points: %w", err)
	}
	_, err = r.q.Exec(ctx, `
		INSERT INTO turns (`+turnColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		turn.ID, turn.SessionID, turn.Seq, string(turn.Kind), turn.ParentTurnID, turn.Topic, turn.Depth, turn.Question,
		points, string(turn.Status), turn.AnswerText, turn.AnswerHash, turn.AnsweredAt, turn.CreatedAt, turn.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ports.ErrStateConflict
		}
		return fmt.Errorf("insert turn: %w", err)
	}
	return nil
}

func (r *turnRepository) Get(ctx context.Context, id string) (*session.Turn, error) {
	row := r.q.QueryRow(ctx, `SELECT `+turnColumns+` FROM turns WHERE id = $1`, id)
	turn, err := scanTurn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get turn: %w", err)
	}
	return turn, nil
}

func (r *turnRepository) UpdateAnswer(ctx context.Context, turn *session.Turn) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE turns
		SET status = $2, answer_text = $3, answer_hash = $4, answered_at = $5, updated_at = $6
		WHERE id = $1 AND status = 'pending'`,
		turn.ID, string(turn.Status), turn.AnswerText, turn.AnswerHash, turn.AnsweredAt, turn.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update turn answer: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ports.ErrStateConflict
	}
	return nil
}

func (r *turnRepository) ListBySession(ctx context.Context, sessionID string) ([]session.Turn, error) {
	rows, err := r.q.Query(ctx, `SELECT `+turnColumns+` FROM turns WHERE session_id = $1 ORDER BY seq`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list turns: %w", err)
	}
	defer rows.Close()

	var turns []session.Turn
	for rows.Next() {
		turn, err := scanTurn(rows)
		if err != nil {
			return nil, fmt.Errorf("scan turn: %w", err)
		}
		turns = append(turns, *turn)
	}
	return turns, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTurn(row rowScanner) (*session.Turn, error) {
	var (
		turn      session.Turn
		kind      string
		status    string
		pointsRaw []byte
	)
	if err := row.Scan(
		&turn.ID, &turn.SessionID, &turn.Seq, &kind, &turn.ParentTurnID, &turn.Topic, &turn.Depth, &turn.Question,
		&pointsRaw, &status, &turn.AnswerText, &turn.AnswerHash, &turn.AnsweredAt, &turn.CreatedAt, &turn.UpdatedAt,
	); err != nil {
		return nil, err
	}
	turn.Kind = session.TurnKind(kind)
	turn.Status = session.TurnStatus(status)
	if len(pointsRaw) > 0 {
		if err := json.Unmarshal(pointsRaw, &turn.ExpectedPoints); err != nil {
			return nil, fmt.Errorf("decode expected points: %w", err)
		}
	}
	return &turn, nil
}

type planRepository struct{ q querier }

func (r *planRepository) CreateItems(ctx context.Context, items []session.PlanItem) error {
	for _, item := range items {
		points, err := json.Marshal(item.ExpectedPoints)
		if err != nil {
			return fmt.Errorf("marshal plan expected points: %w", err)
		}
		if _, err := r.q.Exec(ctx, `
			INSERT INTO plan_items (id, session_id, seq, topic, objective, expected_points, difficulty, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (session_id, seq) DO NOTHING`,
			item.ID, item.SessionID, item.Seq, item.Topic, item.Objective, points, item.Difficulty, item.Status); err != nil {
			return fmt.Errorf("insert plan item: %w", err)
		}
	}
	return nil
}

func (r *planRepository) ListBySession(ctx context.Context, sessionID string) ([]session.PlanItem, error) {
	rows, err := r.q.Query(ctx, `
		SELECT id, session_id, seq, topic, objective, expected_points, difficulty, status
		FROM plan_items WHERE session_id = $1 ORDER BY seq`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list plan items: %w", err)
	}
	defer rows.Close()

	var items []session.PlanItem
	for rows.Next() {
		var (
			item   session.PlanItem
			points []byte
		)
		if err := rows.Scan(&item.ID, &item.SessionID, &item.Seq, &item.Topic, &item.Objective,
			&points, &item.Difficulty, &item.Status); err != nil {
			return nil, fmt.Errorf("scan plan item: %w", err)
		}
		if len(points) > 0 {
			if err := json.Unmarshal(points, &item.ExpectedPoints); err != nil {
				return nil, fmt.Errorf("decode plan expected points: %w", err)
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type evaluationRepository struct{ q querier }

func (r *evaluationRepository) Upsert(ctx context.Context, rec *evaluation.Record) error {
	scores, err := json.Marshal(rec.Scores)
	if err != nil {
		return fmt.Errorf("marshal scores: %w", err)
	}
	bonus, err := json.Marshal(rec.Bonus)
	if err != nil {
		return fmt.Errorf("marshal bonus: %w", err)
	}
	penalty, err := json.Marshal(rec.Penalty)
	if err != nil {
		return fmt.Errorf("marshal penalty: %w", err)
	}
	evidence, err := json.Marshal(rec.Evidence)
	if err != nil {
		return fmt.Errorf("marshal evidence: %w", err)
	}
	missing, err := json.Marshal(rec.MissingPoints)
	if err != nil {
		return fmt.Errorf("marshal missing points: %w", err)
	}

	_, err = r.q.Exec(ctx, `
		INSERT INTO evaluations (turn_id, session_id, schema_version, scores, base_score, weighted_score,
			bonus, penalty, evidence, missing_points, confidence, model_name, prompt_version,
			raw_output, degraded, error_code, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (turn_id) DO UPDATE SET
			schema_version = EXCLUDED.schema_version,
			scores = EXCLUDED.scores,
			base_score = EXCLUDED.base_score,
			weighted_score = EXCLUDED.weighted_score,
			bonus = EXCLUDED.bonus,
			penalty = EXCLUDED.penalty,
			evidence = EXCLUDED.evidence,
			missing_points = EXCLUDED.missing_points,
			confidence = EXCLUDED.confidence,
			model_name = EXCLUDED.model_name,
			prompt_version = EXCLUDED.prompt_version,
			raw_output = EXCLUDED.raw_output,
			degraded = EXCLUDED.degraded,
			error_code = EXCLUDED.error_code`,
		rec.TurnID, rec.SessionID, rec.SchemaVersion, scores, rec.BaseScore, rec.WeightedScore,
		bonus, penalty, evidence, missing, rec.Confidence, rec.ModelName, rec.PromptVersion,
		rec.RawOutput, rec.Degraded, rec.ErrorCode, rec.CreatedAt)
	if err != nil {
		return fmt.Errorf("upsert evaluation: %w", err)
	}
	return nil
}

func (r *evaluationRepository) GetByTurn(ctx context.Context, turnID string) (*evaluation.Record, error) {
	row := r.q.QueryRow(ctx, `
		SELECT turn_id, session_id, schema_version, scores, base_score, weighted_score,
			bonus, penalty, evidence, missing_points, confidence, model_name, prompt_version,
			raw_output, degraded, error_code, created_at
		FROM evaluations WHERE turn_id = $1`, turnID)
	rec, err := scanEvaluation(row)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

func (r *evaluationRepository) ListBySession(ctx context.Context, sessionID string) ([]evaluation.Record, error) {
	rows, err := r.q.Query(ctx, `
		SELECT turn_id, session_id, schema_version, scores, base_score, weighted_score,
			bonus, penalty, evidence, missing_points, confidence, model_name, prompt_version,
			raw_output, degraded, error_code, created_at
		FROM evaluations WHERE session_id = $1 ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list evaluations: %w", err)
	}
	defer rows.Close()

	var out []evaluation.Record
	for rows.Next() {
		rec, err := scanEvaluation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

func scanEvaluation(row rowScanner) (*evaluation.Record, error) {
	var (
		rec                    evaluation.Record
		scores, bonus, penalty []byte
		evidence, missing      []byte
	)
	if err := row.Scan(
		&rec.TurnID, &rec.SessionID, &rec.SchemaVersion, &scores, &rec.BaseScore, &rec.WeightedScore,
		&bonus, &penalty, &evidence, &missing, &rec.Confidence, &rec.ModelName, &rec.PromptVersion,
		&rec.RawOutput, &rec.Degraded, &rec.ErrorCode, &rec.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ports.ErrNotFound
		}
		return nil, fmt.Errorf("scan evaluation: %w", err)
	}
	_ = json.Unmarshal(scores, &rec.Scores)
	_ = json.Unmarshal(bonus, &rec.Bonus)
	_ = json.Unmarshal(penalty, &rec.Penalty)
	_ = json.Unmarshal(evidence, &rec.Evidence)
	_ = json.Unmarshal(missing, &rec.MissingPoints)
	return &rec, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
