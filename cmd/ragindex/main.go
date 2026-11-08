// Command ragindex builds the retrieval corpus and loads it into pgvector.
// Running without -apply performs a dry-run: it reads the repository and
// prints chunk/token/batch estimates without touching the database.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/knowledge"
	"github.com/waiting2050/ai-interview-agent/internal/domain/domainpack"
	"github.com/waiting2050/ai-interview-agent/internal/platform/config"
	"github.com/waiting2050/ai-interview-agent/internal/platform/idgen"
	"github.com/waiting2050/ai-interview-agent/internal/store/postgres"
	"github.com/waiting2050/ai-interview-agent/migrations"
)

type summary struct {
	DryRun           bool                    `json:"dry_run"`
	CorpusHash       string                  `json:"corpus_hash"`
	Stats            knowledge.CorpusStats   `json:"stats"`
	Embedding        knowledge.EmbeddingInfo `json:"embedding"`
	EmbeddingReady   bool                    `json:"embedding_ready"`
	EmbeddingReason  string                  `json:"embedding_reason,omitempty"`
	Mode             string                  `json:"mode"`
	Changed          int                     `json:"changed_chunks"`
	ToEmbed          int                     `json:"chunks_to_embed"`
	Embedded         int                     `json:"chunks_embedded"`
	Skipped          int                     `json:"chunks_skipped"`
	Deleted          int                     `json:"chunks_deleted"`
	EstimatedTokens  int                     `json:"estimated_tokens"`
	EstimatedBatches int                     `json:"estimated_batches"`
	EmbedDurationMS  int64                   `json:"embed_duration_ms"`
	TotalDurationMS  int64                   `json:"total_duration_ms"`
	EstimatedCostUSD float64                 `json:"estimated_cost_usd"`
	Status           string                  `json:"status"`
	Noop             bool                    `json:"noop"`
	Error            string                  `json:"error,omitempty"`
}

func main() {
	if err := run(); err != nil {
		slog.Error("ragindex failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	apply := flag.Bool("apply", false, "write chunks and embeddings to PostgreSQL")
	dryRun := flag.Bool("dry-run", false, "print the plan only (default)")
	force := flag.Bool("force-embed", false, "re-embed unchanged chunks")
	flag.Parse()
	if !*apply {
		*dryRun = true
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	chunks, err := knowledge.BuildCorpus(knowledge.CorpusSources{
		Packs:      domainpack.Default().List(),
		DocsRoot:   cfg.M20.RAGDocsDir,
		ExtraFiles: []string{"README.md"},
	})
	if err != nil {
		return fmt.Errorf("build corpus: %w", err)
	}

	stats := knowledge.Stats(chunks, cfg.M20.EmbeddingBatch)
	embedder := knowledge.NewEmbedder(embeddingConfig(cfg), slog.Default())
	out := summary{
		DryRun:           *dryRun,
		CorpusHash:       knowledge.CorpusHash(chunks),
		Stats:            stats,
		Embedding:        embedder.Info(),
		EmbeddingReady:   embedder.Enabled(),
		EstimatedTokens:  stats.Tokens,
		EstimatedBatches: stats.Batches,
		EstimatedCostUSD: embedder.CostUSD(stats.Tokens),
		Mode:             string(knowledge.ModeBM25),
	}

	if *dryRun {
		out.EmbeddingReady = false
		if embedder.Enabled() {
			out.Mode = string(knowledge.ModeHybrid)
			out.EmbeddingReason = "dry-run: embedding configured; reachability is probed at ingest"
		} else {
			out.Mode = string(knowledge.ModeBM25Fallback)
			out.EmbeddingReason = "embedding provider disabled"
		}
		return printSummary(out)
	}

	ctx := context.Background()
	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer store.Close()

	kb := postgres.NewKnowledge(store.Pool())
	if err := migrations.Apply(ctx, store.Pool()); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	plan, err := planFromDB(ctx, kb, chunks, *force)
	if err != nil {
		return err
	}
	out.Changed = len(plan.Changed)
	out.Skipped = len(chunks) - len(plan.Changed)
	out.Deleted = len(plan.Stale)
	out.Status = "applied"

	if len(plan.Changed) == 0 && len(plan.Stale) == 0 {
		out.Noop = true
		out.Mode = string(knowledge.ModeBM25)
		if embedder.Enabled() {
			out.Mode = string(knowledge.ModeHybrid)
		}
		slog.Info("rag ingest skipped: corpus unchanged", "chunks", len(chunks), "hash", out.CorpusHash)
		return printSummary(out)
	}

	started := time.Now()
	run := postgres.IngestRun{
		ID:             idgen.New(),
		CorpusHash:     out.CorpusHash,
		EmbeddingModel: embedder.Info().Model,
		EmbeddingDim:   embedder.Info().Dim,
		ChunksTotal:    len(chunks),
		Status:         "running",
	}
	if err := kb.StartRun(ctx, run); err != nil {
		return err
	}

	rows := make([]postgres.ChunkRow, 0, len(plan.Changed))
	vectors := make([][]float32, len(plan.Changed))
	if embedder.Enabled() && len(plan.Changed) > 0 {
		texts := make([]string, len(plan.Changed))
		for i, chunk := range plan.Changed {
			texts[i] = chunk.Title + "\n" + chunk.Content
		}
		out.EstimatedTokens = knowledge.EstimateTokens(joinForEstimate(texts))
		embedStart := time.Now()
		vectors, err = embedder.Embed(ctx, texts)
		out.EmbedDurationMS = time.Since(embedStart).Milliseconds()
		if err != nil {
			// Explicit degradation: content is still written so BM25 works, and
			// the run is recorded as content_only with the real reason.
			slog.Warn("embedding failed, writing content only; retrieval will run bm25",
				"error", err, "chunks", len(plan.Changed))
			out.EmbeddingReason = err.Error()
			out.Status = "content_only"
			vectors = make([][]float32, len(plan.Changed))
			out.Mode = string(knowledge.ModeBM25Fallback)
		} else {
			out.Embedded = len(vectors)
			out.Mode = string(knowledge.ModeHybrid)
		}
	} else if len(plan.Changed) > 0 {
		out.Mode = string(knowledge.ModeBM25Fallback)
		out.EmbeddingReason = "embedding provider disabled"
		out.Status = "content_only"
	}

	for i, chunk := range plan.Changed {
		rows = append(rows, postgres.ChunkRow{Chunk: chunk, Embedding: vectors[i]})
	}
	if err := kb.UpsertChunks(ctx, rows); err != nil {
		out.TotalDurationMS = finishRun(ctx, kb, run, out, started)
		return fmt.Errorf("upsert chunks: %w", err)
	}
	deleted, err := kb.DeleteChunks(ctx, plan.Stale)
	if err != nil {
		out.TotalDurationMS = finishRun(ctx, kb, run, out, started)
		return fmt.Errorf("delete stale chunks: %w", err)
	}
	out.Deleted = deleted
	out.TotalDurationMS = finishRun(ctx, kb, run, out, started)
	return printSummary(out)
}

func finishRun(ctx context.Context, kb *postgres.Knowledge, run postgres.IngestRun, out summary, started time.Time) int64 {
	run.Status = out.Status
	run.ChunksEmbedded = out.Embedded
	run.ChunksSkipped = out.Skipped
	run.ChunksDeleted = out.Deleted
	run.DurationMS = time.Since(started).Milliseconds()
	run.Error = out.EmbeddingReason
	if err := kb.FinishRun(ctx, run); err != nil {
		slog.Error("record ingest run failed", "error", err)
	}
	return run.DurationMS
}

// planFromDB loads stored chunks and applies the same diff rules the unit
// tests cover, so the command and the test exercise one implementation.
func planFromDB(ctx context.Context, kb *postgres.Knowledge, chunks []knowledge.Chunk, force bool) (knowledge.IngestPlan, error) {
	existing, err := kb.LoadChunks(ctx)
	if err != nil {
		// Fail loudly: an ingest that cannot see current state must not
		// silently re-embed or delete anything.
		return knowledge.IngestPlan{}, fmt.Errorf("load existing chunks: %w", err)
	}
	return knowledge.PlanIngest(existing, chunks, force), nil
}

func joinForEstimate(texts []string) string {
	var b []byte
	for _, text := range texts {
		b = append(b, text...)
		b = append(b, '\n')
	}
	return string(b)
}

func embeddingConfig(cfg config.Config) knowledge.EmbeddingConfig {
	return knowledge.EmbeddingConfig{
		Provider:     cfg.M20.EmbeddingProvider,
		BaseURL:      cfg.M20.EmbeddingBaseURL,
		APIKey:       cfg.M20.EmbeddingAPIKey,
		Model:        cfg.M20.EmbeddingModel,
		Dim:          cfg.M20.EmbeddingDim,
		Batch:        cfg.M20.EmbeddingBatch,
		Timeout:      cfg.M20.EmbeddingTimeout,
		PricePerMTOK: cfg.M20.EmbeddingPricePerMTOK,
	}
}

func printSummary(out summary) error {
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}
