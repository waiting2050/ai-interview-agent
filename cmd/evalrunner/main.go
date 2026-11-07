// Command evalrunner runs the scoring regression set through the real
// evaluation chain. With no OPENAI_API_KEY it measures the deterministic
// heuristic baseline; with a key the same command measures the real model.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"

	"github.com/waiting2050/ai-interview-agent/internal/agent/eval"
	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/agent/prompt"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
)

const (
	exitOK         = 0
	exitError      = 1
	exitGateFailed = 2

	defaultSet = "internal/agent/eval/testdata/regression.json"
	defaultOut = "docs/eval-results"
)

// callBudget enforces a hard upper bound on model calls. The eval runner is
// sequential, so a check-then-increment is sufficient; a limit of zero means
// unlimited and keeps the default behaviour unchanged.
type callBudget struct {
	inner llm.Client
	limit int64
	used  atomic.Int64
}

func (b *callBudget) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	if b.limit > 0 && b.used.Load() >= b.limit {
		return llm.Response{}, fmt.Errorf("model call budget exhausted (%d calls)", b.limit)
	}
	b.used.Add(1)
	return b.inner.Generate(ctx, req)
}

func main() {
	code, err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "evalrunner: %v\n", err)
		os.Exit(exitError)
	}
	os.Exit(code)
}

func run() (int, error) {
	setPath := flag.String("set", defaultSet, "regression set JSON path")
	outDir := flag.String("out", defaultOut, "directory for timestamped result files")
	provider := flag.String("provider", "", "heuristic|openai (default: MODEL_PROVIDER, or openai when OPENAI_API_KEY is set)")
	limit := flag.Int("limit", 0, "run only the first N cases (0 = whole set); useful for a cost-bounded smoke run")
	maxCalls := flag.Int("max-calls", 0, "hard cap on model calls (0 = unlimited); the run stops calling once the cap is hit")
	gate := flag.Bool("gate", true, "exit 2 when the plan's thresholds are missed")
	flag.Parse()

	ds, err := eval.LoadDataset(*setPath)
	if err != nil {
		return exitError, err
	}
	if *limit > 0 && *limit < len(ds.Cases) {
		ds.Cases = ds.Cases[:*limit]
	}
	digest, err := eval.FileDigest(*setPath)
	if err != nil {
		return exitError, err
	}

	cfg, err := llm.LoadProviderConfig()
	if err != nil {
		return exitError, fmt.Errorf("load provider config: %w", err)
	}
	if *provider != "" {
		cfg.Provider = strings.ToLower(strings.TrimSpace(*provider))
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	client, info, err := llm.NewClient(context.Background(), cfg, interview.NewHeuristicClient(), logger)
	if err != nil {
		return exitError, fmt.Errorf("assemble model client: %w", err)
	}
	budget := &callBudget{inner: client, limit: int64(*maxCalls)}
	client = budget

	report := eval.Run(context.Background(),
		interview.NewEvaluator(client, info.Model),
		ds,
		eval.Meta{
			DatasetPath:    *setPath,
			DatasetSHA256:  digest,
			Provider:       info.Provider,
			Model:          info.Model,
			BaseURL:        info.BaseURL,
			Fallback:       info.Fallback,
			FallbackReason: info.Reason,
			PromptVersion:  prompt.EvaluationVersion,
		},
	)

	printSummary(report)
	fmt.Printf("model_calls_used=%d", budget.used.Load())
	if *maxCalls > 0 {
		fmt.Printf(" max_calls=%d", *maxCalls)
	}
	fmt.Println()

	path, err := eval.WriteReport(report, *outDir)
	if err != nil {
		return exitError, err
	}
	fmt.Printf("\nresult_file: %s\n", path)

	if !report.Gate.Passed && *gate {
		return exitGateFailed, nil
	}
	return exitOK, nil
}

func printSummary(r eval.Report) {
	fmt.Printf("provider=%s model=%s prompt=%s rubric=%d\n",
		r.Provider, r.Model, r.PromptVersion, r.RubricSchemaVersion)
	if r.Fallback {
		fmt.Printf("fallback=true reason=%q\n", r.FallbackReason)
	}
	fmt.Printf("dataset=%s sha256=%.12s cases=%d\n", r.DatasetPath, r.DatasetSHA256, r.Metrics.Total)

	fmt.Print("distribution: ")
	for _, typ := range eval.SortedTypes(r.Distribution) {
		fmt.Printf("%s=%d ", typ, r.Distribution[typ])
	}
	fmt.Println()

	m := r.Metrics
	fmt.Printf("schema_valid_rate=%.1f%% (%d/%d valid, %d degraded, %d errors)\n",
		m.SchemaValidRate*100, m.Evaluated, m.Total, m.Degraded, m.Errors)
	fmt.Printf("band_hit_rate=%.1f%% (%d/%d)\n", m.BandHitRate*100, m.BandHits, m.Evaluated)
	fmt.Printf("adjacent_band_rate=%.1f%% (%d/%d)\n", m.AdjacentBandRate*100, m.AdjacentHits, m.Evaluated)
	fmt.Printf("dimension_mae=%.4f (gate <= %.2f)\n", m.DimensionMAE, r.Thresholds.DimensionMAE)
	fmt.Printf("evidence_quote_rate=%.1f%%\n", m.EvidenceQuoteRate*100)
	fmt.Printf("cost_per_case: prompt_tokens=%.1f completion_tokens=%.1f latency_ms=%.1f\n",
		m.AvgPromptTokens, m.AvgCompletionTokens, m.AvgLatencyMS)

	fmt.Println("\ncases:")
	for _, c := range r.Cases {
		switch {
		case c.Error != "":
			fmt.Printf("  ERROR   %-14s type=%-11s error=%s\n", c.CaseID, c.Type, c.Error)
		case c.Degraded:
			fmt.Printf("  DEGRADE %-14s type=%-11s code=%s\n", c.CaseID, c.Type, c.ErrorCode)
		default:
			status := "PASS"
			if c.ActualBand != c.ExpectedBand {
				status = "MISS"
			}
			fmt.Printf("  %-7s %-14s type=%-11s band=%d->%d score=%.1f",
				status, c.CaseID, c.Type, c.ExpectedBand, c.ActualBand, c.TotalScore)
			for _, d := range c.Deviations {
				fmt.Printf(" %s:%d->%d", shortDimension(d.Dimension), d.Expected, d.Actual)
			}
			fmt.Println()
		}
	}

	if len(r.MissesByType) > 0 {
		fmt.Print("\nmisses_by_type: ")
		for _, typ := range eval.SortedTypes(r.MissesByType) {
			fmt.Printf("%s=%d ", typ, r.MissesByType[typ])
		}
		fmt.Println()
	}

	if r.Gate.Passed {
		fmt.Println("\ngate: PASS")
		return
	}
	fmt.Println("\ngate: FAIL")
	for _, f := range r.Gate.Failures {
		fmt.Printf("  - %s\n", f)
	}
}

func shortDimension(d evaluation.Dimension) string {
	switch d {
	case evaluation.DimensionCorrectness:
		return "correctness"
	case evaluation.DimensionDepth:
		return "depth"
	case evaluation.DimensionEngineeringJudgment:
		return "engineering"
	case evaluation.DimensionStructure:
		return "structure"
	case evaluation.DimensionEvidenceCredibility:
		return "evidence"
	default:
		return string(d)
	}
}
