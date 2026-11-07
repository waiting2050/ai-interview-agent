-- Review upgrade: objective variant answers, explainable ranking and daily
-- limits. All columns are additive and nullable/defaulted so old cards keep
-- working.
ALTER TABLE review_cards ADD COLUMN IF NOT EXISTS source_score double precision NOT NULL DEFAULT 0;
ALTER TABLE review_cards ADD COLUMN IF NOT EXISTS key_topic boolean NOT NULL DEFAULT false;
ALTER TABLE review_cards ADD COLUMN IF NOT EXISTS last_objective_score integer;
ALTER TABLE review_cards ADD COLUMN IF NOT EXISTS last_objective_band integer;
ALTER TABLE review_cards ADD COLUMN IF NOT EXISTS last_self_grade integer;
ALTER TABLE review_cards ADD COLUMN IF NOT EXISTS last_divergence integer;
