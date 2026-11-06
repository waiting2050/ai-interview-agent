package eval

import (
	"context"
	"errors"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
)

type fakeEvaluator struct {
	outcomes map[string]interview.Outcome
	errs     map[string]error
}

func (f fakeEvaluator) Evaluate(_ context.Context, in interview.EvaluateInput) (interview.Outcome, error) {
	if err := f.errs[in.TurnID]; err != nil {
		return interview.Outcome{}, err
	}
	return f.outcomes[in.TurnID], nil
}

func allScores(v int) map[evaluation.Dimension]int {
	s := map[evaluation.Dimension]int{}
	for _, d := range evaluation.DimensionOrder {
		s[d] = v
	}
	return s
}

func makeCase(id string, typ string, band int, scores map[evaluation.Dimension]int) Case {
	return Case{
		CaseID:         id,
		Type:           typ,
		Topic:          "topic",
		Difficulty:     "mid",
		Question:       "question " + id,
		Answer:         "answer " + id,
		ExpectedBand:   band,
		ExpectedScores: scores,
		Notes:          "label",
	}
}

func evalOutcome(answer string, scores map[evaluation.Dimension]int) interview.Outcome {
	return interview.Outcome{
		Result: evaluation.Result{
			SchemaVersion: evaluation.SchemaVersion,
			Scores:        scores,
			Evidence: []evaluation.Evidence{
				{Dimension: evaluation.DimensionCorrectness, Quote: answer, Reason: "quote"},
			},
			Confidence: 0.6,
		},
		ModelName:     "fake",
		PromptVersion: "eval-v2",
	}
}

func TestRunComputesBandAndDimensionMetrics(t *testing.T) {
	ds := &Dataset{Version: 1, Cases: []Case{
		makeCase("hit-high", TypeHigh, 5, allScores(5)),
		makeCase("adjacent-miss", TypeMid, 3, allScores(3)),
		makeCase("hit-mid", TypeMid, 3, allScores(3)),
		makeCase("far-miss", TypeLow, 1, allScores(1)),
	}}

	ev := fakeEvaluator{outcomes: map[string]interview.Outcome{
		"hit-high":      evalOutcome("answer hit-high", allScores(5)),
		"adjacent-miss": evalOutcome("answer adjacent-miss", allScores(2)),
		"hit-mid":       evalOutcome("answer hit-mid", allScores(3)),
		"far-miss":      evalOutcome("answer far-miss", allScores(4)),
	}}

	report := Run(context.Background(), ev, ds, Meta{Provider: "fake", PromptVersion: "eval-v2"})
	m := report.Metrics

	if m.Total != 4 || m.Evaluated != 4 {
		t.Fatalf("unexpected totals: %+v", m)
	}
	if m.BandHits != 2 || m.BandHitRate != 0.5 {
		t.Fatalf("band hits = %d rate = %.2f, want 2/0.5", m.BandHits, m.BandHitRate)
	}
	if m.AdjacentHits != 3 || m.AdjacentBandRate != 0.75 {
		t.Fatalf("adjacent = %d rate = %.2f, want 3/0.75", m.AdjacentHits, m.AdjacentBandRate)
	}
	// Errors are (3-2)*5=5 and (4-1)*5=15 over 20 labelled dimensions.
	if m.DimensionMAE != 1.0 {
		t.Fatalf("dimension MAE = %.4f, want 1.0", m.DimensionMAE)
	}
	if m.SchemaValidRate != 1.0 || m.EvidenceQuoteRate != 1.0 {
		t.Fatalf("schema/evidence rates = %.2f/%.2f", m.SchemaValidRate, m.EvidenceQuoteRate)
	}
	if report.Gate.Passed {
		t.Fatalf("gate should fail on band/adjacent/mae: %+v", report.Gate)
	}
	if report.MissesByType[TypeMid] != 1 || report.MissesByType[TypeLow] != 1 {
		t.Fatalf("unexpected misses_by_type: %+v", report.MissesByType)
	}
}

func TestRunExcludesDegradedAndErrorsFromBandDenominator(t *testing.T) {
	ds := &Dataset{Version: 1, Cases: []Case{
		makeCase("ok", TypeHigh, 5, allScores(5)),
		makeCase("degraded", TypeMid, 3, allScores(3)),
		makeCase("error", TypeLow, 1, allScores(1)),
	}}

	degraded := interview.Outcome{Degraded: true, ErrorCode: interview.ErrorCodeModelOutputInvalid}
	ev := fakeEvaluator{
		outcomes: map[string]interview.Outcome{
			"ok":       evalOutcome("answer ok", allScores(5)),
			"degraded": degraded,
		},
		errs: map[string]error{"error": errors.New("upstream timeout")},
	}

	report := Run(context.Background(), ev, ds, Meta{})
	m := report.Metrics

	if m.Total != 3 || m.Evaluated != 1 || m.Degraded != 1 || m.Errors != 1 {
		t.Fatalf("unexpected counts: %+v", m)
	}
	if m.SchemaValidRate != 0.3333 {
		t.Fatalf("schema valid rate = %.4f, want 0.3333", m.SchemaValidRate)
	}
	if m.BandHitRate != 1.0 || m.AdjacentBandRate != 1.0 {
		t.Fatalf("band denominators should exclude degraded/error: %+v", m)
	}
	if report.Gate.Passed {
		t.Fatalf("gate should fail on schema_valid_rate: %+v", report.Gate)
	}
}

func TestRunMarksEvidenceQuoteFailure(t *testing.T) {
	ds := &Dataset{Version: 1, Cases: []Case{makeCase("invented", TypeHigh, 5, allScores(5))}}
	bad := evalOutcome("answer invented", allScores(5))
	bad.Result.Evidence[0].Quote = "这段原文不存在"

	report := Run(context.Background(), fakeEvaluator{outcomes: map[string]interview.Outcome{"invented": bad}}, ds, Meta{})
	if report.Metrics.EvidenceQuoteRate != 0 {
		t.Fatalf("invented evidence must not pass the re-check: %+v", report.Metrics)
	}
	if report.Gate.Passed {
		t.Fatalf("gate should fail when evidence quotes do not survive: %+v", report.Gate)
	}
}
