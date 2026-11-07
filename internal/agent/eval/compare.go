package eval

import (
	"fmt"
	"sort"
)

// MetricDelta is one gate metric compared between two result files.
type MetricDelta struct {
	Name      string  `json:"name"`
	Base      float64 `json:"base"`
	Candidate float64 `json:"candidate"`
	Delta     float64 `json:"delta"`
	// LowerIsBetter is true for error-style metrics such as dimension_mae.
	LowerIsBetter bool `json:"lower_is_better"`
	Improved      bool `json:"improved"`
}

// Flip is one case whose band judgement changed between the two files.
type Flip struct {
	CaseID       string  `json:"case_id"`
	Domain       string  `json:"domain"`
	Topic        string  `json:"topic"`
	ExpectedBand int     `json:"expected_band"`
	BaseBand     int     `json:"base_band"`
	BaseScore    float64 `json:"base_score"`
	CandBand     int     `json:"candidate_band"`
	CandScore    float64 `json:"candidate_score"`
	// Direction is improved (candidate now matches the label), regressed
	// (base matched and candidate does not) or changed (neither matched).
	Direction string `json:"direction"`
}

// FileSummary identifies a compared result file.
type FileSummary struct {
	Path        string `json:"path"`
	GeneratedAt string `json:"generated_at"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Total       int    `json:"total"`
	GatePassed  bool   `json:"gate_passed"`
}

// Comparison is the read-only A/B diff between two reports.
type Comparison struct {
	Base             FileSummary   `json:"base"`
	Candidate        FileSummary   `json:"candidate"`
	Metrics          []MetricDelta `json:"metrics"`
	BaseOnlyCases    int           `json:"base_only_cases"`
	CandidateOnly    int           `json:"candidate_only_cases"`
	Flips            []Flip        `json:"flips"`
	ImprovedFlips    int           `json:"improved_flips"`
	RegressedFlips   int           `json:"regressed_flips"`
	ChangedFlips     int           `json:"changed_flips"`
	GateStatusChange string        `json:"gate_status_change"`
}

// Compare diffs two existing result files. It performs no model calls, so it
// works even when no API key is configured.
func Compare(base, candidate Report, basePath, candidatePath string) Comparison {
	cmp := Comparison{
		Base:      summarize(base, basePath),
		Candidate: summarize(candidate, candidatePath),
		Metrics: []MetricDelta{
			metricDelta("schema_valid_rate", base.Metrics.SchemaValidRate, candidate.Metrics.SchemaValidRate, false),
			metricDelta("band_hit_rate", base.Metrics.BandHitRate, candidate.Metrics.BandHitRate, false),
			metricDelta("adjacent_band_rate", base.Metrics.AdjacentBandRate, candidate.Metrics.AdjacentBandRate, false),
			metricDelta("dimension_mae", base.Metrics.DimensionMAE, candidate.Metrics.DimensionMAE, true),
			metricDelta("evidence_quote_rate", base.Metrics.EvidenceQuoteRate, candidate.Metrics.EvidenceQuoteRate, false),
		},
	}

	baseCases := indexCases(base.Cases)
	candCases := indexCases(candidate.Cases)
	for id, bc := range baseCases {
		cc, ok := candCases[id]
		if !ok {
			cmp.BaseOnlyCases++
			continue
		}
		if bc.Error != "" || bc.Degraded || cc.Error != "" || cc.Degraded {
			continue
		}
		if bc.ActualBand == cc.ActualBand {
			continue
		}
		if bc.ActualBand == 0 || cc.ActualBand == 0 {
			continue
		}
		flip := Flip{
			CaseID:       id,
			Domain:       cc.Domain,
			Topic:        cc.Topic,
			ExpectedBand: cc.ExpectedBand,
			BaseBand:     bc.ActualBand,
			BaseScore:    bc.TotalScore,
			CandBand:     cc.ActualBand,
			CandScore:    cc.TotalScore,
		}
		switch {
		case cc.ActualBand == cc.ExpectedBand && bc.ActualBand != bc.ExpectedBand:
			flip.Direction = "improved"
			cmp.ImprovedFlips++
		case bc.ActualBand == bc.ExpectedBand && cc.ActualBand != cc.ExpectedBand:
			flip.Direction = "regressed"
			cmp.RegressedFlips++
		default:
			flip.Direction = "changed"
			cmp.ChangedFlips++
		}
		cmp.Flips = append(cmp.Flips, flip)
	}
	for id := range candCases {
		if _, ok := baseCases[id]; !ok {
			cmp.CandidateOnly++
		}
	}
	sort.Slice(cmp.Flips, func(i, j int) bool {
		if cmp.Flips[i].Direction != cmp.Flips[j].Direction {
			return cmp.Flips[i].Direction < cmp.Flips[j].Direction
		}
		return cmp.Flips[i].CaseID < cmp.Flips[j].CaseID
	})
	switch {
	case base.Gate.Passed && !candidate.Gate.Passed:
		cmp.GateStatusChange = "passed -> failed"
	case !base.Gate.Passed && candidate.Gate.Passed:
		cmp.GateStatusChange = "failed -> passed"
	default:
		cmp.GateStatusChange = "unchanged"
	}
	return cmp
}

// MetricDeltaString renders one delta for CLI output.
func (m MetricDelta) String() string {
	sign := "+"
	if m.Delta < 0 {
		sign = ""
	}
	flag := ""
	if m.Improved {
		flag = " improved"
	}
	return fmt.Sprintf("%-22s base=%.4f candidate=%.4f delta=%s%.4f%s", m.Name, m.Base, m.Candidate, sign, m.Delta, flag)
}

func summarize(r Report, path string) FileSummary {
	return FileSummary{
		Path:        path,
		GeneratedAt: r.GeneratedAt.Format("2006-01-02T15:04:05Z07:00"),
		Provider:    r.Provider,
		Model:       r.Model,
		Total:       r.Metrics.Total,
		GatePassed:  r.Gate.Passed,
	}
}

func metricDelta(name string, base, candidate float64, lowerIsBetter bool) MetricDelta {
	delta := candidate - base
	improved := delta > 0
	if lowerIsBetter {
		improved = delta < 0
	}
	if delta == 0 {
		improved = false
	}
	return MetricDelta{
		Name:          name,
		Base:          base,
		Candidate:     candidate,
		Delta:         round4(delta),
		LowerIsBetter: lowerIsBetter,
		Improved:      improved,
	}
}

func indexCases(cases []CaseResult) map[string]CaseResult {
	out := make(map[string]CaseResult, len(cases))
	for _, c := range cases {
		out[c.CaseID] = c
	}
	return out
}
