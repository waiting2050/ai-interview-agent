// Command rageval measures retrieval quality on the committed 54-query set.
// It runs the same chunks through BM25-only and hybrid (BM25 + pgvector RRF)
// and writes a JSON artifact plus a terminal summary. It does not tune
// parameters: a miss stays a miss.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/knowledge"
	"github.com/waiting2050/ai-interview-agent/internal/platform/config"
	"github.com/waiting2050/ai-interview-agent/internal/store/postgres"
	"github.com/waiting2050/ai-interview-agent/migrations"
)

type evalReport struct {
	GeneratedAt    time.Time               `json:"generated_at"`
	QuerySetPath   string                  `json:"query_set_path"`
	QuerySetVer    string                  `json:"query_set_version"`
	TopK           int                     `json:"top_k"`
	Chunks         int                     `json:"chunks"`
	Mode           string                  `json:"mode"`
	BM25Weight     float64                 `json:"bm25_weight"`
	VectorWeight   float64                 `json:"vector_weight"`
	Embedding      knowledge.EmbeddingInfo `json:"embedding"`
	BM25           knowledge.ArmMetrics    `json:"bm25_only"`
	Hybrid         knowledge.ArmMetrics    `json:"hybrid"`
	BM25A          knowledge.ArmMetrics    `json:"bm25_group_a"`
	BM25B          knowledge.ArmMetrics    `json:"bm25_group_b"`
	HybridA        knowledge.ArmMetrics    `json:"hybrid_group_a"`
	HybridB        knowledge.ArmMetrics    `json:"hybrid_group_b"`
	BM25Calib      knowledge.Calibration   `json:"bm25_no_answer_calibration"`
	HybridCalib    knowledge.Calibration   `json:"hybrid_no_answer_calibration"`
	BM25Outcomes   []knowledge.Outcome     `json:"bm25_outcomes"`
	HybridOutcomes []knowledge.Outcome     `json:"hybrid_outcomes"`
}

func main() {
	if err := run(); err != nil {
		slog.Error("rageval failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	queryPath := flag.String("queries", "internal/agent/knowledge/testdata/retrieval_queries.json", "query set path")
	topK := flag.Int("top-k", 5, "hit@k cutoff")
	bm25Weight := flag.Float64("bm25-weight", 0, "override the RRF lexical weight (0 uses config)")
	vectorWeight := flag.Float64("vector-weight", 0, "override the RRF vector weight (0 uses config)")
	outDir := flag.String("out", "docs/eval-results", "result directory")
	flag.Parse()

	set, err := knowledge.LoadQuerySet(*queryPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *bm25Weight > 0 {
		cfg.M20.RAGBM25Weight = *bm25Weight
	}
	if *vectorWeight > 0 {
		cfg.M20.RAGVectorWeight = *vectorWeight
	}
	ctx := context.Background()
	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer store.Close()
	if err := migrations.Apply(ctx, store.Pool()); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	kb := postgres.NewKnowledge(store.Pool())
	chunks, err := kb.LoadChunks(ctx)
	if err != nil {
		return err
	}
	// Labels are validated against the stored corpus before any query runs:
	// a typo in a section prefix must fail loudly instead of scoring as a miss.
	if err := validateLabels(chunks, set.Queries); err != nil {
		return err
	}

	embedder := knowledge.NewEmbedder(knowledge.EmbeddingConfig{
		Provider: cfg.M20.EmbeddingProvider, BaseURL: cfg.M20.EmbeddingBaseURL,
		APIKey: cfg.M20.EmbeddingAPIKey, Model: cfg.M20.EmbeddingModel, Dim: cfg.M20.EmbeddingDim,
		Batch: cfg.M20.EmbeddingBatch, Timeout: cfg.M20.EmbeddingTimeout,
		ProbeTimeout: cfg.M20.EmbeddingProbeTimeout,
		PricePerMTOK: cfg.M20.EmbeddingPricePerMTOK,
	}, slog.Default())
	retriever := knowledge.NewRetriever(kb, embedder, knowledge.Options{
		Mode: knowledge.ModeHybrid, TopK: *topK,
		Candidate: cfg.M20.RAGCandidate, RRFK: cfg.M20.RAGRRFK,
		BM25Weight: cfg.M20.RAGBM25Weight, VectorWeight: cfg.M20.RAGVectorWeight,
	})
	retriever.Init(ctx)
	// Warm-up runs in the background; wait so the evaluation measures a warm
	// endpoint instead of cold-load spikes.
	if err := retriever.WaitReady(ctx); err != nil {
		return fmt.Errorf("hybrid warm-up failed: mode=%s reason=%s: %w", retriever.Mode(), retriever.Reason(), err)
	}
	if retriever.Mode() != knowledge.ModeHybrid {
		return fmt.Errorf("hybrid arm unavailable: mode=%s reason=%s", retriever.Mode(), retriever.Reason())
	}

	report := evalReport{
		GeneratedAt:  time.Now().UTC(),
		QuerySetPath: *queryPath,
		QuerySetVer:  set.Version,
		TopK:         *topK,
		Chunks:       len(chunks),
		Mode:         string(retriever.Mode()),
		BM25Weight:   cfg.M20.RAGBM25Weight,
		VectorWeight: cfg.M20.RAGVectorWeight,
		Embedding:    embedder.Info(),
	}

	for _, spec := range set.Queries {
		bm25, err := retriever.SearchBM25Only(ctx, spec.Query)
		if err != nil {
			return fmt.Errorf("bm25 search %s: %w", spec.ID, err)
		}
		hybrid, err := retriever.Search(ctx, spec.Query)
		if err != nil {
			return fmt.Errorf("hybrid search %s: %w", spec.ID, err)
		}
		report.BM25Outcomes = append(report.BM25Outcomes, toOutcome(spec, bm25, *topK,
			func(h knowledge.Hit) float64 { return h.BM25Score }))
		report.HybridOutcomes = append(report.HybridOutcomes, toOutcome(spec, hybrid, *topK,
			func(h knowledge.Hit) float64 { return h.Cosine }))
	}
	report.BM25 = knowledge.ScoreArm(set.Queries, report.BM25Outcomes, *topK)
	report.Hybrid = knowledge.ScoreArm(set.Queries, report.HybridOutcomes, *topK)
	report.BM25A = knowledge.ScoreArm(knowledge.GroupSpecs(set.Queries, "A"), report.BM25Outcomes, *topK)
	report.BM25B = knowledge.ScoreArm(knowledge.GroupSpecs(set.Queries, "B"), report.BM25Outcomes, *topK)
	report.HybridA = knowledge.ScoreArm(knowledge.GroupSpecs(set.Queries, "A"), report.HybridOutcomes, *topK)
	report.HybridB = knowledge.ScoreArm(knowledge.GroupSpecs(set.Queries, "B"), report.HybridOutcomes, *topK)
	report.BM25Calib = knowledge.Calibrate(report.BM25Outcomes)
	report.HybridCalib = knowledge.Calibrate(report.HybridOutcomes)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(*outDir, "rag-eval-"+report.GeneratedAt.Format("20060102-150405")+".json")
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		return err
	}
	printSummary(report, path)
	return nil
}

// validateLabels fails when any expectation matches no stored chunk, which
// would otherwise turn a labelling mistake into a silent retrieval miss.
func validateLabels(chunks []knowledge.StoredChunk, specs []knowledge.QuerySpec) error {
	for _, spec := range specs {
		for _, expected := range spec.Expected {
			found := false
			for _, chunk := range chunks {
				if knowledge.MatchesExpectation(chunk.Chunk, expected) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("query %s: expectation %+v matches no chunk", spec.ID, expected)
			}
		}
	}
	return nil
}

func toOutcome(spec knowledge.QuerySpec, result knowledge.Result, topK int, score func(knowledge.Hit) float64) knowledge.Outcome {
	outcome := knowledge.Outcome{
		ID: spec.ID, Group: spec.Group, Category: spec.Category, Query: spec.Query,
		Answerable: spec.Answerable, Mode: string(result.Mode),
		LatencyMS: result.LatencyMS, Top: result.Hits,
	}
	if len(result.Hits) > 0 {
		outcome.Top1Score = score(result.Hits[0])
	}
	// The runner-up is the best alternative in the same arm's score space; a
	// BM25-only hit carries cosine 0 and must not fabricate a huge cosine gap.
	for _, hit := range result.Hits[1:] {
		if s := score(hit); s > outcome.Top2Score {
			outcome.Top2Score = s
		}
	}
	outcome.Gap = outcome.Top1Score - outcome.Top2Score
	outcome.Rank = knowledge.RankOf(spec, outcome, topK)
	outcome.Hit = outcome.Rank > 0
	return outcome
}

func printSummary(report evalReport, path string) {
	fmt.Printf("query_set=%s queries=%d chunks=%d top_k=%d mode=%s\n",
		report.QuerySetVer, len(report.BM25Outcomes), report.Chunks, report.TopK, report.Mode)
	fmt.Printf("%-8s %-6s %-8s %-8s %-18s %-14s %s\n",
		"arm", "group", "hit@5", "MRR", "no-answer拒绝", "P50/P95(ms)", "hits")
	rows := []struct {
		arm, group string
		m          knowledge.ArmMetrics
		calib      *knowledge.Calibration
	}{
		{"bm25", "A", report.BM25A, nil},
		{"bm25", "B", report.BM25B, nil},
		{"bm25", "total", report.BM25, &report.BM25Calib},
		{"hybrid", "A", report.HybridA, nil},
		{"hybrid", "B", report.HybridB, nil},
		{"hybrid", "total", report.Hybrid, &report.HybridCalib},
	}
	for _, row := range rows {
		refusal := "—"
		if row.calib != nil {
			refusal = fmt.Sprintf("%d/%d %.2f", row.calib.RefusedNoAnswer, row.calib.NoAnswer, row.calib.RefusalRate)
		}
		fmt.Printf("%-8s %-6s %-8.4f %-8.4f %-18s %-14s %d/%d\n",
			row.arm, row.group, row.m.HitAt5, row.m.MRR, refusal,
			fmt.Sprintf("%d/%d", row.m.P50MS, row.m.P95MS), row.m.Hits, row.m.Answerable)
	}

	fmt.Println("no-answer calibration (refuse when top1 score <= threshold; scanned over observed scores):")
	for _, row := range []struct {
		name  string
		calib knowledge.Calibration
	}{
		{"bm25", report.BM25Calib},
		{"hybrid", report.HybridCalib},
	} {
		fmt.Printf("  %-7s threshold=%.3f refuse=%d/%d (%.2f) false_refusal=%d/%d (%.2f) overlap=%v\n",
			row.name, row.calib.Threshold, row.calib.RefusedNoAnswer, row.calib.NoAnswer, row.calib.RefusalRate,
			row.calib.FalseRefused, row.calib.Answerable, row.calib.FalseRefusalRate, row.calib.Overlap)
	}

	fmt.Println("no-answer top1 (bm25 raw score | hybrid cosine) and top1-top2 gap:")
	for i := range report.BM25Outcomes {
		if report.BM25Outcomes[i].Answerable {
			continue
		}
		b, hy := report.BM25Outcomes[i], report.HybridOutcomes[i]
		fmt.Printf("  %-4s bm25 %.2f gap=%.2f (%q) | hybrid cos=%.3f gap=%.3f (%q)\n",
			b.ID, b.Top1Score, b.Gap, topTitle(b), hy.Top1Score, hy.Gap, topTitle(hy))
	}

	fmt.Println("rank changes / misses (group, bm25 -> hybrid):")
	for i := range report.BM25Outcomes {
		b, hy := report.BM25Outcomes[i], report.HybridOutcomes[i]
		if b.Rank == hy.Rank && b.Hit == hy.Hit {
			continue
		}
		fmt.Printf("  %-4s %-2s %-10s bm25=%d hybrid=%d\n", b.ID, b.Group, b.Category, b.Rank, hy.Rank)
	}
	fmt.Println("wrote", path)
}

func topTitle(outcome knowledge.Outcome) string {
	if len(outcome.Top) == 0 {
		return ""
	}
	return outcome.Top[0].Title
}
