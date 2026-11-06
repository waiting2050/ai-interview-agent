-- M3: structured resume profile and syllabus expected points.
-- plan_items gains the knowledge-point objective, expected coverage points and
-- difficulty so the coverage gate can advance a question deterministically.
-- turns.expected_points already exists from M2; the first primary turn now
-- copies the plan item's points instead of staying empty.

ALTER TABLE sessions ADD COLUMN IF NOT EXISTS resume_profile jsonb NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE plan_items ADD COLUMN IF NOT EXISTS objective text NOT NULL DEFAULT '';
ALTER TABLE plan_items ADD COLUMN IF NOT EXISTS expected_points jsonb NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE plan_items ADD COLUMN IF NOT EXISTS difficulty text NOT NULL DEFAULT 'mid';
