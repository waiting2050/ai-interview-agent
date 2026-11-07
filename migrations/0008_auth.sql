-- M17: minimal user accounts and per-user resource ownership.
--
-- Migration strategy for pre-existing data: every row created before this
-- migration is assigned to the placeholder user `usr_legacy`. That user has an
-- empty password hash and can never be obtained through login, so historical
-- data stays readable by the migration/backfill path but is not reachable by
-- any newly registered account. New rows must name an explicit owner.
--
-- `user_id` is added with a default only so the backfill can run, then the
-- default is dropped so a forgotten owner fails loudly instead of silently
-- writing a row into the legacy bucket.

CREATE TABLE IF NOT EXISTS users (
    id            text PRIMARY KEY,
    email         text NOT NULL,
    password_hash text NOT NULL DEFAULT '',
    is_demo       boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS users_email_idx ON users (lower(email));

INSERT INTO users (id, email, password_hash, is_demo, created_at)
VALUES ('usr_legacy', 'legacy@placeholder.local', '', false, now())
ON CONFLICT (id) DO NOTHING;

-- sessions is the ownership root; the other tables are reachable only through
-- it, but they still carry user_id so a row can be traced to its owner without
-- a join.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT 'usr_legacy';
UPDATE sessions SET user_id = 'usr_legacy' WHERE user_id = '';
ALTER TABLE sessions ALTER COLUMN user_id DROP DEFAULT;
CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions (user_id, created_at DESC);

ALTER TABLE runs ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT 'usr_legacy';
UPDATE runs r SET user_id = s.user_id FROM sessions s WHERE r.session_id = s.id;
ALTER TABLE runs ALTER COLUMN user_id DROP DEFAULT;
CREATE INDEX IF NOT EXISTS runs_user_idx ON runs (user_id, created_at DESC);

ALTER TABLE run_steps ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT 'usr_legacy';
UPDATE run_steps rs SET user_id = r.user_id FROM runs r WHERE rs.run_id = r.id;
ALTER TABLE run_steps ALTER COLUMN user_id DROP DEFAULT;
CREATE INDEX IF NOT EXISTS run_steps_user_idx ON run_steps (user_id);

ALTER TABLE session_events ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT 'usr_legacy';
UPDATE session_events e SET user_id = s.user_id FROM sessions s WHERE e.session_id = s.id;
ALTER TABLE session_events ALTER COLUMN user_id DROP DEFAULT;
CREATE INDEX IF NOT EXISTS session_events_user_idx ON session_events (user_id, event_id);

ALTER TABLE turns ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT 'usr_legacy';
UPDATE turns t SET user_id = s.user_id FROM sessions s WHERE t.session_id = s.id;
ALTER TABLE turns ALTER COLUMN user_id DROP DEFAULT;
CREATE INDEX IF NOT EXISTS turns_user_idx ON turns (user_id, session_id);

ALTER TABLE evaluations ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT 'usr_legacy';
UPDATE evaluations e SET user_id = s.user_id FROM sessions s WHERE e.session_id = s.id;
ALTER TABLE evaluations ALTER COLUMN user_id DROP DEFAULT;
CREATE INDEX IF NOT EXISTS evaluations_user_idx ON evaluations (user_id, session_id);

ALTER TABLE reports ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT 'usr_legacy';
UPDATE reports r SET user_id = s.user_id FROM sessions s WHERE r.session_id = s.id;
ALTER TABLE reports ALTER COLUMN user_id DROP DEFAULT;
CREATE INDEX IF NOT EXISTS reports_user_idx ON reports (user_id, created_at DESC);

ALTER TABLE review_cards ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT 'usr_legacy';
UPDATE review_cards c SET user_id = s.user_id FROM sessions s WHERE c.session_id = s.id;
ALTER TABLE review_cards ALTER COLUMN user_id DROP DEFAULT;
CREATE INDEX IF NOT EXISTS review_cards_user_due_idx ON review_cards (user_id, due_at);

ALTER TABLE jd_listings ADD COLUMN IF NOT EXISTS user_id text NOT NULL DEFAULT 'usr_legacy';
UPDATE jd_listings SET user_id = 'usr_legacy' WHERE user_id = '';
ALTER TABLE jd_listings ALTER COLUMN user_id DROP DEFAULT;
CREATE INDEX IF NOT EXISTS jd_listings_user_idx ON jd_listings (user_id, created_at DESC);
