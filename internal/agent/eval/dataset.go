// Package eval owns the scoring regression set and the repeatable batch
// runner. It is a developer tool: production traffic never imports it.
package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
)

// Case types in the regression set. They are used for attribution summaries,
// not for scoring.
const (
	TypeHigh        = "high"
	TypeMid         = "mid"
	TypeLow         = "low"
	TypeEmpty       = "empty"
	TypeExaggerated = "exaggerated"
	TypeOffTopic    = "off_topic"
)

const (
	MinCases = 20
	MaxCases = 40
)

var caseTypes = map[string]bool{
	TypeHigh:        true,
	TypeMid:         true,
	TypeLow:         true,
	TypeEmpty:       true,
	TypeExaggerated: true,
	TypeOffTopic:    true,
}

// Case is one labelled answer. ExpectedScores are the human per-dimension
// labels; ExpectedBand is the coarse band those labels must add up to.
type Case struct {
	CaseID         string                       `json:"case_id"`
	Type           string                       `json:"type"`
	Topic          string                       `json:"topic"`
	Difficulty     string                       `json:"difficulty"`
	Question       string                       `json:"question"`
	Answer         string                       `json:"answer"`
	ExpectedPoints []string                     `json:"expected_points,omitempty"`
	ExpectedBand   int                          `json:"expected_band"`
	ExpectedScores map[evaluation.Dimension]int `json:"expected_scores"`
	Notes          string                       `json:"notes"`
}

// Dataset is the versioned regression file.
type Dataset struct {
	Version int    `json:"version"`
	Cases   []Case `json:"cases"`
}

// LoadDataset decodes and validates a regression file. Unknown fields are
// rejected so a typo in a label cannot silently change the metrics.
func LoadDataset(path string) (*Dataset, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read regression set %s: %w", path, err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var ds Dataset
	if err := dec.Decode(&ds); err != nil {
		return nil, fmt.Errorf("decode regression set %s: %w", path, err)
	}
	if err := ds.Validate(); err != nil {
		return nil, fmt.Errorf("regression set %s: %w", path, err)
	}
	if err := ds.ValidateSize(); err != nil {
		return nil, fmt.Errorf("regression set %s: %w", path, err)
	}
	return &ds, nil
}

// FileDigest returns the sha256 of the dataset file so two result files can be
// compared only when they ran against the same inputs.
func FileDigest(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Validate checks the structural contract of every case.
func (d *Dataset) Validate() error {
	if d.Version < 1 {
		return fmt.Errorf("version must be >= 1")
	}
	if len(d.Cases) == 0 {
		return fmt.Errorf("cases must not be empty")
	}

	seen := map[string]bool{}
	for i, c := range d.Cases {
		where := fmt.Sprintf("case[%d]", i)
		if strings.TrimSpace(c.CaseID) == "" {
			return fmt.Errorf("%s: case_id is required", where)
		}
		where = c.CaseID
		if seen[c.CaseID] {
			return fmt.Errorf("%s: duplicate case_id", where)
		}
		seen[c.CaseID] = true

		if !caseTypes[c.Type] {
			return fmt.Errorf("%s: type %q is not one of high/mid/low/empty/exaggerated/off_topic", where, c.Type)
		}
		if strings.TrimSpace(c.Question) == "" || strings.TrimSpace(c.Answer) == "" {
			return fmt.Errorf("%s: question and answer are required", where)
		}
		if strings.TrimSpace(c.Notes) == "" {
			return fmt.Errorf("%s: notes must record why this band was chosen", where)
		}
		if c.ExpectedBand != 1 && c.ExpectedBand != 3 && c.ExpectedBand != 5 {
			return fmt.Errorf("%s: expected_band must be 1, 3 or 5, got %d", where, c.ExpectedBand)
		}
		if len(c.ExpectedScores) != len(evaluation.DimensionOrder) {
			return fmt.Errorf("%s: expected_scores must score all %d dimensions", where, len(evaluation.DimensionOrder))
		}
		for _, dim := range evaluation.DimensionOrder {
			score, ok := c.ExpectedScores[dim]
			if !ok {
				return fmt.Errorf("%s: expected_scores missing %s", where, dim)
			}
			if score < 1 || score > 5 {
				return fmt.Errorf("%s: expected score for %s must be in [1,5], got %d", where, dim, score)
			}
		}
		derived := evaluation.Band(evaluation.ComputeScore(c.ExpectedScores, nil, nil).Total)
		if derived != c.ExpectedBand {
			return fmt.Errorf("%s: expected_scores derive band %d, but expected_band is %d", where, derived, c.ExpectedBand)
		}
	}
	return nil
}

// ValidateSize enforces the plan's 20-40 case budget.
func (d *Dataset) ValidateSize() error {
	if len(d.Cases) < MinCases || len(d.Cases) > MaxCases {
		return fmt.Errorf("regression set must contain %d-%d cases, got %d", MinCases, MaxCases, len(d.Cases))
	}
	return nil
}

// TypeCounts returns the number of cases per type, sorted by type name.
func (d *Dataset) TypeCounts() map[string]int {
	counts := map[string]int{}
	for _, c := range d.Cases {
		counts[c.Type]++
	}
	return counts
}

// SortedTypes returns the type keys in a stable order for printing.
func SortedTypes(counts map[string]int) []string {
	out := make([]string, 0, len(counts))
	for k := range counts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
