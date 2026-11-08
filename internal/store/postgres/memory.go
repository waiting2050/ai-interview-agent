package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/waiting2050/ai-interview-agent/internal/agent/memory"
)

// SessionMemory persists the compressed older-turn layer. It is separate from
// ports.Repositories because it is derived context, not a domain aggregate.
type SessionMemory struct {
	q querier
}

func NewSessionMemory(pool *pgxpool.Pool) *SessionMemory {
	if pool == nil {
		return nil
	}
	return &SessionMemory{q: pool}
}

// Load returns the stored summary and whether a row exists.
func (m *SessionMemory) Load(ctx context.Context, sessionID string) (memory.State, bool, error) {
	if m == nil {
		return memory.State{}, false, nil
	}
	var state memory.State
	err := m.q.QueryRow(ctx, `
		SELECT summary, upto_seq, covered_turns, prompt_version, model,
		       prompt_tokens, completion_tokens, degraded, updated_at
		FROM session_memories
		WHERE session_id = $1`, sessionID).Scan(
		&state.Summary, &state.UptoSeq, &state.CoveredTurns, &state.PromptVersion,
		&state.Model, &state.PromptTokens, &state.CompletionTokens, &state.Degraded, &state.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return memory.State{}, false, nil
	}
	if err != nil {
		return memory.State{}, false, fmt.Errorf("load session memory: %w", err)
	}
	return state, true, nil
}

// Save upserts the summary. A write is idempotent for the same session and
// boundary, so a retried answer run does not create a second summary.
func (m *SessionMemory) Save(ctx context.Context, sessionID string, state memory.State) error {
	if m == nil {
		return nil
	}
	_, err := m.q.Exec(ctx, `
		INSERT INTO session_memories
			(session_id, summary, upto_seq, covered_turns, prompt_version, model,
			 prompt_tokens, completion_tokens, degraded, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, now())
		ON CONFLICT (session_id) DO UPDATE SET
			summary = EXCLUDED.summary,
			upto_seq = EXCLUDED.upto_seq,
			covered_turns = EXCLUDED.covered_turns,
			prompt_version = EXCLUDED.prompt_version,
			model = EXCLUDED.model,
			prompt_tokens = EXCLUDED.prompt_tokens,
			completion_tokens = EXCLUDED.completion_tokens,
			degraded = EXCLUDED.degraded,
			updated_at = now()`,
		sessionID, state.Summary, state.UptoSeq, state.CoveredTurns, state.PromptVersion,
		state.Model, state.PromptTokens, state.CompletionTokens, state.Degraded)
	if err != nil {
		return fmt.Errorf("save session memory: %w", err)
	}
	return nil
}
