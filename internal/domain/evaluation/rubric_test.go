package evaluation

import (
	"testing"
)

func fullScores(v int) map[Dimension]int {
	scores := map[Dimension]int{}
	for _, d := range DimensionOrder {
		scores[d] = v
	}
	return scores
}

func TestComputeScore(t *testing.T) {
	if got := ComputeScore(fullScores(5), nil, nil); got.Total != 100 {
		t.Fatalf("all 5 should be 100, got %+v", got)
	}
	if got := ComputeScore(fullScores(3), nil, nil); got.Total != 60 {
		t.Fatalf("all 3 should be 60, got %+v", got)
	}
	if got := ComputeScore(fullScores(1), nil, nil); got.Total != 20 {
		t.Fatalf("all 1 should be 20, got %+v", got)
	}

	got := ComputeScore(fullScores(4), []Adjustment{{Points: 3}}, []Adjustment{{Points: -2}})
	if got.Base != 80 || got.Bonus != 3 || got.Penalty != -2 || got.Total != 81 {
		t.Fatalf("unexpected breakdown: %+v", got)
	}

	if got := ComputeScore(fullScores(1), []Adjustment{{Points: 3}}, []Adjustment{{Points: -3}}); got.Total != 20 {
		t.Fatalf("bonus/penalty cancelled: %+v", got)
	}
	if got := ComputeScore(fullScores(5), []Adjustment{{Points: 3}}, nil); got.Total != 100 {
		t.Fatalf("score must clamp at 100: %+v", got)
	}
}

func TestBand(t *testing.T) {
	cases := []struct {
		total float64
		want  int
	}{
		{0, 1}, {40, 1}, {49.9, 1},
		{50, 3}, {60, 3}, {79.9, 3},
		{80, 5}, {100, 5},
	}
	for _, c := range cases {
		if got := Band(c.total); got != c.want {
			t.Fatalf("Band(%.1f) = %d, want %d", c.total, got, c.want)
		}
	}
}

func TestSanitizeDropsQuotesNotInAnswer(t *testing.T) {
	answer := "我用 channel 控制并发，压测 QPS 从 800 提升到 1500。"
	r := Result{
		Scores: fullScores(3),
		Evidence: []Evidence{
			{Dimension: DimensionCorrectness, Quote: "channel 控制并发", Reason: "命中"},
			{Dimension: DimensionDepth, Quote: "我用了 Redis 分布式锁", Reason: "编造"},
			{Dimension: DimensionDepth, Quote: "qps 从 800 提升到 1500", Reason: "标点与大小写归一"},
		},
		Bonus:   []Adjustment{{Points: 1, Quote: "压测"}},
		Penalty: []Adjustment{{Points: -2, Quote: "编造的惩罚"}},
	}
	r.Sanitize(answer)

	if len(r.Evidence) != 2 {
		t.Fatalf("expected 2 surviving evidence, got %d: %+v", len(r.Evidence), r.Evidence)
	}
	if len(r.Bonus) != 1 || len(r.Penalty) != 0 {
		t.Fatalf("unexpected adjustments: bonus=%v penalty=%v", r.Bonus, r.Penalty)
	}
}

func TestSanitizeClearsFollowupWithoutTarget(t *testing.T) {
	r := Result{Followup: FollowupSuggestion{Needed: true, Target: "  "}}
	r.Sanitize("answer")
	if r.Followup.Needed {
		t.Fatal("followup without target must be cleared")
	}
}

func TestUniqueEvidenceDropsRepeatedQuotes(t *testing.T) {
	in := []Evidence{
		{Dimension: DimensionCorrectness, Quote: "channel 控制并发", Reason: "first"},
		{Dimension: DimensionCorrectness, Quote: " channel 控制并发 ", Reason: "spacing variant"},
		{Dimension: DimensionDepth, Quote: "channel 控制并发", Reason: "other dimension"},
		{Dimension: DimensionDepth, Quote: "压测 QPS 从 800 提升到 1500", Reason: "distinct quote"},
	}
	out := UniqueEvidence(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 unique quotes, got %d: %+v", len(out), out)
	}
	if out[0].Reason != "first" {
		t.Fatalf("first annotation must be kept, got %+v", out[0])
	}
	if out[1].Quote != "压测 QPS 从 800 提升到 1500" {
		t.Fatalf("unexpected second quote: %+v", out[1])
	}
	if UniqueEvidence(nil) != nil {
		t.Fatal("nil evidence must stay nil")
	}
}

func TestValidateReportsContractViolations(t *testing.T) {
	valid := Result{
		SchemaVersion: SchemaVersion,
		Scores:        fullScores(3),
		Evidence:      []Evidence{{Dimension: DimensionCorrectness, Quote: "answer", Reason: "ok"}},
		Confidence:    0.5,
	}
	if issues := valid.Validate(); len(issues) != 0 {
		t.Fatalf("valid result reported issues: %v", issues)
	}

	missing := fullScores(3)
	delete(missing, DimensionDepth)
	cases := map[string]Result{
		"missing dimension": {SchemaVersion: 1, Scores: missing},
		"score out of range": {
			SchemaVersion: 1,
			Scores:        map[Dimension]int{DimensionCorrectness: 0},
			Evidence:      []Evidence{{Dimension: DimensionCorrectness, Quote: "answer"}},
		},
		"empty evidence": {
			SchemaVersion: 1,
			Scores:        fullScores(3),
			Confidence:    0.5,
		},
		"bad confidence": {
			SchemaVersion: 1,
			Scores:        fullScores(3),
			Evidence:      []Evidence{{Dimension: DimensionCorrectness, Quote: "answer"}},
			Confidence:    1.4,
		},
		"followup without target": {
			SchemaVersion: 1,
			Scores:        fullScores(3),
			Evidence:      []Evidence{{Dimension: DimensionCorrectness, Quote: "answer"}},
			Confidence:    0.5,
			Followup:      FollowupSuggestion{Needed: true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if issues := tc.Validate(); len(issues) == 0 {
				t.Fatal("expected validation issues")
			}
		})
	}
}
