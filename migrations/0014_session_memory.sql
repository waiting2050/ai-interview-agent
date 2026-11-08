-- M20: layered memory. Structured state (sessions/turns/evaluations) stays the
-- source of truth; this table only holds the compressed older turns.
CREATE TABLE IF NOT EXISTS session_memories (
    session_id        text PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    summary           text NOT NULL DEFAULT '',
    upto_seq          int NOT NULL DEFAULT 0,
    covered_turns     int NOT NULL DEFAULT 0,
    prompt_version    text NOT NULL DEFAULT '',
    model             text NOT NULL DEFAULT '',
    prompt_tokens     int NOT NULL DEFAULT 0,
    completion_tokens int NOT NULL DEFAULT 0,
    degraded          boolean NOT NULL DEFAULT false,
    updated_at        timestamptz NOT NULL DEFAULT now()
);
