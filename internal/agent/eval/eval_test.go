//go:build eval

// This test runs the shipped regression set through the heuristic model. It
// asserts the hard invariants (schema and evidence quotes) and prints the
// measured baseline; band thresholds are enforced by cmd/evalrunner because
// the heuristic model is a length-only baseline and is expected to miss
// correctness/exaggeration/off-topic cases.
package eval_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/agent/eval"
	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/agent/prompt"
)

func TestHeuristicBaseline(t *testing.T) {
	path := filepath.Join("testdata", "regression.json")
	ds, err := eval.LoadDataset(path)
	if err != nil {
		t.Fatalf("load regression set: %v", err)
	}
	digest, err := eval.FileDigest(path)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}

	report := eval.Run(context.Background(),
		interview.NewEvaluator(interview.NewHeuristicClient(), "fake-heuristic"),
		ds,
		eval.Meta{
			DatasetPath:   path,
			DatasetSHA256: digest,
			Provider:      "heuristic",
			Model:         "fake-heuristic",
			PromptVersion: prompt.EvaluationVersion,
		},
	)

	m := report.Metrics
	if m.SchemaValidRate != 1.0 {
		t.Fatalf("heuristic baseline must always produce schema-valid output: %+v", m)
	}
	if m.EvidenceQuoteRate != 1.0 {
		t.Fatalf("heuristic baseline quotes must survive the evidence re-check: %+v", m)
	}

	t.Logf("heuristic baseline: cases=%d valid=%d band_hit=%.1f%% adjacent=%.1f%% mae=%.2f gate=%v",
		m.Total, m.Evaluated, m.BandHitRate*100, m.AdjacentBandRate*100, m.DimensionMAE, report.Gate.Passed)
	for _, c := range report.Cases {
		if c.Error != "" || c.Degraded || c.ActualBand != c.ExpectedBand {
			t.Logf("miss %-18s type=%-11s band=%d->%d score=%.1f dev=%v",
				c.CaseID, c.Type, c.ExpectedBand, c.ActualBand, c.TotalScore, c.Deviations)
		}
	}
}
