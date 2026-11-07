package eval

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
)

// Evaluator is the production answer-evaluation path. The batch runner does
// not re-implement any scoring; it only feeds cases in and reads results out.
type Evaluator interface {
	Evaluate(ctx context.Context, in interview.EvaluateInput) (interview.Outcome, error)
}

// Meta is provenance written into every result file.
type Meta struct {
	DatasetPath    string
	DatasetSHA256  string
	Provider       string
	Model          string
	BaseURL        string
	Fallback       bool
	FallbackReason string
	PromptVersion  string
}

// Thresholds are the acceptance gates from development-plan 6.5.
type Thresholds struct {
	SchemaValidRate   float64 `json:"schema_valid_rate"`
	BandHitRate       float64 `json:"band_hit_rate"`
	AdjacentBandRate  float64 `json:"adjacent_band_rate"`
	DimensionMAE      float64 `json:"dimension_mae"`
	EvidenceQuoteRate float64 `json:"evidence_quote_rate"`
}

// DefaultThresholds returns a fresh copy so callers cannot mutate the shared
// acceptance bar.
func DefaultThresholds() Thresholds {
	return Thresholds{
		SchemaValidRate:   1.0,
		BandHitRate:       0.8,
		AdjacentBandRate:  0.95,
		DimensionMAE:      0.6,
		EvidenceQuoteRate: 1.0,
	}
}

// Deviation is one dimension whose actual score moved by at least one point.
type Deviation struct {
	Dimension evaluation.Dimension `json:"dimension"`
	Expected  int                  `json:"expected"`
	Actual    int                  `json:"actual"`
	Delta     int                  `json:"delta"`
}

// CaseResult is the per-case detail kept in the result file.
type CaseResult struct {
	CaseID           string                       `json:"case_id"`
	Domain           string                       `json:"domain"`
	Type             string                       `json:"type"`
	Topic            string                       `json:"topic"`
	EvidenceAnchor   string                       `json:"evidence_anchor"`
	ExpectedBand     int                          `json:"expected_band"`
	ActualBand       int                          `json:"actual_band"`
	BandDelta        int                          `json:"band_delta"`
	TotalScore       float64                      `json:"total_score"`
	ExpectedScores   map[evaluation.Dimension]int `json:"expected_scores"`
	ActualScores     map[evaluation.Dimension]int `json:"actual_scores,omitempty"`
	Deviations       []Deviation                  `json:"deviations,omitempty"`
	Degraded         bool                         `json:"degraded"`
	ErrorCode        string                       `json:"error_code,omitempty"`
	Error            string                       `json:"error,omitempty"`
	Attempts         int                          `json:"attempts"`
	EvidenceQuotes   int                          `json:"evidence_quotes"`
	EvidenceQuoteOK  bool                         `json:"evidence_quote_ok"`
	PromptTokens     int                          `json:"prompt_tokens"`
	CompletionTokens int                          `json:"completion_tokens"`
	LatencyMS        int64                        `json:"latency_ms"`
	Notes            string                       `json:"notes"`
}

// Metrics are the aggregate numbers used for the gate and for version-over-
// version comparison.
type Metrics struct {
	Total               int     `json:"total"`
	Evaluated           int     `json:"evaluated"`
	Degraded            int     `json:"degraded"`
	Errors              int     `json:"errors"`
	SchemaValidRate     float64 `json:"schema_valid_rate"`
	BandHits            int     `json:"band_hits"`
	BandHitRate         float64 `json:"band_hit_rate"`
	AdjacentHits        int     `json:"adjacent_hits"`
	AdjacentBandRate    float64 `json:"adjacent_band_rate"`
	DimensionMAE        float64 `json:"dimension_mae"`
	EvidenceQuoteRate   float64 `json:"evidence_quote_rate"`
	AvgPromptTokens     float64 `json:"avg_prompt_tokens"`
	AvgCompletionTokens float64 `json:"avg_completion_tokens"`
	AvgLatencyMS        float64 `json:"avg_latency_ms"`
}

// Gate records whether the measured metrics meet Thresholds.
type Gate struct {
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
}

// Report is the JSON artifact written under docs/eval-results.
type Report struct {
	GeneratedAt         time.Time      `json:"generated_at"`
	DatasetPath         string         `json:"dataset_path"`
	DatasetSHA256       string         `json:"dataset_sha256"`
	Provider            string         `json:"provider"`
	Model               string         `json:"model"`
	BaseURL             string         `json:"base_url,omitempty"`
	Fallback            bool           `json:"fallback"`
	FallbackReason      string         `json:"fallback_reason,omitempty"`
	PromptVersion       string         `json:"prompt_version"`
	RubricSchemaVersion int            `json:"rubric_schema_version"`
	Thresholds          Thresholds     `json:"thresholds"`
	Distribution        map[string]int `json:"distribution"`
	DomainDistribution  map[string]int `json:"domain_distribution"`
	Metrics             Metrics        `json:"metrics"`
	MissesByType        map[string]int `json:"misses_by_type"`
	MissesByDomain      map[string]int `json:"misses_by_domain"`
	Gate                Gate           `json:"gate"`
	Cases               []CaseResult   `json:"cases"`
}

// Run executes every case through the evaluator and computes the metrics. It
// never mutates the dataset: a miss stays a miss.
func Run(ctx context.Context, ev Evaluator, ds *Dataset, meta Meta) Report {
	report := Report{
		GeneratedAt:         time.Now().UTC(),
		DatasetPath:         meta.DatasetPath,
		DatasetSHA256:       meta.DatasetSHA256,
		Provider:            meta.Provider,
		Model:               meta.Model,
		BaseURL:             meta.BaseURL,
		Fallback:            meta.Fallback,
		FallbackReason:      meta.FallbackReason,
		PromptVersion:       meta.PromptVersion,
		RubricSchemaVersion: evaluation.SchemaVersion,
		Thresholds:          DefaultThresholds(),
		Distribution:        ds.TypeCounts(),
		DomainDistribution:  ds.DomainCounts(),
		MissesByType:        map[string]int{},
		MissesByDomain:      map[string]int{},
		Cases:               make([]CaseResult, 0, len(ds.Cases)),
	}

	for _, c := range ds.Cases {
		cr := CaseResult{
			CaseID:         c.CaseID,
			Domain:         c.Domain,
			Type:           c.Type,
			Topic:          c.Topic,
			EvidenceAnchor: c.EvidenceAnchor,
			ExpectedBand:   c.ExpectedBand,
			ExpectedScores: c.ExpectedScores,
			Notes:          c.Notes,
		}

		start := time.Now()
		out, err := ev.Evaluate(ctx, interview.EvaluateInput{
			SessionID:      "eval-" + c.CaseID,
			TurnID:         c.CaseID,
			Topic:          c.Topic,
			Question:       c.Question,
			Answer:         c.Answer,
			TargetRole:     "后端工程师（Go/MySQL/Redis）",
			Difficulty:     c.Difficulty,
			ExpectedPoints: c.ExpectedPoints,
		})
		cr.LatencyMS = time.Since(start).Milliseconds()

		if err != nil {
			cr.Error = err.Error()
			cr.Degraded = true
			report.Cases = append(report.Cases, cr)
			continue
		}

		cr.Degraded = out.Degraded
		cr.ErrorCode = out.ErrorCode
		cr.Attempts = out.Attempts
		cr.PromptTokens = out.PromptTokens
		cr.CompletionTokens = out.CompletionTokens
		cr.EvidenceQuotes = len(out.Result.Evidence)

		if out.Degraded {
			report.Cases = append(report.Cases, cr)
			continue
		}

		score := evaluation.ComputeScore(out.Result.Scores, out.Result.Bonus, out.Result.Penalty)
		cr.TotalScore = score.Total
		cr.ActualBand = evaluation.Band(score.Total)
		cr.BandDelta = bandIndex(cr.ActualBand) - bandIndex(cr.ExpectedBand)
		cr.ActualScores = out.Result.Scores
		cr.Deviations = deviations(c.ExpectedScores, out.Result.Scores)
		cr.EvidenceQuoteOK = evidenceQuotesOK(out.Result.Evidence, c.Answer)

		report.Cases = append(report.Cases, cr)
	}

	report.Metrics = computeMetrics(report.Cases)
	report.MissesByType = missesByType(report.Cases)
	report.MissesByDomain = missesByDomain(report.Cases)
	report.Gate = checkGate(report.Metrics, report.Thresholds)
	return report
}

func bandIndex(b int) int {
	switch b {
	case 1:
		return 0
	case 3:
		return 1
	case 5:
		return 2
	default:
		return -1
	}
}

func deviations(expected, actual map[evaluation.Dimension]int) []Deviation {
	var out []Deviation
	for _, dim := range evaluation.DimensionOrder {
		exp, okExp := expected[dim]
		act, okAct := actual[dim]
		if !okExp || !okAct {
			continue
		}
		if exp == act {
			continue
		}
		out = append(out, Deviation{Dimension: dim, Expected: exp, Actual: act, Delta: act - exp})
	}
	return out
}

func evidenceQuotesOK(evidence []evaluation.Evidence, answer string) bool {
	if len(evidence) == 0 {
		return false
	}
	for _, e := range evidence {
		if !evaluation.QuoteInAnswer(e.Quote, answer) {
			return false
		}
	}
	return true
}

func computeMetrics(cases []CaseResult) Metrics {
	m := Metrics{Total: len(cases)}

	var maeSum float64
	var maeCount int
	var evidenceOK int
	var tokensPrompt, tokensCompletion int
	var latency int64

	for _, c := range cases {
		tokensPrompt += c.PromptTokens
		tokensCompletion += c.CompletionTokens
		latency += c.LatencyMS

		switch {
		case c.Error != "":
			m.Errors++
			continue
		case c.Degraded:
			m.Degraded++
			continue
		}

		m.Evaluated++
		if c.ActualBand == c.ExpectedBand {
			m.BandHits++
		}
		if absInt(c.BandDelta) <= 1 {
			m.AdjacentHits++
		}
		if c.EvidenceQuoteOK {
			evidenceOK++
		}
		for _, d := range c.Deviations {
			maeSum += math.Abs(float64(d.Delta))
			maeCount++
		}
		// Dimensions that matched also contribute a zero error; count them so
		// the MAE is averaged over every labelled dimension, not only misses.
		maeCount += (len(evaluation.DimensionOrder) - len(c.Deviations))
	}

	m.SchemaValidRate = ratio(m.Evaluated, m.Total)
	if m.Evaluated > 0 {
		m.BandHitRate = round4(float64(m.BandHits) / float64(m.Evaluated))
		m.AdjacentBandRate = round4(float64(m.AdjacentHits) / float64(m.Evaluated))
		m.EvidenceQuoteRate = round4(float64(evidenceOK) / float64(m.Evaluated))
	}
	if maeCount > 0 {
		m.DimensionMAE = round4(maeSum / float64(maeCount))
	}
	if m.Total > 0 {
		m.AvgPromptTokens = round4(float64(tokensPrompt) / float64(m.Total))
		m.AvgCompletionTokens = round4(float64(tokensCompletion) / float64(m.Total))
		m.AvgLatencyMS = round4(float64(latency) / float64(m.Total))
	}
	return m
}

func missesByType(cases []CaseResult) map[string]int {
	out := map[string]int{}
	for _, c := range cases {
		if c.Error != "" || c.Degraded {
			out[c.Type]++
			continue
		}
		if c.ActualBand != c.ExpectedBand {
			out[c.Type]++
		}
	}
	return out
}

func missesByDomain(cases []CaseResult) map[string]int {
	out := map[string]int{}
	for _, c := range cases {
		if c.Error != "" || c.Degraded || c.ActualBand != c.ExpectedBand {
			out[c.Domain]++
		}
	}
	return out
}

func checkGate(m Metrics, t Thresholds) Gate {
	var failures []string
	if m.SchemaValidRate < t.SchemaValidRate {
		failures = append(failures, fmt.Sprintf("schema_valid_rate %.4f < %.4f", m.SchemaValidRate, t.SchemaValidRate))
	}
	if m.BandHitRate < t.BandHitRate {
		failures = append(failures, fmt.Sprintf("band_hit_rate %.4f < %.4f", m.BandHitRate, t.BandHitRate))
	}
	if m.AdjacentBandRate < t.AdjacentBandRate {
		failures = append(failures, fmt.Sprintf("adjacent_band_rate %.4f < %.4f", m.AdjacentBandRate, t.AdjacentBandRate))
	}
	if m.DimensionMAE > t.DimensionMAE {
		failures = append(failures, fmt.Sprintf("dimension_mae %.4f > %.4f", m.DimensionMAE, t.DimensionMAE))
	}
	if m.EvidenceQuoteRate < t.EvidenceQuoteRate {
		failures = append(failures, fmt.Sprintf("evidence_quote_rate %.4f < %.4f", m.EvidenceQuoteRate, t.EvidenceQuoteRate))
	}
	return Gate{Passed: len(failures) == 0, Failures: failures}
}

func ratio(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return round4(float64(part) / float64(total))
}

func round4(v float64) float64 {
	return math.Round(v*10000) / 10000
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
