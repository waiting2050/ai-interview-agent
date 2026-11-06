// Package domainpack loads interview domain packs. A pack owns its outline,
// rubric weights and lexicon; the interview engine stays domain-agnostic.
package domainpack

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"github.com/waiting2050/ai-interview-agent/domainpacks"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	"gopkg.in/yaml.v3"
)

// Dimension is one scored axis of a pack rubric.
type Dimension struct {
	Key    string  `yaml:"key" json:"key"`
	Name   string  `yaml:"name" json:"name"`
	Weight float64 `yaml:"weight" json:"weight"`
	Anchor string  `yaml:"anchor,omitempty" json:"anchor,omitempty"`
}

type Direction struct {
	ID       string   `yaml:"id" json:"id"`
	Label    string   `yaml:"label" json:"label"`
	Keywords []string `yaml:"keywords" json:"keywords"`
}

type Topic struct {
	ID             string   `yaml:"id" json:"id"`
	Direction      string   `yaml:"direction" json:"direction"`
	Topic          string   `yaml:"topic" json:"topic"`
	Objective      string   `yaml:"objective" json:"objective"`
	Difficulty     string   `yaml:"difficulty" json:"difficulty"`
	ExpectedPoints []string `yaml:"expected_points" json:"expected_points"`
	Source         string   `yaml:"source" json:"source"`
	URL            string   `yaml:"url,omitempty" json:"url,omitempty"`
}

type Lexicon struct {
	Skills   []string `yaml:"skills" json:"skills"`
	Tools    []string `yaml:"tools" json:"tools"`
	Keywords []string `yaml:"keywords" json:"keywords"`
}

type Rubric struct {
	Dimensions []Dimension `yaml:"dimensions" json:"dimensions"`
}

// Pack is a validated domain pack loaded from disk.
type Pack struct {
	ID         string      `yaml:"id" json:"id"`
	Name       string      `yaml:"name" json:"name"`
	Version    string      `yaml:"version" json:"version"`
	Rubric     Rubric      `yaml:"rubric" json:"rubric"`
	Directions []Direction `yaml:"directions" json:"directions"`
	Topics     []Topic     `yaml:"topics" json:"topics"`
	Lexicon    Lexicon     `yaml:"lexicon" json:"lexicon"`
}

// PlanItem is the outline shape consumed by the interview engine.
type PlanItem struct {
	ID             string
	Direction      string
	Topic          string
	Objective      string
	Difficulty     string
	ExpectedPoints []string
}

// Registry holds every pack found at load time.
type Registry struct {
	packs map[string]*Pack
	list  []*Pack
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
	defaultErr  error
)

// Default returns the registry embedded in the binary. It panics on invalid
// built-in data; invalid packs are caught by tests before shipping.
func Default() *Registry {
	defaultOnce.Do(func() {
		defaultReg, defaultErr = LoadFS(domainpacks.FS)
	})
	if defaultErr != nil {
		panic("domain packs: " + defaultErr.Error())
	}
	return defaultReg
}

// LoadFS scans the root for "<pack-id>/manifest.yaml" directories. Files that
// do not look like a pack are skipped; every found manifest is validated and
// one invalid pack rejects the whole load.
func LoadFS(fsys fs.FS) (*Registry, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read domain pack root: %w", err)
	}
	reg := &Registry{packs: map[string]*Pack{}}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := name + "/manifest.yaml"
		data, err := fs.ReadFile(fsys, path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		pack, err := parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if pack.ID != name {
			return nil, fmt.Errorf("%s: id %q must match directory %q", path, pack.ID, name)
		}
		if _, dup := reg.packs[pack.ID]; dup {
			return nil, fmt.Errorf("%s: duplicate pack id %q", path, pack.ID)
		}
		reg.packs[pack.ID] = pack
		reg.list = append(reg.list, pack)
	}
	if len(reg.list) == 0 {
		return nil, errors.New("no domain packs found")
	}
	sort.Slice(reg.list, func(i, j int) bool { return reg.list[i].ID < reg.list[j].ID })
	return reg, nil
}

func parse(data []byte) (*Pack, error) {
	var pack Pack
	if err := yaml.Unmarshal(data, &pack); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := pack.Validate(); err != nil {
		return nil, err
	}
	return &pack, nil
}

// Get returns a pack by id.
func (r *Registry) Get(id string) (*Pack, bool) {
	pack, ok := r.packs[strings.TrimSpace(id)]
	return pack, ok
}

// MustGet returns the pack or the Go backend fallback. Callers validate user
// input before this point; the fallback keeps old sessions readable.
func (r *Registry) MustGet(id string) *Pack {
	if pack, ok := r.Get(id); ok {
		return pack
	}
	if pack, ok := r.Get(LegacyGoBackendID); ok {
		return pack
	}
	if len(r.list) > 0 {
		return r.list[0]
	}
	return nil
}

// List returns packs sorted by id.
func (r *Registry) List() []*Pack {
	return append([]*Pack(nil), r.list...)
}

// LegacyGoBackendID is the pack used when a session predates the domain field.
const LegacyGoBackendID = "go-backend"

var validDifficulties = map[string]struct{}{"junior": {}, "mid": {}, "senior": {}}
var validSources = map[string]struct{}{"curated": {}, "retrieved": {}}

// Validate rejects packs that would make scoring or planning ambiguous.
func (p *Pack) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("id is required")
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name is required")
	}
	if err := p.Rubric.Validate(); err != nil {
		return err
	}
	if len(p.Directions) == 0 {
		return errors.New("at least one direction is required")
	}
	directions := map[string]Direction{}
	for i, d := range p.Directions {
		id := strings.TrimSpace(d.ID)
		if id == "" {
			return fmt.Errorf("directions[%d]: id is required", i)
		}
		if _, dup := directions[id]; dup {
			return fmt.Errorf("duplicate direction %q", id)
		}
		if len(d.Keywords) == 0 {
			return fmt.Errorf("direction %q: at least one keyword is required", id)
		}
		directions[id] = d
	}
	if len(p.Topics) < 1 {
		return errors.New("at least one topic is required")
	}
	seen := map[string]struct{}{}
	perDirection := map[string]int{}
	for i, t := range p.Topics {
		id := strings.TrimSpace(t.ID)
		if id == "" {
			return fmt.Errorf("topics[%d]: id is required", i)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("duplicate topic id %q", id)
		}
		seen[id] = struct{}{}
		if _, ok := directions[t.Direction]; !ok {
			return fmt.Errorf("topic %q: unknown direction %q", id, t.Direction)
		}
		if strings.TrimSpace(t.Topic) == "" {
			return fmt.Errorf("topic %q: topic is required", id)
		}
		if strings.TrimSpace(t.Objective) == "" {
			return fmt.Errorf("topic %q: objective is required", id)
		}
		if _, ok := validDifficulties[strings.ToLower(strings.TrimSpace(t.Difficulty))]; !ok {
			return fmt.Errorf("topic %q: difficulty must be junior/mid/senior, got %q", id, t.Difficulty)
		}
		if len(t.ExpectedPoints) < 3 {
			return fmt.Errorf("topic %q: expected_points must list 3 points, got %d", id, len(t.ExpectedPoints))
		}
		if _, ok := validSources[t.Source]; !ok {
			return fmt.Errorf("topic %q: source must be curated or retrieved, got %q", id, t.Source)
		}
		if t.Source == "curated" && strings.TrimSpace(t.URL) != "" {
			return fmt.Errorf("topic %q: curated topics must not carry a URL", id)
		}
		if t.Source == "retrieved" && strings.TrimSpace(t.URL) == "" {
			return fmt.Errorf("topic %q: retrieved topics must carry the source URL", id)
		}
		perDirection[t.Direction]++
	}
	for id := range directions {
		if perDirection[id] == 0 {
			return fmt.Errorf("direction %q has no topics", id)
		}
	}
	if len(p.Lexicon.Skills)+len(p.Lexicon.Tools)+len(p.Lexicon.Keywords) == 0 {
		return errors.New("lexicon must list at least one skill, tool or keyword")
	}
	return nil
}

// Validate checks the rubric is usable as a weight table.
func (r Rubric) Validate() error {
	if len(r.Dimensions) == 0 {
		return errors.New("rubric.dimensions is required")
	}
	seen := map[string]struct{}{}
	sum := 0.0
	for i, d := range r.Dimensions {
		key := strings.TrimSpace(d.Key)
		if key == "" {
			return fmt.Errorf("rubric.dimensions[%d]: key is required", i)
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("rubric: duplicate dimension %q", key)
		}
		seen[key] = struct{}{}
		if strings.TrimSpace(d.Name) == "" {
			return fmt.Errorf("rubric dimension %q: name is required", key)
		}
		if d.Weight <= 0 || d.Weight > 1 {
			return fmt.Errorf("rubric dimension %q: weight must be in (0,1], got %v", key, d.Weight)
		}
		sum += d.Weight
	}
	if diff := sum - 1; diff > 0.001 || diff < -0.001 {
		return fmt.Errorf("rubric weights must sum to 1, got %.3f", sum)
	}
	return nil
}

// EvaluationRubric converts the pack rubric into the evaluation engine shape.
// Packs are validated at load time, so a conversion error here is a bug and
// falls back to the default rubric instead of breaking a session.
func (p *Pack) EvaluationRubric() evaluation.Rubric {
	if p == nil {
		return evaluation.DefaultRubric()
	}
	defs := make([]evaluation.DimensionDef, 0, len(p.Rubric.Dimensions))
	for _, d := range p.Rubric.Dimensions {
		defs = append(defs, evaluation.DimensionDef{
			Key:    d.Key,
			Name:   d.Name,
			Weight: d.Weight,
			Anchor: d.Anchor,
		})
	}
	rubric, err := evaluation.NewRubric(defs)
	if err != nil {
		return evaluation.DefaultRubric()
	}
	return rubric
}

// Detect returns every direction whose keywords appear in text, in manifest
// order. An empty result means the caller falls back to the first direction.
func (p *Pack) Detect(text string) []string {
	lower := strings.ToLower(text)
	var out []string
	for _, direction := range p.Directions {
		for _, keyword := range direction.Keywords {
			if strings.Contains(lower, strings.ToLower(keyword)) {
				out = append(out, direction.ID)
				break
			}
		}
	}
	return out
}

// Plan merges matched directions into one ordered outline. Items above the
// candidate difficulty move to the back so a junior session stays approachable
// while still filling the outline. Duplicate topics are removed.
func (p *Pack) Plan(text, difficulty string, limit int) []PlanItem {
	if limit <= 0 {
		limit = 8
	}
	matched := p.Detect(text)
	if len(matched) == 0 && len(p.Directions) > 0 {
		matched = []string{p.Directions[0].ID}
	}

	rank := difficultyRank(difficulty)
	var primary, secondary []PlanItem
	seen := map[string]struct{}{}
	for _, direction := range matched {
		for _, t := range p.Topics {
			if t.Direction != direction {
				continue
			}
			if _, dup := seen[t.Topic]; dup {
				continue
			}
			seen[t.Topic] = struct{}{}
			item := PlanItem{
				ID:             t.ID,
				Direction:      t.Direction,
				Topic:          t.Topic,
				Objective:      t.Objective,
				Difficulty:     t.Difficulty,
				ExpectedPoints: append([]string(nil), t.ExpectedPoints...),
			}
			if difficultyRank(t.Difficulty) <= rank {
				primary = append(primary, item)
			} else {
				secondary = append(secondary, item)
			}
		}
	}
	items := append(primary, secondary...)
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

func difficultyRank(difficulty string) int {
	switch strings.ToLower(strings.TrimSpace(difficulty)) {
	case "junior":
		return 1
	case "senior":
		return 3
	default:
		return 2
	}
}
