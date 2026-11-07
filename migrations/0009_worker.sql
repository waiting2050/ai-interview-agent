-- Cross-instance run claiming, lease-based recovery and dead letters.
-- Runs continue to be created as 'running' with an empty lease, so a queued
-- run is simply "running without an active lease". Workers claim it with
-- SELECT ... FOR UPDATE SKIP LOCKED and stamp a lease on it.

ALTER TABLE runs ADD COLUMN IF NOT EXISTS lease_id text NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS lease_worker text NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS lease_expires_at timestamptz;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS failure_count integer NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS last_error text NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS last_error_class text NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN IF NOT EXISTS dead_at timestamptz;

-- Migration policy for existing rows: lease columns default to empty/NULL,
-- failure counters default to zero. A run that was 'running' before this
-- migration is therefore claimable immediately; startup reconciliation still
-- marks runs whose lease/updated_at is stale as 'interrupted' first so the
-- interrupted event is preserved. No historical run is dead-lettered.

-- Claim scan: queued or expired runs, oldest first.
CREATE INDEX IF NOT EXISTS runs_claim_idx
    ON runs (status, lease_expires_at, updated_at);

-- Dead-letter listing, scoped by owner.
CREATE INDEX IF NOT EXISTS runs_dead_idx
    ON runs (user_id, dead_at DESC)
    WHERE status = 'dead';
