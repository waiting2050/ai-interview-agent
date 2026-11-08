-- M20 run accounting: persist per-step and per-run model usage so a run can
-- answer "how much did this round cost and which step spent it". Cost is
-- stored in micro-CNY (1e-6 CNY); 0 means no price was configured.
ALTER TABLE run_steps ADD COLUMN IF NOT EXISTS prompt_tokens integer NOT NULL DEFAULT 0;
ALTER TABLE run_steps ADD COLUMN IF NOT EXISTS completion_tokens integer NOT NULL DEFAULT 0;
ALTER TABLE run_steps ADD COLUMN IF NOT EXISTS model_calls integer NOT NULL DEFAULT 0;
ALTER TABLE run_steps ADD COLUMN IF NOT EXISTS cost_micros bigint NOT NULL DEFAULT 0;

ALTER TABLE runs ADD COLUMN IF NOT EXISTS prompt_tokens integer NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS completion_tokens integer NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS model_calls integer NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN IF NOT EXISTS cost_micros bigint NOT NULL DEFAULT 0;
