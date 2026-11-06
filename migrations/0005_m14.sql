-- M14: each session names the domain pack that shaped its outline and rubric.
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS domain text NOT NULL DEFAULT 'go-backend';
