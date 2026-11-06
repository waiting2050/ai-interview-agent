package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

type sessionRepository struct{ q querier }

const sessionColumns = `id, candidate_name, target_role, resume_text, resume_profile, jd_text, config,
	status, phase, state_version, pending_turn_id, plan_cursor, followups_used,
	started_at, ended_at, created_at, updated_at`

func (r *sessionRepository) Create(ctx context.Context, s *session.Session) error {
	cfg, err := json.Marshal(s.Config)
	if err != nil {
		return fmt.Errorf("marshal session config: %w", err)
	}
	profile, err := json.Marshal(s.ResumeProfile)
	if err != nil {
		return fmt.Errorf("marshal resume profile: %w", err)
	}

	_, err = r.q.Exec(ctx, `
		INSERT INTO sessions (`+sessionColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		s.ID, s.CandidateName, s.TargetRole, s.ResumeText, string(profile), s.JDText, string(cfg),
		string(s.Status), string(s.Phase), s.StateVersion, s.PendingTurnID, s.PlanCursor, s.FollowupsUsed,
		s.StartedAt, s.EndedAt, s.CreatedAt, s.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (r *sessionRepository) Get(ctx context.Context, id string) (*session.Session, error) {
	row := r.q.QueryRow(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id = $1`, id)
	s, err := scanSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	return s, nil
}

func (r *sessionRepository) UpdateState(ctx context.Context, s *session.Session, expectedVersion uint64) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE sessions
		SET status = $3, phase = $4, state_version = state_version + 1,
		    pending_turn_id = $5, plan_cursor = $6, followups_used = $7,
		    started_at = $8, ended_at = $9, updated_at = $10
		WHERE id = $1 AND state_version = $2`,
		s.ID, expectedVersion, string(s.Status), string(s.Phase), s.PendingTurnID, s.PlanCursor,
		s.FollowupsUsed, s.StartedAt, s.EndedAt, s.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update session state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ports.ErrStateConflict
	}
	s.StateVersion = expectedVersion + 1
	return nil
}

func scanSession(row pgx.Row) (*session.Session, error) {
	var (
		s       session.Session
		cfg     []byte
		profile []byte
		status  string
		phase   string
	)
	if err := row.Scan(
		&s.ID, &s.CandidateName, &s.TargetRole, &s.ResumeText, &profile, &s.JDText, &cfg,
		&status, &phase, &s.StateVersion, &s.PendingTurnID, &s.PlanCursor, &s.FollowupsUsed,
		&s.StartedAt, &s.EndedAt, &s.CreatedAt, &s.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(cfg, &s.Config); err != nil {
		return nil, fmt.Errorf("unmarshal session config: %w", err)
	}
	if len(profile) > 0 {
		if err := json.Unmarshal(profile, &s.ResumeProfile); err != nil {
			return nil, fmt.Errorf("unmarshal resume profile: %w", err)
		}
	}
	s.Status = session.Status(status)
	s.Phase = session.Phase(phase)
	return &s, nil
}

type eventRepository struct{ q querier }

func (r *eventRepository) Append(ctx context.Context, event *session.Event) error {
	payload := string(event.Payload)
	if payload == "" {
		payload = "{}"
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}

	err := r.q.QueryRow(ctx, `
		INSERT INTO session_events (session_id, run_id, type, payload, created_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING event_id`,
		event.SessionID, event.RunID, event.Type, payload, event.CreatedAt,
	).Scan(&event.ID)
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	// Missing NOTIFY is the only correctness-critical failure here: SSE readers
	// would wait for a wake-up that never arrives. pg_notify inside the
	// transaction is delivered on commit and discarded on rollback.
	if _, err := r.q.Exec(ctx, `SELECT pg_notify($1, $2)`, eventChannel, event.SessionID); err != nil {
		return fmt.Errorf("notify event: %w", err)
	}
	return nil
}

func (r *eventRepository) ListAfter(ctx context.Context, sessionID string, afterID int64, limit int) ([]session.Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.q.Query(ctx, `
		SELECT event_id, session_id, run_id, type, payload, created_at
		FROM session_events
		WHERE session_id = $1 AND event_id > $2
		ORDER BY event_id
		LIMIT $3`, sessionID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()

	events := make([]session.Event, 0, limit)
	for rows.Next() {
		var e session.Event
		if err := rows.Scan(&e.ID, &e.SessionID, &e.RunID, &e.Type, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return events, nil
}

type runRepository struct{ q querier }

const runColumns = `id, session_id, kind, status, idempotency_key, request_hash,
	input_json, attempt, error_code, last_step, created_at, updated_at, finished_at`

func (r *runRepository) Create(ctx context.Context, run *session.Run) (bool, *session.Run, error) {
	input := run.Input
	if len(input) == 0 {
		input = []byte("{}")
	}
	tag, err := r.q.Exec(ctx, `
		INSERT INTO runs (`+runColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (session_id, kind, idempotency_key) DO NOTHING`,
		run.ID, run.SessionID, string(run.Kind), string(run.Status), run.IdempotencyKey, run.RequestHash,
		string(input), run.Attempt, run.ErrorCode, run.LastStep, run.CreatedAt, run.UpdatedAt, run.FinishedAt)
	if err != nil {
		return false, nil, fmt.Errorf("insert run: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return true, nil, nil
	}

	existing, err := r.GetByIdempotencyKey(ctx, run.SessionID, run.Kind, run.IdempotencyKey)
	if err != nil {
		return false, nil, err
	}
	return false, existing, nil
}

func (r *runRepository) Get(ctx context.Context, id string) (*session.Run, error) {
	row := r.q.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id = $1`, id)
	run, err := scanRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	return run, nil
}

func (r *runRepository) GetByIdempotencyKey(ctx context.Context, sessionID string, kind session.RunKind, key string) (*session.Run, error) {
	row := r.q.QueryRow(ctx, `
		SELECT `+runColumns+` FROM runs
		WHERE session_id = $1 AND kind = $2 AND idempotency_key = $3`,
		sessionID, string(kind), key)
	run, err := scanRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get run by idempotency key: %w", err)
	}
	return run, nil
}

func (r *runRepository) GetRunningBySession(ctx context.Context, sessionID string) (*session.Run, error) {
	row := r.q.QueryRow(ctx, `
		SELECT `+runColumns+` FROM runs
		WHERE session_id = $1 AND status = $2
		ORDER BY created_at
		LIMIT 1`, sessionID, string(session.RunStatusRunning))
	run, err := scanRun(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get running run: %w", err)
	}
	return run, nil
}

func (r *runRepository) SetLastStep(ctx context.Context, id, step string) error {
	_, err := r.q.Exec(ctx, `UPDATE runs SET last_step = $2, updated_at = $3 WHERE id = $1`,
		id, step, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("set run last step: %w", err)
	}
	return nil
}

func (r *runRepository) MarkRunning(ctx context.Context, id string, attempt int, at time.Time) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE runs
		SET status = $2, attempt = $3, updated_at = $4, finished_at = NULL
		WHERE id = $1`,
		id, string(session.RunStatusRunning), attempt, at)
	if err != nil {
		return fmt.Errorf("mark run running: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ports.ErrNotFound
	}
	return nil
}

func (r *runRepository) MarkFinished(ctx context.Context, id string, status session.RunStatus, errorCode string, at time.Time) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE runs
		SET status = $2, error_code = $3, updated_at = $4, finished_at = $4
		WHERE id = $1`,
		id, string(status), errorCode, at)
	if err != nil {
		return fmt.Errorf("mark run finished: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ports.ErrNotFound
	}
	return nil
}

func (r *runRepository) MarkInterrupted(ctx context.Context, id, lastStep string, at time.Time) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE runs
		SET status = $2, last_step = $3, updated_at = $4, finished_at = $4
		WHERE id = $1 AND status = $5`,
		id, string(session.RunStatusInterrupted), lastStep, at, string(session.RunStatusRunning))
	if err != nil {
		return fmt.Errorf("mark run interrupted: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ports.ErrStateConflict
	}
	return nil
}

func (r *runRepository) ListStaleRunning(ctx context.Context, staleBefore time.Time, limit int) ([]session.Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.q.Query(ctx, `
		SELECT `+runColumns+` FROM runs
		WHERE status = $1 AND updated_at < $2
		ORDER BY updated_at
		LIMIT $3`, string(session.RunStatusRunning), staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("list stale runs: %w", err)
	}
	defer rows.Close()

	runs := make([]session.Run, 0, limit)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan stale run: %w", err)
		}
		runs = append(runs, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale runs: %w", err)
	}
	return runs, nil
}

func scanRun(row pgx.Row) (*session.Run, error) {
	var (
		run    session.Run
		kind   string
		status string
	)
	if err := row.Scan(
		&run.ID, &run.SessionID, &kind, &status, &run.IdempotencyKey, &run.RequestHash,
		&run.Input, &run.Attempt, &run.ErrorCode, &run.LastStep, &run.CreatedAt, &run.UpdatedAt, &run.FinishedAt,
	); err != nil {
		return nil, err
	}
	run.Kind = session.RunKind(kind)
	run.Status = session.RunStatus(status)
	return &run, nil
}

type stepRepository struct{ q querier }

func (r *stepRepository) Get(ctx context.Context, runID, stepName string) (*session.RunStep, error) {
	row := r.q.QueryRow(ctx, `
		SELECT run_id, step_name, status, attempt, input_hash, output_json, error_code, started_at, finished_at
		FROM run_steps WHERE run_id = $1 AND step_name = $2`, runID, stepName)
	step, err := scanStep(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get run step: %w", err)
	}
	return step, nil
}

func (r *stepRepository) ListByRun(ctx context.Context, runID string) ([]session.RunStep, error) {
	rows, err := r.q.Query(ctx, `
		SELECT run_id, step_name, status, attempt, input_hash, output_json, error_code, started_at, finished_at
		FROM run_steps WHERE run_id = $1 ORDER BY started_at`, runID)
	if err != nil {
		return nil, fmt.Errorf("list run steps: %w", err)
	}
	defer rows.Close()

	steps := make([]session.RunStep, 0, 8)
	for rows.Next() {
		step, err := scanStep(rows)
		if err != nil {
			return nil, fmt.Errorf("scan run step: %w", err)
		}
		steps = append(steps, *step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run steps: %w", err)
	}
	return steps, nil
}

func (r *stepRepository) Start(ctx context.Context, step *session.RunStep) error {
	_, err := r.q.Exec(ctx, `
		INSERT INTO run_steps (run_id, step_name, status, attempt, input_hash, started_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (run_id, step_name) DO UPDATE
		SET status = $3, attempt = $4, input_hash = $5, started_at = $6,
		    output_json = NULL, error_code = '', finished_at = NULL`,
		step.RunID, step.StepName, string(step.Status), step.Attempt, step.InputHash, step.StartedAt)
	if err != nil {
		return fmt.Errorf("start run step: %w", err)
	}
	return nil
}

func (r *stepRepository) Complete(ctx context.Context, runID, stepName string, output []byte, at time.Time) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE run_steps
		SET status = $3, output_json = $4, finished_at = $5, error_code = ''
		WHERE run_id = $1 AND step_name = $2`,
		runID, stepName, string(session.StepStatusSucceeded), string(output), at)
	if err != nil {
		return fmt.Errorf("complete run step: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ports.ErrNotFound
	}
	return nil
}

func (r *stepRepository) Fail(ctx context.Context, runID, stepName, errorCode string, at time.Time) error {
	_, err := r.q.Exec(ctx, `
		UPDATE run_steps
		SET status = $3, error_code = $4, finished_at = $5
		WHERE run_id = $1 AND step_name = $2`,
		runID, stepName, string(session.StepStatusFailed), errorCode, at)
	if err != nil {
		return fmt.Errorf("fail run step: %w", err)
	}
	return nil
}

func scanStep(row pgx.Row) (*session.RunStep, error) {
	var (
		step   session.RunStep
		status string
	)
	if err := row.Scan(
		&step.RunID, &step.StepName, &status, &step.Attempt, &step.InputHash,
		&step.Output, &step.ErrorCode, &step.StartedAt, &step.FinishedAt,
	); err != nil {
		return nil, err
	}
	step.Status = session.StepStatus(status)
	return &step, nil
}
