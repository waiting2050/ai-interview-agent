// Package evaluation defines the rubric output contract. The JSON shape is
// fixed by the development plan; this file also owns deterministic scoring,
// evidence checks and the persisted Record.
package evaluation

import (
	"math"
	"strings"
	"time"
	"unicode"
)

type Dimension string

const (
	DimensionCorrectness         Dimension = "correctness"
	DimensionDepth               Dimension = "depth"
	DimensionEngineeringJudgment Dimension = "engineering_judgment"
	DimensionStructure           Dimension = "structure"
	DimensionEvidenceCredibility Dimension = "evidence_credibility"
)

// DimensionOrder is the stable order used by prompts, scoring and validation.
var DimensionOrder = []Dimension{
	DimensionCorrectness,
	DimensionDepth,
	DimensionEngineeringJudgment,
	DimensionStructure,
	DimensionEvidenceCredibility,
}

// Weights mirrors the rubric table in the development plan.
var Weights = map[Dimension]float64{
	DimensionCorrectness:         0.30,
	DimensionDepth:               0.25,
	DimensionEngineeringJudgment: 0.20,
	DimensionStructure:           0.15,
	DimensionEvidenceCredibility: 0.10,
}

const (
	SchemaVersion = 1
	MaxBonus      = 8
	MaxPenalty    = -10
)

type Evidence struct {
	Dimension Dimension `json:"dimension"`
	Quote     string    `json:"quote"`
	Reason    string    `json:"reason"`
}

// Adjustment covers bonus and penalty entries. Points are applied after the
// weighted dimension score, never at the same time as a dimension deduction.
type Adjustment struct {
	Type   string `json:"type"`
	Quote  string `json:"quote"`
	Points int    `json:"points"`
	Reason string `json:"reason"`
}

type FollowupSuggestion struct {
	Needed bool   `json:"needed"`
	Target string `json:"target"`
	Reason string `json:"reason"`
}

type Result struct {
	SchemaVersion int                `json:"schema_version"`
	TurnID        string             `json:"turn_id"`
	Scores        map[Dimension]int  `json:"scores"`
	Evidence      []Evidence         `json:"evidence"`
	Bonus         []Adjustment       `json:"bonus"`
	Penalty       []Adjustment       `json:"penalty"`
	Confidence    float64            `json:"confidence"`
	MissingPoints []string           `json:"missing_points"`
	Followup      FollowupSuggestion `json:"followup"`
}

// Score is the deterministic decomposition of a rubric result.
type Score struct {
	Base    float64
	Bonus   float64
	Penalty float64
	Total   float64
}

// ComputeScore applies the formula from the plan:
// base = Σ(score_i / 5 × weight_i) × 100, total = clamp(base + bonus + penalty, 0, 100).
func ComputeScore(scores map[Dimension]int, bonus, penalty []Adjustment) Score {
	var base float64
	for _, d := range DimensionOrder {
		base += float64(scores[d]) / 5 * Weights[d] * 100
	}

	var bonusPoints, penaltyPoints float64
	for _, b := range bonus {
		bonusPoints += float64(b.Points)
	}
	if bonusPoints > MaxBonus {
		bonusPoints = MaxBonus
	}
	for _, p := range penalty {
		penaltyPoints += float64(p.Points)
	}
	if penaltyPoints < MaxPenalty {
		penaltyPoints = MaxPenalty
	}

	total := math.Max(0, math.Min(100, base+bonusPoints+penaltyPoints))
	return Score{Base: round1(base), Bonus: bonusPoints, Penalty: penaltyPoints, Total: round1(total)}
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// Sanitize drops evidence and adjustments whose quote does not appear in the
// answer, and clears a follow-up suggestion without a usable target. It is
// called before Validate so a single hallucinated quote does not fail the
// whole result.
func (r *Result) Sanitize(answer string) {
	evidence := r.Evidence[:0]
	for _, e := range r.Evidence {
		if isValidDimension(e.Dimension) && QuoteInAnswer(e.Quote, answer) {
			evidence = append(evidence, e)
		}
	}
	r.Evidence = evidence

	bonus := r.Bonus[:0]
	for _, b := range r.Bonus {
		if b.Points >= 1 && b.Points <= 3 && QuoteInAnswer(b.Quote, answer) {
			bonus = append(bonus, b)
		}
	}
	r.Bonus = bonus

	penalty := r.Penalty[:0]
	for _, p := range r.Penalty {
		if p.Points <= -1 && p.Points >= -3 && QuoteInAnswer(p.Quote, answer) {
			penalty = append(penalty, p)
		}
	}
	r.Penalty = penalty

	if r.Followup.Needed && strings.TrimSpace(r.Followup.Target) == "" {
		r.Followup = FollowupSuggestion{}
	}
}

// Validate returns the issues that must be repaired or, after the retry budget,
// degrade the evaluation. Sanitize must run first.
func (r *Result) Validate() []string {
	var issues []string

	if r.SchemaVersion != SchemaVersion {
		issues = append(issues, "schema_version must be 1")
	}
	for _, d := range DimensionOrder {
		score, ok := r.Scores[d]
		if !ok {
			issues = append(issues, "missing score for "+string(d))
			continue
		}
		if score < 1 || score > 5 {
			issues = append(issues, "score out of range for "+string(d))
		}
	}
	if r.Confidence < 0 || r.Confidence > 1 {
		issues = append(issues, "confidence must be in [0,1]")
	}
	if len(r.Evidence) == 0 {
		issues = append(issues, "evidence must cite at least one answer quote")
	}
	if r.Followup.Needed && strings.TrimSpace(r.Followup.Target) == "" {
		issues = append(issues, "followup.target is required when followup.needed is true")
	}
	return issues
}

func isValidDimension(d Dimension) bool {
	_, ok := Weights[d]
	return ok
}

// Band maps a 0-100 weighted total to the coarse band used by the scoring
// regression set: 1 covers the 1-2 range, 3 the 3 range, 5 the 4-5 range.
// The cut points follow the rubric scale: 50 is a uniform 2.5, 80 a uniform 4.
func Band(total float64) int {
	switch {
	case total < 50:
		return 1
	case total < 80:
		return 3
	default:
		return 5
	}
}

// QuoteInAnswer compares after stripping whitespace and punctuation so that
// quote matching survives copy/paste differences. Empty quotes never match.
// It is exported so the batch runner can re-check persisted evidence.
func QuoteInAnswer(quote, answer string) bool {
	q := normalize(quote)
	if q == "" {
		return false
	}
	return strings.Contains(normalize(answer), q)
}

func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Record is one persisted evaluation. A degraded record has empty scores,
// Degraded=true and an ErrorCode, and must never be used for scoring.
type Record struct {
	TurnID        string
	SessionID     string
	SchemaVersion int
	Scores        map[Dimension]int
	BaseScore     float64
	WeightedScore float64 // total after bonus/penalty
	Bonus         []Adjustment
	Penalty       []Adjustment
	Evidence      []Evidence
	MissingPoints []string
	Confidence    float64
	ModelName     string
	PromptVersion string
	RawOutput     string
	Degraded      bool
	ErrorCode     string
	CreatedAt     time.Time
}
