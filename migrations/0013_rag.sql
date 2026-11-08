-- M20: retrieval corpus and vector index. pgvector is required; the compose
-- file switches to the pgvector image so a fresh volume gets the extension.
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS knowledge_chunks (
    id           text PRIMARY KEY,
    source_path  text NOT NULL,
    section_key  text NOT NULL,
    source_kind  text NOT NULL,
    domain       text NOT NULL DEFAULT '',
    topic_id     text NOT NULL DEFAULT '',
    title        text NOT NULL,
    content      text NOT NULL,
    content_hash text NOT NULL,
    embedding    vector(1024),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_path, section_key)
);

-- m=16 / ef_construction=64 is the documented starting point for a corpus of
-- this size (~100 chunks); query-time ef_search is set per connection.
CREATE INDEX IF NOT EXISTS knowledge_chunks_embedding_idx
    ON knowledge_chunks USING hnsw (embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);

CREATE INDEX IF NOT EXISTS knowledge_chunks_domain_idx ON knowledge_chunks (domain);

-- One row per ingest attempt. Re-running with the same corpus hash and a
-- complete embedding set is a no-op, so the command is safe to repeat.
CREATE TABLE IF NOT EXISTS rag_ingest_runs (
    id              text PRIMARY KEY,
    corpus_hash     text NOT NULL,
    embedding_model text NOT NULL DEFAULT '',
    embedding_dim   int NOT NULL DEFAULT 0,
    chunks_total    int NOT NULL DEFAULT 0,
    chunks_embedded int NOT NULL DEFAULT 0,
    chunks_skipped  int NOT NULL DEFAULT 0,
    chunks_deleted  int NOT NULL DEFAULT 0,
    status          text NOT NULL,
    duration_ms     bigint NOT NULL DEFAULT 0,
    error           text NOT NULL DEFAULT '',
    started_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz
);

CREATE INDEX IF NOT EXISTS rag_ingest_runs_started_idx ON rag_ingest_runs (started_at DESC);
