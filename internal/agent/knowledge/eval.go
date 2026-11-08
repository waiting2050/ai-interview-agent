package knowledge

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Expectation labels one acceptable target without pinning a chunk id: a
// topic id, a source path plus section prefix, or a title fragment.
type Expectation struct {
	TopicID       string `json:"topic_id,omitempty"`
	Domain        string `json:"domain,omitempty"`
	Title         string `json:"title,omitempty"`
	SourcePath    string `json:"source_path,omitempty"`
	SectionPrefix string `json:"section_prefix,omitempty"`
}

// QuerySpec is one labelled retrieval query. Group separates the two halves of
// the quality set: A keeps the document's own wording, B is a rewrite that
// would only be matched by semantic or cross-section retrieval.
type QuerySpec struct {
	ID         string        `json:"id"`
	Group      string        `json:"group"`
	Category   string        `json:"category"`
	Rewrite    string        `json:"rewrite,omitempty"`
	Query      string        `json:"query"`
	Answerable bool          `json:"answerable"`
	Expected   []Expectation `json:"expected"`
}

// QuerySet is the versioned file committed under testdata.
type QuerySet struct {
	Version string      `json:"version"`
	Note    string      `json:"note"`
	Queries []QuerySpec `json:"queries"`
}

// LoadQuerySet reads and validates the evaluation file.
func LoadQuerySet(path string) (*QuerySet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read query set: %w", err)
	}
	var set QuerySet
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("decode query set: %w", err)
	}
	if err := validateSet(&set); err != nil {
		return nil, err
	}
	return &set, nil
}

// validateSet enforces the data contract: 50 answerable queries split evenly
// into original-wording and rewritten groups, plus four unanswerable probes.
func validateSet(set *QuerySet) error {
	if len(set.Queries) < 20 || len(set.Queries) > 60 {
		return fmt.Errorf("query set must have 20-60 queries, got %d", len(set.Queries))
	}
	seen := map[string]bool{}
	counts := map[string]int{}
	for _, q := range set.Queries {
		if q.ID == "" || q.Query == "" {
			return fmt.Errorf("query id and text must not be empty")
		}
		if seen[q.ID] {
			return fmt.Errorf("duplicate query id %s", q.ID)
		}
		seen[q.ID] = true
		if q.Answerable && len(q.Expected) == 0 {
			return fmt.Errorf("query %s is answerable but has no expectation", q.ID)
		}
		switch q.Group {
		case "A", "B", "no_answer":
		case "":
			return fmt.Errorf("query %s has no group", q.ID)
		default:
			return fmt.Errorf("query %s has unknown group %q", q.ID, q.Group)
		}
		if q.Group != "no_answer" && !q.Answerable {
			return fmt.Errorf("query %s is unanswerable but grouped as %s", q.ID, q.Group)
		}
		if q.Group == "no_answer" && q.Answerable {
			return fmt.Errorf("query %s is answerable but grouped as no_answer", q.ID)
		}
		if q.Group == "B" && q.Rewrite == "" {
			return fmt.Errorf("B query %s must note its rewrite intent", q.ID)
		}
		counts[q.Group]++
	}
	// The acceptance comparison is 25 original-wording + 25 rewritten + 4
	// unanswerable. Enforcing the split prevents silently shrinking group B.
	if counts["A"] != 25 || counts["B"] != 25 || counts["no_answer"] != 4 {
		return fmt.Errorf("query groups must be A=25 B=25 no_answer=4, got A=%d B=%d no_answer=%d",
			counts["A"], counts["B"], counts["no_answer"])
	}
	return nil
}

// Outcome is one query result per arm.
type Outcome struct {
	ID         string `json:"id"`
	Group      string `json:"group"`
	Category   string `json:"category"`
	Query      string `json:"query"`
	Answerable bool   `json:"answerable"`
	Mode       string `json:"mode"`
	Rank       int    `json:"rank"`
	Hit        bool   `json:"hit"`
	LatencyMS  int64  `json:"latency_ms"`
	// Top1Score/SecondScore/Gap carry the arm's own ranking score (BM25 score
	// for the lexical arm, cosine for the hybrid arm) so a refusal threshold
	// can be calibrated from data instead of guessed.
	Top1Score float64 `json:"top1_score"`
	Top2Score float64 `json:"top2_score"`
	Gap       float64 `json:"top1_top2_gap"`
	Top       []Hit   `json:"top"`
}

// CategoryScore is the hit count for one query category.
type CategoryScore struct {
	Total int     `json:"total"`
	Hits  int     `json:"hits"`
	Rate  float64 `json:"rate"`
}

// ArmMetrics are the numbers the M20 acceptance gate reads.
type ArmMetrics struct {
	Answerable        int                      `json:"answerable"`
	Hits              int                      `json:"hits"`
	HitAt5            float64                  `json:"hit_at_5"`
	MRR               float64                  `json:"mrr"`
	NoAnswer          int                      `json:"no_answer_queries"`
	FalsePositives    int                      `json:"false_positives_at_5"`
	FalsePositiveRate float64                  `json:"false_positive_rate_at_5"`
	P50MS             int64                    `json:"p50_ms"`
	P95MS             int64                    `json:"p95_ms"`
	ByCategory        map[string]CategoryScore `json:"by_category"`
}

// ScoreArm computes hit@5, MRR and the unanswerable false-positive rate. A
// no-answer query counts as a false positive when any hit is returned.
func ScoreArm(specs []QuerySpec, outcomes []Outcome, topK int) ArmMetrics {
	if topK <= 0 {
		topK = 5
	}
	m := ArmMetrics{ByCategory: map[string]CategoryScore{}}
	var rrSum float64
	var latencies []int64

	for _, spec := range specs {
		outcome, ok := findOutcome(outcomes, spec.ID)
		if !ok {
			continue
		}
		cat := m.ByCategory[spec.Category]
		cat.Total++
		latencies = append(latencies, outcome.LatencyMS)

		if !spec.Answerable {
			m.NoAnswer++
			if len(outcome.Top) > 0 {
				m.FalsePositives++
			}
			m.ByCategory[spec.Category] = cat
			continue
		}
		m.Answerable++
		rank := RankOf(spec, outcome, topK)
		if rank > 0 {
			m.Hits++
			cat.Hits++
			rrSum += 1.0 / float64(rank)
		}
		m.ByCategory[spec.Category] = cat
	}

	if m.Answerable > 0 {
		m.HitAt5 = round4(float64(m.Hits) / float64(m.Answerable))
		m.MRR = round4(rrSum / float64(m.Answerable))
	}
	if m.NoAnswer > 0 {
		m.FalsePositiveRate = round4(float64(m.FalsePositives) / float64(m.NoAnswer))
	}
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		m.P50MS = percentile(latencies, 0.50)
		m.P95MS = percentile(latencies, 0.95)
	}

	// Answerable categories get a rate; the no_answer bucket is scored by the
	// false-positive metric instead.
	for key, cat := range m.ByCategory {
		if cat.Total > 0 {
			cat.Rate = round4(float64(cat.Hits) / float64(cat.Total))
			m.ByCategory[key] = cat
		}
	}
	return m
}

// Calibration is the data-driven answer to "where should refusal cut off?".
// It scans every observed top-1 score and picks the threshold that refuses the
// most no-answer queries while refusing as few answerable ones as possible.
type Calibration struct {
	Threshold        float64 `json:"threshold"`
	NoAnswer         int     `json:"no_answer_queries"`
	RefusedNoAnswer  int     `json:"no_answer_refused"`
	RefusalRate      float64 `json:"no_answer_refusal_rate"`
	Answerable       int     `json:"answerable_queries"`
	FalseRefused     int     `json:"answerable_false_refused"`
	FalseRefusalRate float64 `json:"answerable_false_refusal_rate"`
	Overlap          bool    `json:"classes_overlap"`
}

// Calibrate computes the best single threshold from the observed scores. A
// query is refused when its top-1 score is at or below the threshold. When the
// classes overlap, the result reports the least harmful tradeoff instead of
// pretending a clean cut exists.
func Calibrate(outcomes []Outcome) Calibration {
	var answerable, noAnswer []float64
	for _, o := range outcomes {
		if len(o.Top) == 0 {
			continue
		}
		if o.Answerable {
			answerable = append(answerable, o.Top1Score)
		} else {
			noAnswer = append(noAnswer, o.Top1Score)
		}
	}
	cal := Calibration{NoAnswer: len(noAnswer), Answerable: len(answerable)}
	if len(answerable) == 0 && len(noAnswer) == 0 {
		return cal
	}

	candidates := append(append([]float64{}, answerable...), noAnswer...)
	sort.Float64s(candidates)
	bestScore := -1 << 30
	for _, threshold := range candidates {
		refused, falseRefused := 0, 0
		for _, score := range noAnswer {
			if score <= threshold {
				refused++
			}
		}
		for _, score := range answerable {
			if score <= threshold {
				falseRefused++
			}
		}
		objective := refused*len(answerable) - falseRefused*len(noAnswer)
		if objective > bestScore || (objective == bestScore && refused > cal.RefusedNoAnswer) {
			bestScore = objective
			cal.Threshold = threshold
			cal.RefusedNoAnswer = refused
			cal.FalseRefused = falseRefused
		}
	}
	if cal.NoAnswer > 0 {
		cal.RefusalRate = round4(float64(cal.RefusedNoAnswer) / float64(cal.NoAnswer))
	}
	if cal.Answerable > 0 {
		cal.FalseRefusalRate = round4(float64(cal.FalseRefused) / float64(cal.Answerable))
	}
	// Overlap means some unanswerable query scored at least as high as the
	// lowest-scoring answerable one: no single threshold separates the classes.
	if len(answerable) > 0 && len(noAnswer) > 0 {
		sort.Float64s(answerable)
		sort.Float64s(noAnswer)
		cal.Overlap = noAnswer[len(noAnswer)-1] >= answerable[0]
	}
	return cal
}

// GroupSpecs returns the answerable specifications of one group (A or B).
func GroupSpecs(specs []QuerySpec, group string) []QuerySpec {
	out := make([]QuerySpec, 0, len(specs))
	for _, spec := range specs {
		if spec.Group == group {
			out = append(out, spec)
		}
	}
	return out
}

// NoAnswerSpecs returns the unanswerable specifications.
func NoAnswerSpecs(specs []QuerySpec) []QuerySpec {
	out := make([]QuerySpec, 0, len(specs))
	for _, spec := range specs {
		if !spec.Answerable {
			out = append(out, spec)
		}
	}
	return out
}

// RankOf returns the 1-based position of the first hit that satisfies the
// spec labels within topK, or 0 when the query misses.
func RankOf(spec QuerySpec, outcome Outcome, topK int) int {
	if topK <= 0 {
		topK = 5
	}
	for i, hit := range outcome.Top {
		if i >= topK {
			break
		}
		if matches(hit, spec.Expected) {
			return i + 1
		}
	}
	return 0
}

func findOutcome(outcomes []Outcome, id string) (Outcome, bool) {
	for _, outcome := range outcomes {
		if outcome.ID == id {
			return outcome, true
		}
	}
	return Outcome{}, false
}

func matches(hit Hit, expected []Expectation) bool {
	for _, e := range expected {
		chunk := Chunk{
			SourcePath: hit.SourcePath, SectionKey: hit.SectionKey, SourceKind: hit.SourceKind,
			Domain: hit.Domain, TopicID: hit.TopicID, Title: hit.Title,
		}
		if MatchesExpectation(chunk, e) {
			return true
		}
	}
	return false
}

// MatchesExpectation reports whether one chunk satisfies one label. An empty
// label matches nothing, so a missing expectation can never inflate a score.
func MatchesExpectation(chunk Chunk, e Expectation) bool {
	if e.TopicID != "" {
		return chunk.SourceKind == "domain_topic" && chunk.TopicID == e.TopicID
	}
	if e.SourcePath != "" && chunk.SourcePath != e.SourcePath {
		return false
	}
	if e.SectionPrefix != "" && !strings.HasPrefix(chunk.SectionKey, e.SectionPrefix) {
		return false
	}
	if e.Title != "" && !strings.Contains(chunk.Title, e.Title) {
		return false
	}
	if e.Domain != "" && chunk.Domain != e.Domain {
		return false
	}
	return e.SourcePath != "" || e.SectionPrefix != "" || e.Title != "" || e.Domain != ""
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(p * float64(len(sorted)-1))
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func round4(v float64) float64 {
	return float64(int(v*10000+0.5)) / 10000
}
