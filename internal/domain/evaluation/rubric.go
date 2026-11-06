// Package evaluation defines the rubric output contract. The JSON shape is
// fixed by the development plan; this file also owns deterministic scoring,
// evidence checks and the persisted Record.
package evaluation

import (
	"errors"
	"fmt"
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

// DimensionOrder is the stable order used by prompts, scoring and validation
// when no domain pack rubric is supplied.
var DimensionOrder = []Dimension{
	DimensionCorrectness,
	DimensionDepth,
	DimensionEngineeringJudgment,
	DimensionStructure,
	DimensionEvidenceCredibility,
}

// Weights mirrors the default rubric table in the development plan.
var Weights = map[Dimension]float64{
	DimensionCorrectness:         0.30,
	DimensionDepth:               0.25,
	DimensionEngineeringJudgment: 0.20,
	DimensionStructure:           0.15,
	DimensionEvidenceCredibility: 0.10,
}

// DimensionDef is one rubric row supplied by a domain pack. The evaluation
// package stays independent of the pack loader; callers convert pack
// dimensions into defs.
type DimensionDef struct {
	Key    string
	Name   string
	Weight float64
	Anchor string
}

// Rubric is a validated dimension table. Order drives prompts, validation,
// scoring and report rendering so a pack can rename or replace dimensions.
type Rubric struct {
	Order   []Dimension
	Weights map[Dimension]float64
	Names   map[Dimension]string
	Anchors map[Dimension]string
}

// DefaultRubric returns the five-dimension rubric used before M14.
func DefaultRubric() Rubric {
	names := map[Dimension]string{
		DimensionCorrectness:         "正确性",
		DimensionDepth:               "深度",
		DimensionEngineeringJudgment: "工程判断",
		DimensionStructure:           "表达结构",
		DimensionEvidenceCredibility: "证据可信度",
	}
	weights := make(map[Dimension]float64, len(Weights))
	for k, v := range Weights {
		weights[k] = v
	}
	return Rubric{
		Order:   append([]Dimension(nil), DimensionOrder...),
		Weights: weights,
		Names:   names,
	}
}

// NewRubric validates and converts pack dimensions. Weights must be positive
// and sum to 1 so the base score stays on the 0-100 scale.
func NewRubric(defs []DimensionDef) (Rubric, error) {
	if len(defs) == 0 {
		return Rubric{}, errors.New("rubric needs at least one dimension")
	}
	r := Rubric{
		Weights: make(map[Dimension]float64, len(defs)),
		Names:   make(map[Dimension]string, len(defs)),
		Anchors: make(map[Dimension]string, len(defs)),
	}
	sum := 0.0
	for i, d := range defs {
		key := strings.TrimSpace(d.Key)
		if key == "" {
			return Rubric{}, fmt.Errorf("rubric dimension[%d]: key is required", i)
		}
		dim := Dimension(key)
		if _, dup := r.Weights[dim]; dup {
			return Rubric{}, fmt.Errorf("rubric: duplicate dimension %q", key)
		}
		if d.Weight <= 0 || d.Weight > 1 {
			return Rubric{}, fmt.Errorf("rubric dimension %q: weight must be in (0,1], got %v", key, d.Weight)
		}
		name := strings.TrimSpace(d.Name)
		if name == "" {
			name = key
		}
		r.Order = append(r.Order, dim)
		r.Weights[dim] = d.Weight
		r.Names[dim] = name
		r.Anchors[dim] = strings.TrimSpace(d.Anchor)
		sum += d.Weight
	}
	if diff := sum - 1; diff > 0.001 || diff < -0.001 {
		return Rubric{}, fmt.Errorf("rubric weights must sum to 1, got %.3f", sum)
	}
	return r, nil
}

// IsZero reports whether a rubric was not supplied; callers then fall back to
// DefaultRubric.
func (r Rubric) IsZero() bool {
	return len(r.Order) == 0
}

// Name returns the human label for a dimension.
func (r Rubric) Name(d Dimension) string {
	if r.Names != nil {
		if name, ok := r.Names[d]; ok {
			return name
		}
	}
	if name, ok := DefaultRubric().Names[d]; ok {
		return name
	}
	return string(d)
}

// Weight returns the dimension weight, falling back to the default table.
func (r Rubric) Weight(d Dimension) float64 {
	if r.Weights != nil {
		if w, ok := r.Weights[d]; ok {
			return w
		}
	}
	return Weights[d]
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

// ComputeScore applies the formula from the plan with the default rubric:
// base = Σ(score_i / 5 × weight_i) × 100, total = clamp(base + bonus + penalty, 0, 100).
func ComputeScore(scores map[Dimension]int, bonus, penalty []Adjustment) Score {
	return ComputeScoreWith(scores, bonus, penalty, DefaultRubric())
}

// ComputeScoreWith applies the weighted formula using the supplied rubric.
func ComputeScoreWith(scores map[Dimension]int, bonus, penalty []Adjustment, rubric Rubric) Score {
	if rubric.IsZero() {
		rubric = DefaultRubric()
	}
	var base float64
	for _, d := range rubric.Order {
		base += float64(scores[d]) / 5 * rubric.Weight(d) * 100
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
	r.SanitizeWith(answer, DefaultRubric())
}

// SanitizeWith drops evidence for dimensions outside the supplied rubric.
func (r *Result) SanitizeWith(answer string, rubric Rubric) {
	if rubric.IsZero() {
		rubric = DefaultRubric()
	}
	evidence := r.Evidence[:0]
	for _, e := range r.Evidence {
		if rubricHas(rubric, e.Dimension) && QuoteInAnswer(e.Quote, answer) {
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
	return r.ValidateWith(DefaultRubric())
}

// ValidateWith checks scores against the supplied rubric order.
func (r *Result) ValidateWith(rubric Rubric) []string {
	if rubric.IsZero() {
		rubric = DefaultRubric()
	}
	var issues []string

	if r.SchemaVersion != SchemaVersion {
		issues = append(issues, "schema_version must be 1")
	}
	for _, d := range rubric.Order {
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

func rubricHas(rubric Rubric, d Dimension) bool {
	_, ok := rubric.Weights[d]
	return ok
}

// UniqueEvidence collapses repeated quotes while keeping the first annotated
// dimension. The heuristic evaluator cites one quote per expected point, so a
// single answer fragment can otherwise appear several times in the same record.
func UniqueEvidence(in []Evidence) []Evidence {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]Evidence, 0, len(in))
	for _, e := range in {
		key := normalize(e.Quote)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	return out
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
