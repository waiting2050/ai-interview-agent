package resume

import (
	"embed"
	"fmt"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed rules/checks.yaml
var rulesFS embed.FS

// SegmentType is how a resume entry is judged. Internships and personal
// projects use different rubrics, so the type is part of the input, never a
// hidden guess.
type SegmentType string

const (
	TypeInternship      SegmentType = "internship"
	TypePersonalProject SegmentType = "personal_project"
	TypeOpenSource      SegmentType = "opensource"
	TypeCompetition     SegmentType = "competition"
	TypeCoursework      SegmentType = "coursework"
	TypeUnknown         SegmentType = "unknown"
)

var segmentTypeOrder = []SegmentType{
	TypeInternship, TypePersonalProject, TypeOpenSource, TypeCompetition, TypeCoursework, TypeUnknown,
}

// IsValidSegmentType reports whether raw is one of the known experience types.
func IsValidSegmentType(raw string) bool {
	for _, t := range segmentTypeOrder {
		if string(t) == raw {
			return true
		}
	}
	return false
}

// SegmentTypeOptions returns all allowed types in a stable order.
func SegmentTypeOptions() []SegmentType {
	out := make([]SegmentType, len(segmentTypeOrder))
	copy(out, segmentTypeOrder)
	return out
}

// Dimension is one research-rubric dimension (A–E) and its weight.
type Dimension struct {
	ID     string `yaml:"id" json:"id"`
	Name   string `yaml:"name" json:"name"`
	Weight int    `yaml:"weight" json:"weight"`
}

// TypedDimension is one dimension of a per-experience rubric (e.g. internship).
type TypedDimension struct {
	ID     string `yaml:"id" json:"id"`
	Name   string `yaml:"name" json:"name"`
	Weight int    `yaml:"weight" json:"weight"`
}

// TypedProfile maps research dimensions to typed dimensions and weights.
type TypedProfile struct {
	ID         string            `yaml:"id" json:"id"`
	Name       string            `yaml:"name" json:"name"`
	Dimensions []TypedDimension  `yaml:"dimensions" json:"dimensions"`
	Map        map[string]string `yaml:"map" json:"-"`
}

func (p TypedProfile) dimensionByID(id string) (TypedDimension, bool) {
	for _, d := range p.Dimensions {
		if d.ID == id {
			return d, true
		}
	}
	return TypedDimension{}, false
}

// Check is one executable rule from docs/resume-rules.md.
type Check struct {
	ID            string         `yaml:"id"`
	Title         string         `yaml:"title"`
	Dimension     string         `yaml:"dimension"`
	Mode          string         `yaml:"mode"`
	Severity      string         `yaml:"severity"`
	Evidence      string         `yaml:"evidence"`
	AppliesTo     []SegmentType  `yaml:"applies_to"`
	Deduction     map[string]int `yaml:"deduction"`
	AutoRatio     float64        `yaml:"auto_ratio"`
	Redline       bool           `yaml:"redline"`
	Note          string         `yaml:"note"`
	RewriteBefore string         `yaml:"rewrite_before"`
	RewriteAfter  string         `yaml:"rewrite_after"`
}

// DeductionFor returns the points this rule takes from the given experience
// type. Unknown types are never auto-scored, so callers skip them first.
func (c Check) DeductionFor(t SegmentType) int {
	if v, ok := c.Deduction[string(t)]; ok {
		return v
	}
	if v, ok := c.Deduction["default"]; ok {
		return v
	}
	return 0
}

// AppliesToType reports whether the rule covers the given experience type.
func (c Check) AppliesToType(t SegmentType) bool {
	for _, candidate := range c.AppliesTo {
		if candidate == t {
			return true
		}
	}
	return false
}

func (c Check) autoScoreRatio() float64 {
	if c.Mode == "auto" || c.AutoRatio <= 0 {
		return 1
	}
	return c.AutoRatio
}

// Ruleset is the versioned external configuration. It must stay offline and
// deterministic: every score comes from this file, never from the network.
type Ruleset struct {
	Version       string         `yaml:"version"`
	Dimensions    []Dimension    `yaml:"dimensions"`
	TypedProfiles []TypedProfile `yaml:"typed_profiles"`
	Checks        []Check        `yaml:"checks"`

	checksByID map[string]Check
	profiles   map[SegmentType]TypedProfile
}

// LoadRuleset parses and validates YAML. Any invalid pack fails loudly so a
// broken rule file cannot silently change scores.
func LoadRuleset(data []byte) (*Ruleset, error) {
	var rs Ruleset
	if err := yaml.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("resume rules yaml: %w", err)
	}
	if err := rs.validate(); err != nil {
		return nil, err
	}
	return &rs, nil
}

func (rs *Ruleset) validate() error {
	if len(rs.Dimensions) == 0 {
		return fmt.Errorf("resume rules: no dimensions")
	}
	sum := 0
	seenDim := map[string]bool{}
	for _, d := range rs.Dimensions {
		if d.ID == "" || d.Name == "" {
			return fmt.Errorf("resume rules: dimension id and name are required")
		}
		if seenDim[d.ID] {
			return fmt.Errorf("resume rules: duplicate dimension %s", d.ID)
		}
		seenDim[d.ID] = true
		if d.Weight <= 0 {
			return fmt.Errorf("resume rules: dimension %s weight must be positive", d.ID)
		}
		sum += d.Weight
	}
	if sum != 100 {
		return fmt.Errorf("resume rules: dimension weights sum to %d, want 100", sum)
	}

	profiles := map[SegmentType]TypedProfile{}
	for _, p := range rs.TypedProfiles {
		if !IsValidSegmentType(p.ID) || p.ID == string(TypeUnknown) {
			return fmt.Errorf("resume rules: invalid typed profile %q", p.ID)
		}
		if len(p.Dimensions) == 0 {
			return fmt.Errorf("resume rules: typed profile %s has no dimensions", p.ID)
		}
		wsum := 0
		for _, d := range p.Dimensions {
			if d.ID == "" || d.Name == "" || d.Weight <= 0 {
				return fmt.Errorf("resume rules: typed profile %s has an invalid dimension", p.ID)
			}
			wsum += d.Weight
		}
		if wsum != 100 {
			return fmt.Errorf("resume rules: typed profile %s weights sum to %d, want 100", p.ID, wsum)
		}
		for researchDim, typedDim := range p.Map {
			if !seenDim[researchDim] {
				return fmt.Errorf("resume rules: typed profile %s maps unknown dimension %s", p.ID, researchDim)
			}
			if _, ok := p.dimensionByID(typedDim); !ok {
				return fmt.Errorf("resume rules: typed profile %s maps to unknown dimension %s", p.ID, typedDim)
			}
		}
		profiles[SegmentType(p.ID)] = p
	}

	checks := map[string]Check{}
	for _, c := range rs.Checks {
		if c.ID == "" || c.Title == "" {
			return fmt.Errorf("resume rules: check id and title are required")
		}
		if _, ok := checks[c.ID]; ok {
			return fmt.Errorf("resume rules: duplicate check %s", c.ID)
		}
		switch c.Mode {
		case "auto", "auto_manual", "manual":
		default:
			return fmt.Errorf("resume rules: check %s has invalid mode %q", c.ID, c.Mode)
		}
		switch c.Severity {
		case "high", "medium", "low":
		default:
			return fmt.Errorf("resume rules: check %s has invalid severity %q", c.ID, c.Severity)
		}
		if !seenDim[c.Dimension] {
			return fmt.Errorf("resume rules: check %s references unknown dimension %s", c.ID, c.Dimension)
		}
		if len(c.AppliesTo) == 0 {
			return fmt.Errorf("resume rules: check %s has no applies_to", c.ID)
		}
		for _, t := range c.AppliesTo {
			if !IsValidSegmentType(string(t)) || t == TypeUnknown {
				return fmt.Errorf("resume rules: check %s applies to invalid type %q", c.ID, t)
			}
		}
		if c.Mode != "manual" && c.DeductionFor(TypeInternship) == 0 && c.DeductionFor(TypePersonalProject) == 0 && c.Deduction["default"] == 0 {
			return fmt.Errorf("resume rules: check %s has no deduction", c.ID)
		}
		c.AppliesTo = dedupeSegmentTypes(c.AppliesTo)
		checks[c.ID] = c
	}

	rs.Dimensions = append([]Dimension(nil), rs.Dimensions...)
	rs.TypedProfiles = append([]TypedProfile(nil), rs.TypedProfiles...)
	rs.Checks = append([]Check(nil), rs.Checks...)
	rs.checksByID = checks
	rs.profiles = profiles
	return nil
}

func dedupeSegmentTypes(in []SegmentType) []SegmentType {
	seen := map[SegmentType]bool{}
	out := make([]SegmentType, 0, len(in))
	for _, t := range in {
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// DimensionByID returns the research dimension with the given id.
func (rs *Ruleset) DimensionByID(id string) (Dimension, bool) {
	for _, d := range rs.Dimensions {
		if d.ID == id {
			return d, true
		}
	}
	return Dimension{}, false
}

// ProfileFor returns the typed rubric for an experience type. Project-like
// types reuse the personal-project rubric, which is documented as a product
// decision in docs/resume-rules-typed.md.
func (rs *Ruleset) ProfileFor(t SegmentType) (TypedProfile, bool) {
	switch t {
	case TypeInternship:
		p, ok := rs.profiles[TypeInternship]
		return p, ok
	case TypePersonalProject, TypeOpenSource, TypeCompetition, TypeCoursework:
		p, ok := rs.profiles[TypePersonalProject]
		return p, ok
	default:
		return TypedProfile{}, false
	}
}

// CheckByID returns one rule by id.
func (rs *Ruleset) CheckByID(id string) (Check, bool) {
	c, ok := rs.checksByID[id]
	return c, ok
}

// ChecksSorted returns checks in stable ID order (tests and docs rely on it).
func (rs *Ruleset) ChecksSorted() []Check {
	out := append([]Check(nil), rs.Checks...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

var (
	defaultOnce sync.Once
	defaultSet  *Ruleset
	defaultErr  error
)

// DefaultRuleset loads the embedded YAML once. A malformed embedded file is a
// programming error: the server should fail to start rather than score
// silently wrong.
func DefaultRuleset() (*Ruleset, error) {
	defaultOnce.Do(func() {
		data, err := rulesFS.ReadFile("rules/checks.yaml")
		if err != nil {
			defaultErr = fmt.Errorf("read embedded resume rules: %w", err)
			return
		}
		defaultSet, defaultErr = LoadRuleset(data)
	})
	return defaultSet, defaultErr
}

// MustDefaultRuleset is used by wiring code where a broken rule file must abort
// startup.
func MustDefaultRuleset() *Ruleset {
	rs, err := DefaultRuleset()
	if err != nil {
		panic(err)
	}
	return rs
}

// Summary returns a one-line description used in API responses.
func (rs *Ruleset) Summary() string {
	return fmt.Sprintf("rules v%s · %d checks · %d dimensions", strings.TrimSpace(rs.Version), len(rs.Checks), len(rs.Dimensions))
}
