-- M15: cached job descriptions with their provenance so GET can replay the
-- same structured result without re-fetching the page.
CREATE TABLE IF NOT EXISTS jd_listings (
    id          text PRIMARY KEY,
    source_kind text NOT NULL,
    source_url  text NOT NULL DEFAULT '',
    fetched_at  timestamptz NOT NULL,
    raw_text    text NOT NULL,
    structured  jsonb NOT NULL,
    created_at  timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS jd_listings_source_url_idx ON jd_listings (source_url);
