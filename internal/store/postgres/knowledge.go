package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/waiting2050/ai-interview-agent/internal/agent/knowledge"
)

// Knowledge is the retrieval persistence adapter. It is constructed from the
// same pool as the business store but deliberately not part of
// ports.Repositories: retrieval is an agent capability, not a domain
// aggregate.
type Knowledge struct {
	q querier
}

func NewKnowledge(pool *pgxpool.Pool) *Knowledge {
	if pool == nil {
		return nil
	}
	return &Knowledge{q: pool}
}

// LoadChunks returns every chunk with its embedding-presence flag. The corpus
// is small enough to hold in memory, which keeps BM25 scoring out of SQL.
func (k *Knowledge) LoadChunks(ctx context.Context) ([]knowledge.StoredChunk, error) {
	rows, err := k.q.Query(ctx, `
		SELECT id, source_path, section_key, source_kind, domain, topic_id, title, content, content_hash,
		       embedding IS NOT NULL
		FROM knowledge_chunks
		ORDER BY source_path, section_key`)
	if err != nil {
		return nil, fmt.Errorf("load knowledge chunks: %w", err)
	}
	defer rows.Close()

	var out []knowledge.StoredChunk
	for rows.Next() {
		var (
			chunk     knowledge.Chunk
			hasVector bool
		)
		if err := rows.Scan(
			&chunk.ID, &chunk.SourcePath, &chunk.SectionKey, &chunk.SourceKind,
			&chunk.Domain, &chunk.TopicID, &chunk.Title, &chunk.Content, &chunk.ContentHash,
			&hasVector,
		); err != nil {
			return nil, fmt.Errorf("scan knowledge chunk: %w", err)
		}
		out = append(out, knowledge.StoredChunk{Chunk: chunk, HasEmbedding: hasVector})
	}
	return out, rows.Err()
}

// SearchVectors runs the pgvector cosine search. ef_search keeps pgvector's
// default of 40, which matched recall on this corpus size.
func (k *Knowledge) SearchVectors(ctx context.Context, vector []float32, limit int) ([]knowledge.VectorHit, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := k.q.Query(ctx, `
		SELECT id, 1 - (embedding <=> $1::vector) AS cosine
		FROM knowledge_chunks
		WHERE embedding IS NOT NULL
		ORDER BY embedding <=> $1::vector
		LIMIT $2`, vectorLiteral(vector), limit)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()

	var out []knowledge.VectorHit
	for rows.Next() {
		var hit knowledge.VectorHit
		if err := rows.Scan(&hit.ChunkID, &hit.Cosine); err != nil {
			return nil, fmt.Errorf("scan vector hit: %w", err)
		}
		hit.Rank = len(out) + 1
		out = append(out, hit)
	}
	return out, rows.Err()
}

// ChunkRow is one upsert payload. A nil Embedding clears the stored vector,
// which is correct when content changed and the embedding arm is unavailable.
type ChunkRow struct {
	knowledge.Chunk
	Embedding []float32
}

// UpsertChunks writes chunk content and optional vectors. Each row is a
// single statement; rerunning the same corpus is an update in place.
func (k *Knowledge) UpsertChunks(ctx context.Context, rows []ChunkRow) error {
	if len(rows) == 0 {
		return nil
	}
	return k.upsertRows(ctx, rows)
}

func (k *Knowledge) upsertRows(ctx context.Context, rows []ChunkRow) error {
	for _, row := range rows {
		var vector any
		if len(row.Embedding) > 0 {
			vector = vectorLiteral(row.Embedding)
		}
		_, err := k.q.Exec(ctx, `
			INSERT INTO knowledge_chunks
				(id, source_path, section_key, source_kind, domain, topic_id, title, content, content_hash, embedding, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::vector, now())
			ON CONFLICT (id) DO UPDATE SET
				source_path = EXCLUDED.source_path,
				section_key = EXCLUDED.section_key,
				source_kind = EXCLUDED.source_kind,
				domain = EXCLUDED.domain,
				topic_id = EXCLUDED.topic_id,
				title = EXCLUDED.title,
				content = EXCLUDED.content,
				content_hash = EXCLUDED.content_hash,
				embedding = EXCLUDED.embedding,
				updated_at = now()`,
			row.ID, row.SourcePath, row.SectionKey, row.SourceKind, row.Domain,
			row.TopicID, row.Title, row.Content, row.ContentHash, vector,
		)
		if err != nil {
			return fmt.Errorf("upsert chunk %s: %w", row.ID, err)
		}
	}
	return nil
}

// DeleteChunks removes chunks whose source section no longer exists.
func (k *Knowledge) DeleteChunks(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := k.q.Exec(ctx, `DELETE FROM knowledge_chunks WHERE id = ANY($1)`, ids)
	if err != nil {
		return 0, fmt.Errorf("delete stale chunks: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// IngestRun is one row of rag_ingest_runs.
type IngestRun struct {
	ID             string
	CorpusHash     string
	EmbeddingModel string
	EmbeddingDim   int
	ChunksTotal    int
	ChunksEmbedded int
	ChunksSkipped  int
	ChunksDeleted  int
	Status         string
	DurationMS     int64
	Error          string
	StartedAt      time.Time
	FinishedAt     *time.Time
}

func (k *Knowledge) StartRun(ctx context.Context, run IngestRun) error {
	_, err := k.q.Exec(ctx, `
		INSERT INTO rag_ingest_runs
			(id, corpus_hash, embedding_model, embedding_dim, chunks_total, status, started_at)
		VALUES ($1,$2,$3,$4,$5,$6, now())`,
		run.ID, run.CorpusHash, run.EmbeddingModel, run.EmbeddingDim, run.ChunksTotal, run.Status)
	if err != nil {
		return fmt.Errorf("start ingest run: %w", err)
	}
	return nil
}

func (k *Knowledge) FinishRun(ctx context.Context, run IngestRun) error {
	_, err := k.q.Exec(ctx, `
		UPDATE rag_ingest_runs SET
			chunks_total = $2, chunks_embedded = $3, chunks_skipped = $4,
			chunks_deleted = $5, status = $6, duration_ms = $7, error = $8,
			finished_at = now()
		WHERE id = $1`,
		run.ID, run.ChunksTotal, run.ChunksEmbedded, run.ChunksSkipped,
		run.ChunksDeleted, run.Status, run.DurationMS, run.Error)
	if err != nil {
		return fmt.Errorf("finish ingest run: %w", err)
	}
	return nil
}

// LastSuccessfulRun returns the most recent applied run for the idempotency
// check. A missing row is returned as nil without error.
func (k *Knowledge) LastSuccessfulRun(ctx context.Context) (*IngestRun, error) {
	row := k.q.QueryRow(ctx, `
		SELECT id, corpus_hash, embedding_model, embedding_dim, chunks_total, chunks_embedded,
		       chunks_skipped, chunks_deleted, status, duration_ms, error, started_at, finished_at
		FROM rag_ingest_runs
		WHERE status = 'applied'
		ORDER BY started_at DESC
		LIMIT 1`)
	var run IngestRun
	err := row.Scan(&run.ID, &run.CorpusHash, &run.EmbeddingModel, &run.EmbeddingDim,
		&run.ChunksTotal, &run.ChunksEmbedded, &run.ChunksSkipped, &run.ChunksDeleted,
		&run.Status, &run.DurationMS, &run.Error, &run.StartedAt, &run.FinishedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load last ingest run: %w", err)
	}
	return &run, nil
}

// vectorLiteral renders a pgvector text literal. pgx has no native vector
// type, so the explicit ::vector cast in SQL is the dependency-free path.
func vectorLiteral(vector []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range vector {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(v), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
