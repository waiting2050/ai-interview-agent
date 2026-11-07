package report

import (
	"context"
	"encoding/json"
	"errors"
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
	// ValidationIssues lists every field-level problem found in the last
	// attempt. Fatal issues cause degradation; warnings only fall back to
	// deterministic values for the affected field.
	ValidationIssues []string
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
	var lastIssues []string

	for attempt := 1; attempt <= b.maxAttempts; attempt++ {
		req := llm.Request{
			System:      system,
			User:        user,
			Temperature: 0.3,
			MaxTokens:   1800,
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
			out.Report.ErrorDetail = "model_unavailable: " + truncateRunes(err.Error(), 300)
			out.Report.Sections = out.Report.RenderSections()
			return out, nil
		}

		out.Attempts = attempt
		out.RawOutput = truncateRunes(resp.Text, 2000)
		generated, parseErr := parseGenerated(resp.Text)
		if parseErr != nil {
			lastIssues = []string{parseErr.Error()}
			issues = lastIssues
			continue
		}
		fatal, warnings := validateGenerated(generated, ev.Overview.Turns)
		lastIssues = append(issueStrings(fatal), issueStrings(warnings)...)
		if len(fatal) == 0 {
			out.ValidationIssues = lastIssues
			out.Report = merge(base, generated, ev)
			out.Report.Sections = out.Report.RenderSections()
			return out, nil
		}
		issues = issueStrings(fatal)
	}

	out.Degraded = true
	out.ErrorCode = ErrorCodeOutputInvalid
	out.Report.Degraded = true
	out.Report.ErrorCode = ErrorCodeOutputInvalid
	if len(lastIssues) > 0 {
		out.Report.ErrorDetail = truncateRunes(strings.Join(lastIssues, "; "), 600)
	}
	out.ValidationIssues = lastIssues
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

// fieldIssue names the exact JSON path and expected type so a degraded report
// can be diagnosed from logs without replaying the model call.
type fieldIssue struct {
	Path     string
	Expected string
	Detail   string
}

func (i fieldIssue) String() string {
	if i.Expected == "" {
		return fmt.Sprintf("%s: %s", i.Path, i.Detail)
	}
	return fmt.Sprintf("%s (期望 %s): %s", i.Path, i.Expected, i.Detail)
}

func issueStrings(in []fieldIssue) []string {
	out := make([]string, 0, len(in))
	for _, issue := range in {
		out = append(out, issue.String())
	}
	return out
}

func parseGenerated(raw string) (*generated, error) {
	text, err := extractJSONObject(raw)
	if err != nil {
		return nil, err
	}
	var out generated
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			path := "$"
			if typeErr.Field != "" {
				path = "$." + typeErr.Field
			}
			if typeErr.Value == "object" || typeErr.Value == "array" {
				return nil, fmt.Errorf("%s (期望 %s): 实际类型是 %s", path, typeErr.Type, typeErr.Value)
			}
			return nil, fmt.Errorf("%s (期望 %s): 类型不匹配", path, typeErr.Type)
		}
		return nil, fmt.Errorf("$ (期望 JSON 对象): %v", err)
	}
	return &out, nil
}

// extractJSONObject tolerates the decorations models still emit: markdown
// fences, prose before/after the object, and full-width structural punctuation.
func extractJSONObject(raw string) (string, error) {
	text := strings.TrimSpace(strings.TrimPrefix(raw, "\uFEFF"))
	if start := strings.Index(text, "```"); start >= 0 {
		rest := text[start+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if end := strings.LastIndex(rest, "```"); end >= 0 {
			rest = rest[:end]
		}
		text = strings.TrimSpace(rest)
	}
	text = normalizeJSONPunct(text)
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return "", fmt.Errorf("$ (期望 JSON 对象): 输出中没有找到 {")
	}
	end, ok := matchingBrace(text, start)
	if !ok {
		return "", fmt.Errorf("$ (期望 JSON 对象): 花括号不匹配，输出可能在对象中途被截断")
	}
	return text[start : end+1], nil
}

// matchingBrace finds the closing brace for the object starting at start while
// ignoring braces inside string literals.
func matchingBrace(text string, start int) (int, bool) {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// normalizeJSONPunct converts full-width structural punctuation to ASCII. Only
// characters outside ASCII string literals are rewritten, so Chinese prose
// inside values keeps its original punctuation.
func normalizeJSONPunct(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	inString := false
	for _, r := range text {
		if !inString {
			switch r {
			case '“':
				b.WriteByte('"')
				inString = true
				continue
			case '”':
				b.WriteByte('"')
				continue
			case '：':
				b.WriteByte(':')
				continue
			case '，':
				b.WriteByte(',')
				continue
			case '；':
				b.WriteByte(';')
				continue
			}
			if r == '"' {
				inString = true
			}
			b.WriteRune(r)
			continue
		}
		if r == '"' || r == '”' {
			inString = false
		}
		if r == '”' {
			b.WriteByte('"')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// validateGenerated splits problems into fatal and warning classes. Missing or
// short commentary fields are warnings: merge keeps the deterministic fallback
// for that field instead of degrading the whole report. Only structural errors
// such as an unknown turn_id remain fatal.
func validateGenerated(in *generated, turns []tools.TurnBrief) ([]fieldIssue, []fieldIssue) {
	var fatal, warnings []fieldIssue
	verdict := strings.TrimSpace(in.Verdict)
	switch {
	case verdict == "":
		warnings = append(warnings, fieldIssue{"verdict", "string, 120-260 字", "缺失，保留确定性总评"})
	case len([]rune(verdict)) < 20 || len([]rune(verdict)) > 800:
		warnings = append(warnings, fieldIssue{"verdict", "string, 20-800 字",
			fmt.Sprintf("长度 %d，按原值截断或保留确定性总评", len([]rune(verdict)))})
	}
	if len(in.DimensionComments) == 0 {
		warnings = append(warnings, fieldIssue{"dimension_comments", "object<string,string>", "缺失，保留确定性维度点评"})
	}

	known := map[string]bool{}
	for _, turn := range turns {
		known[turn.TurnID] = true
	}
	for i, q := range in.Questions {
		if !known[q.TurnID] {
			fatal = append(fatal, fieldIssue{
				fmt.Sprintf("questions[%d].turn_id", i),
				"string, 必须来自 turn_summaries",
				fmt.Sprintf("未知取值 %q", q.TurnID),
			})
			continue
		}
		comment := strings.TrimSpace(q.Comment)
		if comment == "" {
			warnings = append(warnings, fieldIssue{
				fmt.Sprintf("questions[%d].comment", i),
				"string, 20-80 字",
				"缺失，保留确定性逐题点评",
			})
			continue
		}
		if n := len([]rune(comment)); n < 5 || n > 300 {
			warnings = append(warnings, fieldIssue{
				fmt.Sprintf("questions[%d].comment", i),
				"string, 5-300 字",
				fmt.Sprintf("长度 %d", n),
			})
		}
	}
	switch n := len(in.Improvements); {
	case n == 0:
		warnings = append(warnings, fieldIssue{"improvements", "string[], 1-5 条", "缺失，保留确定性改进项"})
	case n > 5:
		warnings = append(warnings, fieldIssue{"improvements", "string[], 1-5 条", fmt.Sprintf("条数 %d，仅取前 3 条", n)})
	}
	if len(in.NextSteps) == 0 {
		warnings = append(warnings, fieldIssue{"next_steps", "string[], 2-6 条", "缺失，保留确定性练习建议"})
	}

	if verdict == "" && len(in.DimensionComments) == 0 && len(in.Questions) == 0 {
		fatal = append(fatal, fieldIssue{"$", "JSON 对象", "所有点评字段为空，视为无有效输出"})
	}
	return fatal, warnings
}

func merge(base domain.Report, in *generated, ev tools.Bundle) domain.Report {
	out := base
	out.Degraded = false
	out.ErrorCode = ""
	out.ErrorDetail = ""
	if verdict := strings.TrimSpace(in.Verdict); verdict != "" {
		out.Verdict = truncateRunes(verdict, 800)
	}

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
