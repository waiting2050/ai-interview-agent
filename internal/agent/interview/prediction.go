package interview

import (
	"context"
	"strings"

	"github.com/waiting2050/ai-interview-agent/internal/agent/prompt"
	"github.com/waiting2050/ai-interview-agent/internal/domain/domainpack"
	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
)

const (
	defaultPredictionCount = 5
	maxPredictionCount     = 10
)

// PredictionInput describes the pre-interview question forecast. It is
// stateless on purpose: predictions are suggestions, and only an explicit
// session start writes rows.
type PredictionInput struct {
	TargetRole string
	Difficulty string
	JDText     string
	Domain     string
	Profile    resume.Profile
	Count      int
}

// PredictedQuestion keeps the outline metadata next to the generated text so
// the client can show "why this question" without parsing the prompt.
type PredictedQuestion struct {
	Topic          string   `json:"topic"`
	Question       string   `json:"question"`
	Objective      string   `json:"objective"`
	ExpectedPoints []string `json:"expected_points,omitempty"`
	Degraded       bool     `json:"degraded"`
}

// PredictionResult reports which generator produced the batch. Generator is
// the model name, so "fake-heuristic" means no API key was configured rather
// than a silent fallback.
type PredictionResult struct {
	Domain      string
	Generator   string
	ModelCalled bool
	Degraded    bool
	Questions   []PredictedQuestion
}

// Predictor reuses the session question generator. Reusing it is the whole
// point: fallback, deduplication and length validation stay in one place, so a
// prediction can never take a different degradation path than a live question.
type Predictor struct {
	generator *QuestionGenerator
}

func NewPredictor(generator *QuestionGenerator) *Predictor {
	return &Predictor{generator: generator}
}

// Predict always returns usable questions: model failures degrade per question
// through QuestionGenerator. The only error is a cancelled context.
func (p *Predictor) Predict(ctx context.Context, in PredictionInput) (PredictionResult, error) {
	count := in.Count
	if count <= 0 {
		count = defaultPredictionCount
	}
	if count > maxPredictionCount {
		count = maxPredictionCount
	}

	pack := domainpack.Default().MustGet(in.Domain)
	result := PredictionResult{Generator: p.generator.ModelName()}
	if pack != nil {
		result.Domain = pack.ID
	}

	difficulty := in.Difficulty
	if difficulty == "" {
		difficulty = "mid"
	}

	items := p.outline(pack, in, difficulty, count)
	history := make([]string, 0, len(items))
	result.Questions = make([]PredictedQuestion, 0, len(items))
	for _, item := range items {
		out, err := p.generator.Generate(ctx, QuestionInput{
			Kind:           prompt.QuestionKindPrimary,
			TargetRole:     in.TargetRole,
			Difficulty:     difficulty,
			JDText:         in.JDText,
			Profile:        in.Profile,
			Topic:          item.Topic,
			Objective:      item.Objective,
			ExpectedPoints: item.ExpectedPoints,
			History:        history,
		})
		if err != nil {
			return PredictionResult{}, err
		}
		history = append(history, out.Text)
		result.ModelCalled = result.ModelCalled || out.Attempts > 0
		result.Degraded = result.Degraded || out.Degraded
		result.Questions = append(result.Questions, PredictedQuestion{
			Topic:          item.Topic,
			Question:       out.Text,
			Objective:      item.Objective,
			ExpectedPoints: item.ExpectedPoints,
			Degraded:       out.Degraded,
		})
	}
	return result, nil
}

// outline ranks domain-pack topics by the resume/JD keywords. Plan keeps its
// input order stable and moves above-level items to the back, so the same
// input always produces the same batch.
func (p *Predictor) outline(pack *domainpack.Pack, in PredictionInput, difficulty string, count int) []domainpack.PlanItem {
	text := strings.Join(nonEmpty(in.TargetRole, in.JDText, in.Profile.Text()), "\n")
	if pack != nil {
		if items := pack.Plan(text, difficulty, count); len(items) > 0 {
			return items
		}
	}
	return genericOutline(in.Profile, count)
}

// genericOutline is the last resort when no domain pack is available. It still
// derives topics from the resume instead of returning hardcoded questions.
func genericOutline(profile resume.Profile, count int) []domainpack.PlanItem {
	items := make([]domainpack.PlanItem, 0, count)
	for _, skill := range profile.Skills {
		items = append(items, domainpack.PlanItem{
			Topic:          skill,
			Objective:      "验证候选人对 " + skill + " 的使用深度与取舍",
			ExpectedPoints: []string{"原理与边界", "项目中的具体用法", "失败场景与验证方式"},
		})
		if len(items) == count {
			return items
		}
	}
	for _, project := range profile.Projects {
		if project.Name == "" {
			continue
		}
		items = append(items, domainpack.PlanItem{
			Topic:          project.Name,
			Objective:      "围绕项目职责边界与关键技术选型提问",
			ExpectedPoints: []string{"你负责的部分", "关键取舍", "可量化的结果"},
		})
		if len(items) == count {
			return items
		}
	}
	if len(items) == 0 {
		items = append(items, domainpack.PlanItem{
			Topic:          "项目经历",
			Objective:      "从简历中挑一个最能体现后端能力的项目展开",
			ExpectedPoints: []string{"背景与目标", "你的职责", "技术取舍与结果"},
		})
	}
	return items
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}
