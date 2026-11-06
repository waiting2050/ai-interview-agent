package report

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/agent/prompt"
	"github.com/waiting2050/ai-interview-agent/internal/agent/tools"
	domain "github.com/waiting2050/ai-interview-agent/internal/domain/report"
)

// Outcome is the generator result. Degraded means the report is complete but
// the commentary came from deterministic assembly instead of the model.
type Outcome struct {
	Report        domain.Report
	Degraded      bool
	ErrorCode     string
	RawOutput     string
	Attempts      int
	ModelName     string
	PromptVersion string
}

// Generator is the seam the app depends on; tests inject stubs.
type Generator interface {
	Generate(ctx context.Context, evidence tools.Bundle) (Outcome, error)
}

type Builder struct {
	client      llm.Client
	modelName   string
	maxAttempts int
	now         func() time.Time
}

func NewBuilder(client llm.Client, modelName string) *Builder {
	return &Builder{client: client, modelName: modelName, maxAttempts: 2, now: time.Now}
}

var _ Generator = (*Builder)(nil)

// Generate asks the model for commentary over a compact evidence bundle. Model
// transport or output failures degrade to the deterministic report; only a
// cancelled context returns an error so the run can be resumed.
func (b *Builder) Generate(ctx context.Context, ev tools.Bundle) (Outcome, error) {
	now := b.now().UTC()
	base := Fallback(ev, now)
	base.ModelName = b.modelName
	base.PromptVersion = prompt.ReportVersion

	out := Outcome{Report: base, ModelName: b.modelName, PromptVersion: prompt.ReportVersion}
	if ev.Overview.AnsweredTurns == 0 {
		out.Report.Degraded = true
		out.Report.ErrorCode = ErrorCodeNoAnswers
		out.Degraded = true
		out.ErrorCode = ErrorCodeNoAnswers
		out.Report.Sections = out.Report.RenderSections()
		return out, nil
	}

	user := prompt.ReportUser(buildPromptInput(ev))
	dims := make([]prompt.DimensionSpec, 0, len(ev.Overview.Dimensions))
	for _, d := range ev.Overview.Dimensions {
		dims = append(dims, prompt.DimensionSpec{Key: d.Dimension, Name: d.Name})
	}
	system := prompt.ReportSystem(dims...)
	var issues []string

	for attempt := 1; attempt <= b.maxAttempts; attempt++ {
		req := llm.Request{
			System:      system,
			User:        user,
			Temperature: 0.3,
			MaxTokens:   900,
		}
		if attempt > 1 {
			req.User = user + "\n\n" + prompt.ReportRepairMessage(issues)
		}

		resp, err := b.client.Generate(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				return Outcome{}, fmt.Errorf("generate report: %w", err)
			}
			out.Attempts = attempt
			out.Degraded = true
			out.ErrorCode = ErrorCodeModelUnavailable
			out.Report.Degraded = true
			out.Report.ErrorCode = ErrorCodeModelUnavailable
			out.Report.Sections = out.Report.RenderSections()
			return out, nil
		}

		out.Attempts = attempt
		out.RawOutput = truncateRunes(resp.Text, 2000)
		generated, parseErr := parseGenerated(resp.Text)
		if parseErr != nil {
			issues = []string{parseErr.Error()}
			continue
		}
		issues = validateGenerated(generated, ev.Overview.Turns)
		if len(issues) == 0 {
			out.Report = merge(base, generated, ev)
			out.Report.Sections = out.Report.RenderSections()
			return out, nil
		}
	}

	out.Degraded = true
	out.ErrorCode = ErrorCodeOutputInvalid
	out.Report.Degraded = true
	out.Report.ErrorCode = ErrorCodeOutputInvalid
	out.Report.Sections = out.Report.RenderSections()
	return out, nil
}

type generated struct {
	Verdict           string            `json:"verdict"`
	DimensionComments map[string]string `json:"dimension_comments"`
	Questions         []struct {
		TurnID        string   `json:"turn_id"`
		Comment       string   `json:"comment"`
		Strengths     []string `json:"strengths"`
		MissingPoints []string `json:"missing_points"`
	} `json:"questions"`
	Improvements []string `json:"improvements"`
	NextSteps    []string `json:"next_steps"`
}

func parseGenerated(raw string) (*generated, error) {
	text := strings.TrimSpace(raw)
	if start, end := strings.Index(text, "{"), strings.LastIndex(text, "}"); start >= 0 && end > start {
		text = text[start : end+1]
	}
	var out generated
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, fmt.Errorf("输出不是合法 JSON：%v", err)
	}
	return &out, nil
}

func validateGenerated(in *generated, turns []tools.TurnBrief) []string {
	var issues []string
	verdict := strings.TrimSpace(in.Verdict)
	if n := len([]rune(verdict)); n < 20 || n > 800 {
		issues = append(issues, fmt.Sprintf("verdict 长度应在 20-800 字，当前 %d", n))
	}
	if len(in.DimensionComments) == 0 {
		issues = append(issues, "dimension_comments 不能为空")
	}
	known := map[string]bool{}
	for _, turn := range turns {
		known[turn.TurnID] = true
	}
	for _, q := range in.Questions {
		if !known[q.TurnID] {
			issues = append(issues, fmt.Sprintf("questions 中出现未知 turn_id %q", q.TurnID))
			continue
		}
		if n := len([]rune(strings.TrimSpace(q.Comment))); n < 5 || n > 300 {
			issues = append(issues, fmt.Sprintf("turn %s 的 comment 长度应在 5-300 字，当前 %d", q.TurnID, n))
		}
	}
	if len(in.Improvements) == 0 || len(in.Improvements) > 5 {
		issues = append(issues, fmt.Sprintf("improvements 应有 1-5 条，当前 %d", len(in.Improvements)))
	}
	for i, point := range in.Improvements {
		if n := len([]rune(strings.TrimSpace(point))); n < 5 || n > 160 {
			issues = append(issues, fmt.Sprintf("improvements[%d] 长度应在 5-160 字，当前 %d", i, n))
		}
	}
	if len(in.NextSteps) == 0 || len(in.NextSteps) > 6 {
		issues = append(issues, fmt.Sprintf("next_steps 应有 1-6 条，当前 %d", len(in.NextSteps)))
	}
	return issues
}

func merge(base domain.Report, in *generated, ev tools.Bundle) domain.Report {
	out := base
	out.Degraded = false
	out.ErrorCode = ""
	out.Verdict = strings.TrimSpace(in.Verdict)

	for i := range out.Dimensions {
		if comment, ok := in.DimensionComments[out.Dimensions[i].Dimension]; ok {
			if comment = strings.TrimSpace(comment); len([]rune(comment)) >= 5 {
				out.Dimensions[i].Comment = comment
			}
		}
	}

	byTurn := map[string]int{}
	for i, q := range out.Questions {
		byTurn[q.TurnID] = i
	}
	for _, q := range in.Questions {
		index, ok := byTurn[q.TurnID]
		if !ok {
			continue
		}
		if comment := strings.TrimSpace(q.Comment); comment != "" {
			out.Questions[index].Comment = comment
		}
		if strengths := cleanPoints(q.Strengths, 2); len(strengths) > 0 {
			out.Questions[index].StrongPoints = strengths
		}
		if missing := cleanPoints(q.MissingPoints, 2); len(missing) > 0 {
			out.Questions[index].MissingPoints = missing
		}
	}

	out.Improvements = cleanPoints(in.Improvements, 3)
	for len(out.Improvements) < 3 {
		out.Improvements = append(out.Improvements, base.Improvements[len(out.Improvements)])
	}
	out.NextSteps = cleanPoints(in.NextSteps, 5)
	if len(out.NextSteps) < 2 {
		out.NextSteps = append(out.NextSteps, base.NextSteps...)
		if len(out.NextSteps) > 5 {
			out.NextSteps = out.NextSteps[:5]
		}
	}

	out.ToolCalls = ev.Calls
	out.ToolBreakdown = ev.ToolBreakdown
	out.ModelName = base.ModelName
	out.PromptVersion = base.PromptVersion
	return out
}

func cleanPoints(in []string, limit int) []string {
	var out []string
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value == "" || len([]rune(value)) > 200 {
			continue
		}
		out = append(out, value)
		if len(out) == limit {
			break
		}
	}
	return out
}

func buildPromptInput(ev tools.Bundle) prompt.ReportInput {
	overview := ev.Overview
	in := prompt.ReportInput{
		TargetRole:    overview.TargetRole,
		Difficulty:    overview.Difficulty,
		CandidateName: overview.CandidateName,
		AnsweredTurns: overview.AnsweredTurns,
		OverallScore:  overview.OverallScore,
	}
	for _, dim := range overview.Dimensions {
		label := dim.Name
		if label == "" {
			label = dimensionLabel(dim.Dimension)
		}
		in.Dimensions = append(in.Dimensions, fmt.Sprintf("%s（%s）：%.1f", dim.Dimension, label, dim.Score))
	}
	for _, turn := range ev.Turns {
		in.Turns = append(in.Turns, prompt.ReportTurn{
			TurnID:        turn.TurnID,
			Seq:           turn.Seq,
			Topic:         turn.Topic,
			Kind:          turn.Kind,
			Question:      turn.Question,
			AnswerExcerpt: turn.AnswerExcerpt,
			Score:         turn.Score,
			MissingPoints: turn.MissingList,
		})
	}
	for _, turn := range ev.WeakTurns {
		in.WeakTurns = append(in.WeakTurns, prompt.ReportTurn{
			TurnID:        turn.TurnID,
			Seq:           turn.Seq,
			Topic:         turn.Topic,
			Kind:          turn.Kind,
			Question:      turn.Question,
			AnswerExcerpt: turn.AnswerExcerpt,
			Score:         turn.Score,
			MissingPoints: turn.MissingList,
		})
	}
	return in
}

func truncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}
