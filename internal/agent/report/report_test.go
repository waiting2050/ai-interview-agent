package report_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	reportagent "github.com/waiting2050/ai-interview-agent/internal/agent/report"
	"github.com/waiting2050/ai-interview-agent/internal/agent/tools"
)

func bundle() tools.Bundle {
	return tools.Bundle{
		Overview: tools.Overview{
			SessionID:     "sess-1",
			TargetRole:    "Go 后端开发",
			CandidateName: "张三",
			Difficulty:    "mid",
			AnsweredTurns: 2,
			OverallScore:  72.5,
			Dimensions: []tools.DimensionSummary{
				{Dimension: "correctness", Score: 80},
				{Dimension: "depth", Score: 60},
				{Dimension: "engineering_judgment", Score: 70},
				{Dimension: "structure", Score: 75},
				{Dimension: "evidence_credibility", Score: 65},
			},
			WeakTopics: []string{"Go 内存模型"},
			Turns: []tools.TurnBrief{
				{TurnID: "t1", Seq: 1, Topic: "Go 并发与调度", Kind: "primary", Depth: 1, Question: "说说 GMP", Score: 80, Missing: 1, MissingList: []string{"抢占式调度细节"}},
				{TurnID: "t2", Seq: 2, Topic: "Go 内存模型", Kind: "followup", Depth: 1, Question: "逃逸分析怎么影响分配", Score: 65, Missing: 2,
					MissingList: []string{"栈上分配的条件", "写屏障的作用"}},
			},
		},
		WeakTurns: []tools.TurnSummary{
			{
				TurnBrief:     tools.TurnBrief{TurnID: "t2", Seq: 2, Topic: "Go 内存模型", Kind: "followup", Depth: 1, Question: "逃逸分析怎么影响分配", Score: 65, Missing: 2, MissingList: []string{"栈上分配的条件", "写屏障的作用"}},
				AnswerExcerpt: "逃逸分析会把不逃逸的对象放到栈上……",
			},
		},
		Turns: []tools.TurnSummary{
			{
				TurnBrief:     tools.TurnBrief{TurnID: "t1", Seq: 1, Topic: "Go 并发与调度", Kind: "primary", Depth: 1, Question: "说说 GMP", Score: 80, Missing: 1, MissingList: []string{"抢占式调度细节"}},
				AnswerExcerpt: "GMP 由 G、M、P 组成……",
			},
			{
				TurnBrief:     tools.TurnBrief{TurnID: "t2", Seq: 2, Topic: "Go 内存模型", Kind: "followup", Depth: 1, Question: "逃逸分析怎么影响分配", Score: 65, Missing: 2, MissingList: []string{"栈上分配的条件", "写屏障的作用"}},
				AnswerExcerpt: "逃逸分析会把不逃逸的对象放到栈上……",
			},
		},
		ToolCalls:     []tools.CallRecord{{Tool: "get_session_overview"}, {Tool: "list_weak_turns"}, {Tool: "search_turns"}},
		ToolBreakdown: map[string]int{"get_session_overview": 1, "list_weak_turns": 1, "search_turns": 1},
		Calls:         3,
	}
}

const validJSON = `{
  "verdict": "本次面试共完成两道 Go 题目，总体基础扎实，GMP 调度讲得较清楚；但内存模型相关的栈上分配条件与写屏障作用没有覆盖，回答容易停留在结论层。",
  "dimension_comments": {
    "correctness": "结论基本正确，个别机制细节需要再确认。",
    "depth": "能讲到组成，但没有深入到调度与内存回收机制。",
    "engineering_judgment": "缺少真实场景下的取舍与压测数据。",
    "structure": "表达有条理，建议先结论后展开。",
    "evidence_credibility": "项目细节偏少，可信度还可以加强。"
  },
  "questions": [
    {"turn_id": "t1", "comment": "GMP 的组成讲清楚了，可以补一个抢占式调度的具体触发场景。", "strengths": ["结构清晰"], "missing_points": ["抢占式调度细节"]},
    {"turn_id": "t2", "comment": "逃逸分析结论正确，但栈上分配条件与写屏障作用没有展开。", "strengths": ["结论正确"], "missing_points": ["写屏障的作用"]}
  ],
  "improvements": ["补齐 Go 内存模型中栈上分配与写屏障的机制细节", "为 GMP 调度准备一个线上抢占场景", "用压测数据说明优化前后的差异"],
  "next_steps": ["针对内存模型做一轮专项练习并录音复述。", "把逐题点评整理成自查清单。"]
}`

func TestGenerateMergesModelCommentaryWithDeterministicScores(t *testing.T) {
	client := llm.NewEinoClient(llm.Scripted("scripted", validJSON), "scripted")
	builder := reportagent.NewBuilder(client, "scripted")

	out, err := builder.Generate(context.Background(), bundle())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out.Degraded || out.Report.Degraded {
		t.Fatalf("unexpected degradation: %+v", out)
	}
	if out.Report.OverallScore != 72.5 || out.Report.ScoreBand != "良好" {
		t.Fatalf("score = %.1f band=%s", out.Report.OverallScore, out.Report.ScoreBand)
	}
	if len(out.Report.Sections) != 5 {
		t.Fatalf("sections = %d", len(out.Report.Sections))
	}
	if got := out.Report.Sections[0].Title; got != "总评" {
		t.Fatalf("first section = %q", got)
	}
	if len(out.Report.Improvements) != 3 || !strings.Contains(out.Report.Improvements[0], "内存模型") {
		t.Fatalf("improvements = %+v", out.Report.Improvements)
	}
	if out.Report.ToolCalls != 3 || out.Report.ToolBreakdown["search_turns"] != 1 {
		t.Fatalf("tool stats = %d %+v", out.Report.ToolCalls, out.Report.ToolBreakdown)
	}
	if !strings.Contains(out.Report.RenderMarkdown(), "逐题点评") {
		t.Fatalf("markdown missing question section")
	}
	if out.Report.Questions[1].Comment == "" {
		t.Fatalf("model question comment was not merged")
	}
}

func TestGenerateDegradesOnInvalidOutput(t *testing.T) {
	client := llm.NewEinoClient(llm.Scripted("scripted", "not json", "still not json"), "scripted")
	builder := reportagent.NewBuilder(client, "scripted")

	out, err := builder.Generate(context.Background(), bundle())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !out.Degraded || !out.Report.Degraded || out.ErrorCode != reportagent.ErrorCodeOutputInvalid {
		t.Fatalf("outcome = %+v", out)
	}
	if len(out.Report.Sections) != 5 || out.Report.Verdict == "" {
		t.Fatalf("degraded report is incomplete: %+v", out.Report)
	}
	if out.Attempts != 2 {
		t.Fatalf("attempts = %d", out.Attempts)
	}
}

func TestGenerateDegradesOnTransportError(t *testing.T) {
	client := llm.NewEinoClient(llm.NewFakeChatModel("broken", func(context.Context, llm.Request) (string, error) {
		return "", errors.New("connection reset")
	}), "broken")
	builder := reportagent.NewBuilder(client, "broken")

	out, err := builder.Generate(context.Background(), bundle())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !out.Degraded || out.ErrorCode != reportagent.ErrorCodeModelUnavailable {
		t.Fatalf("outcome = %+v", out)
	}
	if len(out.Report.Improvements) != 3 {
		t.Fatalf("fallback improvements = %+v", out.Report.Improvements)
	}
}

func TestGenerateWithoutAnswersMarksNoAnswers(t *testing.T) {
	empty := tools.Bundle{Overview: tools.Overview{SessionID: "sess-empty", TargetRole: "Go 后端"}}
	out, err := reportagent.NewBuilder(llm.NewEinoClient(llm.Scripted("scripted", validJSON), "scripted"), "scripted").
		Generate(context.Background(), empty)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !out.Degraded || out.ErrorCode != reportagent.ErrorCodeNoAnswers {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Report.Verdict == "" {
		t.Fatalf("empty verdict")
	}
}

func TestFallbackIsDeterministic(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	a := reportagent.Fallback(bundle(), now)
	b := reportagent.Fallback(bundle(), now)
	if a.OverallScore != b.OverallScore || a.Verdict != b.Verdict {
		t.Fatalf("fallback is not deterministic")
	}
	if len(a.Sections) != 5 {
		t.Fatalf("sections = %d", len(a.Sections))
	}
	if len(a.Improvements) != 3 {
		t.Fatalf("improvements = %+v", a.Improvements)
	}
}

func generateScripted(t *testing.T, raw string) (reportagent.Outcome, error) {
	t.Helper()
	builder := reportagent.NewBuilder(llm.NewEinoClient(llm.Scripted("scripted", raw), "scripted"), "scripted")
	return builder.Generate(context.Background(), bundle())
}

func TestGenerateStripsMarkdownFence(t *testing.T) {
	out, err := generateScripted(t, "```json\n"+validJSON+"\n```")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out.Degraded || out.Report.Degraded {
		t.Fatalf("fenced JSON should parse: %+v", out.ValidationIssues)
	}
	if !strings.Contains(out.Report.Verdict, "本次面试") {
		t.Fatalf("verdict = %q", out.Report.Verdict)
	}
}

func TestGenerateFillsMissingFieldsFromFallback(t *testing.T) {
	raw := `{"verdict": "本次面试完成两道 Go 题目，GMP 调度讲得清楚，但内存模型相关细节明显不足。"}`
	out, err := generateScripted(t, raw)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out.Degraded || out.Report.Degraded {
		t.Fatalf("missing optional fields should not degrade: %+v", out.ValidationIssues)
	}
	if len(out.Report.Improvements) != 3 || len(out.Report.NextSteps) < 2 {
		t.Fatalf("fallback fields were not filled: %+v", out.Report)
	}
	if out.Report.Questions[0].Comment == "" || out.Report.Dimensions[0].Comment == "" {
		t.Fatalf("deterministic comments missing: %+v", out.Report)
	}
	if !strings.Contains(out.Report.Verdict, "内存模型") {
		t.Fatalf("model verdict was not kept: %q", out.Report.Verdict)
	}
}

func TestGenerateIgnoresExtraFields(t *testing.T) {
	raw := strings.Replace(validJSON, "{", `{"model_notes": {"version": 2}, "schema_version": 9,`, 1)
	out, err := generateScripted(t, raw)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out.Degraded {
		t.Fatalf("extra fields should be ignored: %+v", out.ValidationIssues)
	}
	if !strings.Contains(out.Report.Verdict, "本次面试") {
		t.Fatalf("verdict = %q", out.Report.Verdict)
	}
}

func TestGenerateToleratesChinesePunctuation(t *testing.T) {
	raw := `{“verdict”： “本次面试完成两道 Go 题目，总体基础扎实，GMP 调度讲得清楚，但内存模型细节不足。”，
“dimension_comments”： {“correctness”： “结论基本正确，机制细节还需确认。”}，
“improvements”： [“补齐内存模型机制细节”， “准备一个线上抢占场景”， “补充压测数据”]，
“next_steps”： [“针对内存模型做专项练习并录音。”， “整理逐题自查清单。”]}`
	out, err := generateScripted(t, raw)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out.Degraded || out.Report.Degraded {
		t.Fatalf("full-width punctuation should parse: %+v", out.ValidationIssues)
	}
	if !strings.Contains(out.Report.Verdict, "GMP") {
		t.Fatalf("verdict = %q", out.Report.Verdict)
	}
}

func TestGenerateExtractsJSONFromProse(t *testing.T) {
	raw := "以下是报告，请查收：\n" + validJSON + "\n以上，如需调整请告诉我。"
	out, err := generateScripted(t, raw)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out.Degraded {
		t.Fatalf("JSON wrapped in prose should parse: %+v", out.ValidationIssues)
	}
}

func TestGenerateRejectsUnknownTurnIDWithFieldPath(t *testing.T) {
	raw := strings.Replace(validJSON, `"turn_id": "t1"`, `"turn_id": "ghost"`, 1)
	out, err := generateScripted(t, raw)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !out.Degraded || out.ErrorCode != reportagent.ErrorCodeOutputInvalid {
		t.Fatalf("outcome = %+v", out)
	}
	detail := strings.Join(out.ValidationIssues, "; ")
	if !strings.Contains(detail, "questions[0].turn_id") || !strings.Contains(detail, "turn_summaries") {
		t.Fatalf("missing field path detail: %q", detail)
	}
	if out.Report.ErrorDetail == "" {
		t.Fatalf("report error detail is empty")
	}
}

func TestGenerateReportsTypeMismatchPath(t *testing.T) {
	out, err := generateScripted(t, `{"verdict": 123}`)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !out.Degraded || out.ErrorCode != reportagent.ErrorCodeOutputInvalid {
		t.Fatalf("outcome = %+v", out)
	}
	detail := strings.Join(out.ValidationIssues, "; ")
	if !strings.Contains(detail, "verdict") || !strings.Contains(detail, "string") {
		t.Fatalf("type mismatch path missing: %q", detail)
	}
}
