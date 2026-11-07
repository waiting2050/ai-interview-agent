-- Records which scoring source produced the last review grade:
-- objective (real model), length_heuristic (heuristic reference only) or
-- self_only (no evaluation chain). Existing rows stay NULL: their score source
-- is unknown, so they must never be relabelled as objective.
ALTER TABLE review_cards ADD COLUMN IF NOT EXISTS last_scoring_mode text;
