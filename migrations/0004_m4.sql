-- M4: one structured report per session. structured holds the report JSON,
-- markdown holds the rendered deliverable.
CREATE TABLE IF NOT EXISTS reports (
    id             text PRIMARY KEY,
    session_id     text NOT NULL UNIQUE REFERENCES sessions(id) ON DELETE CASCADE,
    run_id         text NOT NULL DEFAULT '',
    status         text NOT NULL DEFAULT 'completed',
    schema_version integer NOT NULL DEFAULT 1,
    structured     jsonb NOT NULL DEFAULT '{}',
    markdown       text NOT NULL DEFAULT '',
    overall_score  double precision NOT NULL DEFAULT 0,
    degraded       boolean NOT NULL DEFAULT false,
    error_code     text NOT NULL DEFAULT '',
    model_name     text NOT NULL DEFAULT '',
    prompt_version text NOT NULL DEFAULT '',
    tool_calls     integer NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL,
    updated_at     timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS reports_created_at_idx ON reports (created_at DESC);
