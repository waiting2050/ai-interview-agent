-- M11: spaced-repetition cards derived from a finished interview report.
-- Scheduling state is SM-2: ease_factor, interval_days, repetitions, due_at.
CREATE TABLE IF NOT EXISTS review_cards (
    id               text PRIMARY KEY,
    session_id       text NOT NULL,
    turn_id          text NOT NULL DEFAULT '',
    topic            text NOT NULL,
    question         text NOT NULL,
    answer_digest    text NOT NULL DEFAULT '',
    key_points       jsonb NOT NULL DEFAULT '[]',
    dimension        text NOT NULL DEFAULT '',
    ease_factor      double precision NOT NULL,
    interval_days    integer NOT NULL,
    repetitions      integer NOT NULL,
    due_at           timestamptz NOT NULL,
    last_grade       integer NOT NULL DEFAULT -1,
    last_reviewed_at timestamptz,
    last_calculation jsonb NOT NULL DEFAULT '{}',
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL
);

-- A replayed report commit must not duplicate cards; the schedule is kept.
CREATE UNIQUE INDEX IF NOT EXISTS review_cards_session_turn_topic_idx
    ON review_cards (session_id, turn_id, topic);
CREATE INDEX IF NOT EXISTS review_cards_due_idx ON review_cards (due_at);
CREATE INDEX IF NOT EXISTS review_cards_session_idx ON review_cards (session_id);
