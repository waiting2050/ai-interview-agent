-- M17: end-to-end trace correlation. The trace id is created at the HTTP
-- boundary and persisted with the run so workers and retries reuse it.
ALTER TABLE runs ADD COLUMN IF NOT EXISTS trace_id text NOT NULL DEFAULT '';
