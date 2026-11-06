-- M1 schema: sessions, runs, run_steps and the SSE event log.
-- turns / evaluations / reports land in later milestones.

CREATE TABLE IF NOT EXISTS sessions (
    id               text PRIMARY KEY,
    candidate_name   text NOT NULL,
    target_role      text NOT NULL,
    resume_text      text NOT NULL,
    jd_text          text NOT NULL,
    config           jsonb NOT NULL,
    status           text NOT NULL,
    phase            text NOT NULL DEFAULT '',
    state_version    bigint NOT NULL DEFAULT 0,
    pending_turn_id  text NOT NULL DEFAULT '',
    plan_cursor      integer NOT NULL DEFAULT 0,
    followups_used   integer NOT NULL DEFAULT 0,
    started_at       timestamptz,
    ended_at         timestamptz,
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS sessions_status_idx ON sessions (status);

CREATE TABLE IF NOT EXISTS runs (
    id               text PRIMARY KEY,
    session_id       text NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    kind             text NOT NULL,
    status           text NOT NULL,
    idempotency_key  text NOT NULL,
    request_hash     text NOT NULL,
    attempt          integer NOT NULL DEFAULT 0,
    error_code       text NOT NULL DEFAULT '',
    last_step        text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,
    finished_at      timestamptz,
    UNIQUE (session_id, kind, idempotency_key)
);

CREATE INDEX IF NOT EXISTS runs_status_idx ON runs (status, updated_at);
CREATE INDEX IF NOT EXISTS runs_session_idx ON runs (session_id, created_at);

CREATE TABLE IF NOT EXISTS run_steps (
    run_id        text NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    step_name     text NOT NULL,
    status        text NOT NULL,
    attempt       integer NOT NULL DEFAULT 0,
    input_hash    text NOT NULL DEFAULT '',
    output_json   jsonb,
    error_code    text NOT NULL DEFAULT '',
    started_at    timestamptz NOT NULL,
    finished_at   timestamptz,
    PRIMARY KEY (run_id, step_name)
);

CREATE TABLE IF NOT EXISTS session_events (
    event_id    bigserial PRIMARY KEY,
    session_id  text NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    run_id      text NOT NULL DEFAULT '',
    type        text NOT NULL,
    payload     jsonb NOT NULL,
    created_at  timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS session_events_session_idx ON session_events (session_id, event_id);
