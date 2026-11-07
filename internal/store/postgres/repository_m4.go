package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	domainreport "github.com/waiting2050/ai-interview-agent/internal/domain/report"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

type reportRepository struct{ q querier }

const reportColumns = `id, session_id, run_id, status, schema_version, structured, markdown,
	overall_score, degraded, error_code, model_name, prompt_version, tool_calls, created_at, updated_at`

func (r *reportRepository) Create(ctx context.Context, rec *domainreport.Record) (bool, *domainreport.Record, error) {
	structured, err := json.Marshal(rec.Report)
	if err != nil {
		return false, nil, fmt.Errorf("marshal report: %w", err)
	}
	tag, err := r.q.Exec(ctx, `
		INSERT INTO reports (`+reportColumns+`, user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
			(SELECT user_id FROM sessions WHERE id = $2))
		ON CONFLICT (session_id) DO NOTHING`,
		rec.ID, rec.SessionID, rec.RunID, rec.Status, rec.Report.SchemaVersion, string(structured), rec.Markdown,
		rec.Report.OverallScore, rec.Report.Degraded, rec.Report.ErrorCode, rec.Report.ModelName,
		rec.Report.PromptVersion, rec.Report.ToolCalls, rec.CreatedAt, rec.UpdatedAt)
	if err != nil {
		return false, nil, fmt.Errorf("insert report: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return true, nil, nil
	}

	existing, err := r.GetBySession(ctx, rec.SessionID)
	if err != nil {
		return false, nil, err
	}
	return false, existing, nil
}

func (r *reportRepository) GetBySession(ctx context.Context, sessionID string) (*domainreport.Record, error) {
	row := r.q.QueryRow(ctx, `SELECT `+reportColumns+` FROM reports WHERE session_id = $1`, sessionID)

	var (
		rec        domainreport.Record
		structured []byte
	)
	if err := row.Scan(
		&rec.ID, &rec.SessionID, &rec.RunID, &rec.Status, &rec.Report.SchemaVersion, &structured, &rec.Markdown,
		&rec.Report.OverallScore, &rec.Report.Degraded, &rec.Report.ErrorCode, &rec.Report.ModelName,
		&rec.Report.PromptVersion, &rec.Report.ToolCalls, &rec.CreatedAt, &rec.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ports.ErrNotFound
		}
		return nil, fmt.Errorf("get report: %w", err)
	}
	if len(structured) > 0 {
		if err := json.Unmarshal(structured, &rec.Report); err != nil {
			return nil, fmt.Errorf("decode report: %w", err)
		}
	}
	return &rec, nil
}
