package eval

import (
	"testing"
	"time"
)

func TestCompareReportsMetricsAndFlips(t *testing.T) {
	base := Report{
		GeneratedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		Provider:    "heuristic",
		Model:       "fake/heuristic",
		Metrics: Metrics{
			Total:             3,
			SchemaValidRate:   1.0,
			BandHitRate:       0.33,
			AdjacentBandRate:  0.67,
			DimensionMAE:      1.20,
			EvidenceQuoteRate: 1.0,
		},
		Gate: Gate{Passed: false, Failures: []string{"band_hit_rate"}},
		Cases: []CaseResult{
			{CaseID: "a", Domain: DomainGoBackend, Topic: "t1", ExpectedBand: 5, ActualBand: 1, TotalScore: 40},
			{CaseID: "b", Domain: DomainAIAgent, Topic: "t2", ExpectedBand: 3, ActualBand: 3, TotalScore: 60},
			{CaseID: "c", Domain: DomainAIAgent, Topic: "t3", ExpectedBand: 1, ActualBand: 3, TotalScore: 55},
		},
	}
	candidate := Report{
		GeneratedAt: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		Provider:    "openai",
		Model:       "deepseek-flash",
		Metrics: Metrics{
			Total:             3,
			SchemaValidRate:   1.0,
			BandHitRate:       0.67,
			AdjacentBandRate:  1.0,
			DimensionMAE:      0.40,
			EvidenceQuoteRate: 1.0,
		},
		Gate: Gate{Passed: true},
		Cases: []CaseResult{
			{CaseID: "a", Domain: DomainGoBackend, Topic: "t1", ExpectedBand: 5, ActualBand: 5, TotalScore: 85},
			{CaseID: "b", Domain: DomainAIAgent, Topic: "t2", ExpectedBand: 3, ActualBand: 1, TotalScore: 35},
			{CaseID: "c", Domain: DomainAIAgent, Topic: "t3", ExpectedBand: 1, ActualBand: 1, TotalScore: 40},
		},
	}

	cmp := Compare(base, candidate, "base.json", "candidate.json")
	if cmp.BaseOnlyCases != 0 || cmp.CandidateOnly != 0 {
		t.Fatalf("unexpected only-in counts: %+v", cmp)
	}
	if cmp.GateStatusChange != "failed -> passed" {
		t.Fatalf("gate change = %q", cmp.GateStatusChange)
	}

	want := map[string]float64{
		"band_hit_rate":      0.34,
		"dimension_mae":      -0.80,
		"adjacent_band_rate": 0.33,
	}
	metrics := map[string]MetricDelta{}
	for _, m := range cmp.Metrics {
		metrics[m.Name] = m
	}
	for name, delta := range want {
		m := metrics[name]
		if m.Delta != delta {
			t.Fatalf("%s delta = %v, want %v", name, m.Delta, delta)
		}
		if !m.Improved {
			t.Fatalf("%s should count as improved", name)
		}
	}
	if !metrics["dimension_mae"].LowerIsBetter {
		t.Fatal("dimension_mae must be marked lower-is-better")
	}

	if len(cmp.Flips) != 3 {
		t.Fatalf("flips = %+v, want 3", cmp.Flips)
	}
	byCase := map[string]Flip{}
	for _, f := range cmp.Flips {
		byCase[f.CaseID] = f
	}
	if byCase["a"].Direction != "improved" {
		t.Fatalf("case a direction = %q", byCase["a"].Direction)
	}
	if byCase["c"].Direction != "improved" {
		t.Fatalf("case c direction = %q", byCase["c"].Direction)
	}
	if byCase["b"].Direction != "regressed" {
		t.Fatalf("case b direction = %q", byCase["b"].Direction)
	}
	if cmp.ImprovedFlips != 2 || cmp.RegressedFlips != 1 || cmp.ChangedFlips != 0 {
		t.Fatalf("flip counts = %d/%d/%d", cmp.ImprovedFlips, cmp.RegressedFlips, cmp.ChangedFlips)
	}
	if byCase["a"].Domain != DomainGoBackend {
		t.Fatalf("flip domain = %q", byCase["a"].Domain)
	}
}

func TestCompareSkipsUnavailableCases(t *testing.T) {
	base := Report{
		Provider: "heuristic",
		Metrics:  Metrics{Total: 2},
		Cases: []CaseResult{
			{CaseID: "err", Domain: DomainGoBackend, ExpectedBand: 3, ActualBand: 3, Error: "boom"},
			{CaseID: "ok", Domain: DomainAIAgent, ExpectedBand: 3, ActualBand: 3},
		},
	}
	candidate := Report{
		Provider: "openai",
		Metrics:  Metrics{Total: 1},
		Cases: []CaseResult{
			{CaseID: "err", Domain: DomainGoBackend, ExpectedBand: 3, ActualBand: 1},
			{CaseID: "only-candidate", Domain: DomainAIAgent, ExpectedBand: 1, ActualBand: 1},
		},
	}
	cmp := Compare(base, candidate, "a.json", "b.json")
	if len(cmp.Flips) != 0 {
		t.Fatalf("error cases must not flip: %+v", cmp.Flips)
	}
	if cmp.BaseOnlyCases != 1 || cmp.CandidateOnly != 1 {
		t.Fatalf("only-in counts = %d/%d", cmp.BaseOnlyCases, cmp.CandidateOnly)
	}
}
