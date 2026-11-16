-- M21 model budget: admission control for model spend.
--
-- Spend itself is NOT duplicated here. The authoritative ledger stays in
-- runs.prompt_tokens / runs.completion_tokens (migration 0015), so there is one
-- write path and no chance of a aggregate drifting from the ledger. This table
-- only stores limits, keyed by scope so user-level and session-level checks
-- share one read path.
CREATE TABLE IF NOT EXISTS model_budget_limits (
    scope        text   NOT NULL CHECK (scope IN ('user', 'session')),
    scope_id     text   NOT NULL,
    limit_tokens bigint NOT NULL CHECK (limit_tokens > 0),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, scope_id)
);

-- runs_session_idx (session_id, created_at) already exists; the session-level
-- SUM(prompt_tokens + completion_tokens) uses it. The user-level SUM starts
-- from sessions_user_idx (user_id, created_at DESC) and joins runs on
-- session_id, so no extra index is added here.
