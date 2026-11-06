-- M2 schema: interview plan items, turns, evaluations and the command
-- payload needed to resume an answer run after a crash.

ALTER TABLE runs ADD COLUMN IF NOT EXISTS input_json jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE TABLE IF NOT EXISTS plan_items (
    id          text PRIMARY KEY,
    session_id  text NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    seq         integer NOT NULL,
    topic       text NOT NULL,
    status      text NOT NULL DEFAULT 'pending',
    UNIQUE (session_id, seq)
);

CREATE TABLE IF NOT EXISTS turns (
    id              text PRIMARY KEY,
    session_id      text NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    seq             integer NOT NULL,
    kind            text NOT NULL,
    parent_turn_id  text NOT NULL DEFAULT '',
    topic           text NOT NULL,
    depth           integer NOT NULL DEFAULT 1,
    question        text NOT NULL,
    expected_points jsonb NOT NULL DEFAULT '[]'::jsonb,
    status          text NOT NULL,
    answer_text     text NOT NULL DEFAULT '',
    answer_hash     text NOT NULL DEFAULT '',
    answered_at     timestamptz,
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    UNIQUE (session_id, seq)
);

CREATE INDEX IF NOT EXISTS turns_session_status_idx ON turns (session_id, status);

CREATE TABLE IF NOT EXISTS evaluations (
    turn_id         text PRIMARY KEY REFERENCES turns (id) ON DELETE CASCADE,
    session_id      text NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    schema_version  integer NOT NULL DEFAULT 1,
    scores          jsonb NOT NULL DEFAULT '{}'::jsonb,
    base_score      double precision NOT NULL DEFAULT 0,
    weighted_score  double precision NOT NULL DEFAULT 0,
    bonus           jsonb NOT NULL DEFAULT '[]'::jsonb,
    penalty         jsonb NOT NULL DEFAULT '[]'::jsonb,
    evidence        jsonb NOT NULL DEFAULT '[]'::jsonb,
    missing_points  jsonb NOT NULL DEFAULT '[]'::jsonb,
    confidence      double precision NOT NULL DEFAULT 0,
    model_name      text NOT NULL DEFAULT '',
    prompt_version  text NOT NULL DEFAULT '',
    raw_output      text NOT NULL DEFAULT '',
    degraded        boolean NOT NULL DEFAULT false,
    error_code      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS evaluations_session_score_idx ON evaluations (session_id, weighted_score);
