// Package report defines the structured interview report contract. The JSON
// shape is stable and persisted as jsonb; the Markdown rendering is persisted
// alongside it so the demo never has to re-render from the model.
package report

import (
	"fmt"
	"strings"
	"time"
)

const SchemaVersion = 1

// DimensionScore is one rubric dimension aggregated over the whole interview.
// Score is 0-100 (raw 1-5 averages are multiplied by 20).
type DimensionScore struct {
	Dimension string  `json:"dimension"`
	Score     float64 `json:"score"`
	Weight    float64 `json:"weight,omitempty"`
	Comment   string  `json:"comment,omitempty"`
}

// Evidence quotes one answer fragment the review is based on.
type Evidence struct {
	TurnID    string `json:"turn_id"`
	Dimension string `json:"dimension,omitempty"`
	Quote     string `json:"quote"`
	Reason    string `json:"reason,omitempty"`
}

// ScoreAdjustment is one bonus or penalty entry with its evidence quote. The
// demo renders the scoring arithmetic from these fields.
type ScoreAdjustment struct {
	Type   string `json:"type"`
	Quote  string `json:"quote"`
	Points int    `json:"points"`
	Reason string `json:"reason,omitempty"`
}

// QuestionReview is the per-question commentary. Score is the persisted
// weighted score (0-100); degraded evaluations keep Degraded=true and are
// excluded from aggregates.
type QuestionReview struct {
	TurnID        string            `json:"turn_id"`
	Seq           int               `json:"seq"`
	Topic         string            `json:"topic"`
	Kind          string            `json:"kind"`
	Depth         int               `json:"depth"`
	Question      string            `json:"question"`
	Score         float64           `json:"score"`
	Scores        map[string]int    `json:"scores,omitempty"`
	BaseScore     float64           `json:"base_score,omitempty"`
	Bonus         []ScoreAdjustment `json:"bonus,omitempty"`
	Penalty       []ScoreAdjustment `json:"penalty,omitempty"`
	Degraded      bool              `json:"degraded,omitempty"`
	Comment       string            `json:"comment,omitempty"`
	StrongPoints  []string          `json:"strong_points,omitempty"`
	MissingPoints []string          `json:"missing_points,omitempty"`
	Evidence      []Evidence        `json:"evidence,omitempty"`
}

// Section is one progressively emitted report block. Sections are also
// embedded in the structured report so a consumer can render either form.
type Section struct {
	Index     int      `json:"index"`
	Title     string   `json:"title"`
	Markdown  string   `json:"markdown"`
	TurnRefs  []string `json:"turn_refs,omitempty"`
	ToolCalls int      `json:"tool_calls"`
}

// Report is the full structured deliverable.
type Report struct {
	SchemaVersion int              `json:"schema_version"`
	SessionID     string           `json:"session_id"`
	GeneratedAt   time.Time        `json:"generated_at"`
	Degraded      bool             `json:"degraded"`
	ErrorCode     string           `json:"error_code,omitempty"`
	ErrorDetail   string           `json:"error_detail,omitempty"`
	OverallScore  float64          `json:"overall_score"`
	PlanItems     int              `json:"plan_items"`
	ScoreBand     string           `json:"score_band"`
	Verdict       string           `json:"verdict"`
	Dimensions    []DimensionScore `json:"dimensions"`
	Questions     []QuestionReview `json:"questions"`
	Improvements  []string         `json:"improvements"`
	NextSteps     []string         `json:"next_steps"`
	Sections      []Section        `json:"sections"`
	ToolCalls     int              `json:"tool_calls"`
	ToolBreakdown map[string]int   `json:"tool_breakdown,omitempty"`
	// Cost fields follow the tool_calls naming convention: a total plus an
	// optional per-purpose breakdown.
	ModelCalls         int               `json:"model_calls"`
	ModelCallBreakdown map[string]int    `json:"model_call_breakdown,omitempty"`
	PromptTokens       int               `json:"prompt_tokens"`
	CompletionTokens   int               `json:"completion_tokens"`
	TotalTokens        int               `json:"total_tokens"`
	ModelName          string            `json:"model_name,omitempty"`
	PromptVersion      string            `json:"prompt_version,omitempty"`
	Meta               map[string]string `json:"meta,omitempty"`
}

// Band maps a 0-100 score to the same coarse bands the report text uses.
func Band(score float64) string {
	switch {
	case score >= 85:
		return "优秀"
	case score >= 70:
		return "良好"
	case score >= 60:
		return "及格"
	default:
		return "待提升"
	}
}

// RenderSections splits the report into the five sections emitted through
// report.section events. It is deterministic so the structured JSON and the
// Markdown always agree.
func (r *Report) RenderSections() []Section {
	var sections []Section
	refs := func() []string {
		ids := make([]string, 0, len(r.Questions))
		for _, q := range r.Questions {
			ids = append(ids, q.TurnID)
		}
		return ids
	}

	var overview strings.Builder
	fmt.Fprintf(&overview, "**总评：%s（%s，%.1f 分）**\n\n%s\n", r.ScoreBand, Band(r.OverallScore), r.OverallScore, r.Verdict)
	sections = append(sections, Section{Index: 0, Title: "总评", Markdown: overview.String(), ToolCalls: r.ToolCalls})

	var dims strings.Builder
	dims.WriteString("| 维度 | 得分 | 说明 |\n| --- | --- | --- |\n")
	for _, d := range r.Dimensions {
		fmt.Fprintf(&dims, "| %s | %.1f | %s |\n", dimensionLabel(d.Dimension), d.Score, d.Comment)
	}
	sections = append(sections, Section{Index: 1, Title: "分维度表现", Markdown: dims.String(), ToolCalls: r.ToolCalls})

	var questions strings.Builder
	if len(r.Questions) == 0 {
		questions.WriteString("本次没有有效作答记录。\n")
	}
	for _, q := range r.Questions {
		fmt.Fprintf(&questions, "### %d. %s（%s，%.1f 分）\n\n", q.Seq, q.Topic, kindLabel(q.Kind), q.Score)
		if q.Question != "" {
			fmt.Fprintf(&questions, "> %s\n\n", truncate(q.Question, 180))
		}
		if q.Comment != "" {
			fmt.Fprintf(&questions, "%s\n\n", q.Comment)
		}
		if len(q.StrongPoints) > 0 {
			fmt.Fprintf(&questions, "- 亮点：%s\n", strings.Join(q.StrongPoints, "；"))
		}
		if len(q.MissingPoints) > 0 {
			fmt.Fprintf(&questions, "- 待补：%s\n", strings.Join(q.MissingPoints, "；"))
		}
	}
	sections = append(sections, Section{Index: 2, Title: "逐题点评", Markdown: questions.String(), TurnRefs: refs(), ToolCalls: r.ToolCalls})

	var improvements strings.Builder
	for i, point := range r.Improvements {
		fmt.Fprintf(&improvements, "%d. %s\n", i+1, point)
	}
	if len(r.Improvements) == 0 {
		improvements.WriteString("暂无明显短板，建议继续加强系统设计与量化表达。\n")
	}
	sections = append(sections, Section{Index: 3, Title: "最需要补的 3 个点", Markdown: improvements.String(), ToolCalls: r.ToolCalls})

	var next strings.Builder
	for i, step := range r.NextSteps {
		fmt.Fprintf(&next, "%d. %s\n", i+1, step)
	}
	if len(r.NextSteps) == 0 {
		next.WriteString("按本次报告中的待补点准备一轮专项练习。\n")
	}
	sections = append(sections, Section{Index: 4, Title: "下一步练习建议", Markdown: next.String(), ToolCalls: r.ToolCalls})

	return sections
}

// RenderMarkdown concatenates all sections. It is what gets persisted in the
// markdown column and returned by GET /report.
func (r *Report) RenderMarkdown() string {
	if len(r.Sections) == 0 {
		r.Sections = r.RenderSections()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# 面试报告\n\n- 会话：`%s`\n- 生成时间：%s\n- 综合得分：%.1f / 100（%s）\n",
		r.SessionID, r.GeneratedAt.Format(time.RFC3339), r.OverallScore, r.ScoreBand)
	if r.Degraded {
		fmt.Fprintf(&b, "- 说明：模型不可用，本报告由已有评估数据降级拼装（%s）\n", r.ErrorCode)
	}
	b.WriteString("\n")
	for _, s := range r.Sections {
		fmt.Fprintf(&b, "## %s\n\n%s\n", s.Title, s.Markdown)
	}
	return b.String()
}

func dimensionLabel(d string) string {
	switch d {
	case "correctness":
		return "正确性"
	case "depth":
		return "深度"
	case "engineering_judgment":
		return "工程判断"
	case "structure":
		return "表达结构"
	case "evidence_credibility":
		return "证据可信度"
	default:
		return d
	}
}

func kindLabel(kind string) string {
	if kind == "followup" {
		return "追问"
	}
	return "主问题"
}

func truncate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

// Record is the persisted report row. Status is always completed for now; the
// unique session_id is the exactly-one-report invariant.
type Record struct {
	ID        string
	SessionID string
	RunID     string
	Status    string
	Report    Report
	Markdown  string
	CreatedAt time.Time
	UpdatedAt time.Time
}
