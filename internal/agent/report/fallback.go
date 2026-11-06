// Package report turns a read-only evidence bundle into the structured
// interview report. Scores and structure are computed deterministically from
// persisted evaluations; the model only writes commentary. That split lets a
// model failure degrade to a complete report instead of failing the run.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/tools"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	domain "github.com/waiting2050/ai-interview-agent/internal/domain/report"
)

const (
	ErrorCodeModelUnavailable = "model_unavailable"
	ErrorCodeOutputInvalid    = "report_output_invalid"
	ErrorCodeNoAnswers        = "no_answers"
)

// Fallback assembles a complete report from evaluation data alone.
func Fallback(ev tools.Bundle, now time.Time) domain.Report {
	overview := ev.Overview
	out := domain.Report{
		SchemaVersion: domain.SchemaVersion,
		SessionID:     overview.SessionID,
		GeneratedAt:   now,
		OverallScore:  overview.OverallScore,
		PlanItems:     overview.PlanItems,
		ScoreBand:     domain.Band(overview.OverallScore),
		ToolCalls:     ev.Calls,
		ToolBreakdown: ev.ToolBreakdown,
	}

	for _, dim := range overview.Dimensions {
		out.Dimensions = append(out.Dimensions, domain.DimensionScore{
			Dimension: dim.Dimension,
			Score:     dim.Score,
			Weight:    evaluation.Weights[evaluation.Dimension(dim.Dimension)],
			Comment:   dimensionComment(dim.Dimension, dim.Score),
		})
	}

	for _, turn := range overview.Turns {
		review := domain.QuestionReview{
			TurnID:        turn.TurnID,
			Seq:           turn.Seq,
			Topic:         turn.Topic,
			Kind:          turn.Kind,
			Depth:         turn.Depth,
			Question:      turn.Question,
			Score:         turn.Score,
			Scores:        turn.Scores,
			BaseScore:     turn.BaseScore,
			Bonus:         adjustments(turn.Bonus),
			Penalty:       adjustments(turn.Penalty),
			Degraded:      turn.Degraded,
			MissingPoints: append([]string(nil), turn.MissingList...),
		}
		for _, e := range evaluation.UniqueEvidence(turn.Evidence) {
			review.Evidence = append(review.Evidence, domain.Evidence{
				TurnID:    turn.TurnID,
				Dimension: string(e.Dimension),
				Quote:     e.Quote,
				Reason:    e.Reason,
			})
		}
		switch {
		case turn.Degraded:
			review.Comment = "本题评估降级，仅有作答记录，建议对照题纲自行复盘。"
		case turn.Score >= 75:
			review.Comment = "回答较完整，能落到实现细节；继续保持量化表达。"
			review.StrongPoints = []string{"覆盖了主要考察点"}
		case turn.Score >= 60:
			review.Comment = "基本正确，但深度与取舍说明不足，容易停留在结论层。"
		default:
			review.Comment = "关键点覆盖不足，建议按「结论-原理-取舍-数据」四段式重答。"
		}
		out.Questions = append(out.Questions, review)
	}

	out.Improvements = improvements(overview, 3)
	out.NextSteps = nextSteps(out.Improvements)
	if overview.AnsweredTurns == 0 {
		out.Verdict = "本次面试没有有效作答记录，无法形成能力判断。建议先完成至少 3 道主问题后再生成报告。"
		out.Improvements = []string{
			"先完成一轮完整面试，确保至少有 3 道题的有效作答与评估记录",
			"每道题按「结论-原理-取舍-数据」四段式作答",
			"准备一个能讲清背景、职责与量化结果的项目案例",
		}
		out.NextSteps = []string{"重新开始一场完整面试并认真作答至少 3 题。", "对照题库题纲，逐项补齐缺失知识点。"}
	} else {
		out.Verdict = verdict(overview)
	}

	out.Sections = out.RenderSections()
	return out
}

func verdict(overview tools.Overview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "本次面试共完成 %d 道题，综合得分 %.1f 分（%s）。", overview.AnsweredTurns, overview.OverallScore, domain.Band(overview.OverallScore))
	if best, worst, ok := dimensionExtremes(overview.Dimensions); ok {
		fmt.Fprintf(&b, "表现相对最好的是%s（%.1f 分），最薄弱的是%s（%.1f 分）。",
			dimensionLabel(best.Dimension), best.Score, dimensionLabel(worst.Dimension), worst.Score)
	}
	if len(overview.WeakTopics) > 0 {
		fmt.Fprintf(&b, "从题目维度看，%s相关内容需要优先补强。", strings.Join(overview.WeakTopics, "、"))
	}
	if overview.DegradedTurns > 0 {
		fmt.Fprintf(&b, "另有 %d 道题因模型不可用而评估降级，已按作答记录保留。", overview.DegradedTurns)
	}
	b.WriteString("建议先解决报告中列出的三个短板，再进行下一轮模拟面试。")
	return b.String()
}

func dimensionExtremes(dims []tools.DimensionSummary) (tools.DimensionSummary, tools.DimensionSummary, bool) {
	if len(dims) == 0 {
		return tools.DimensionSummary{}, tools.DimensionSummary{}, false
	}
	var scored []tools.DimensionSummary
	for _, d := range dims {
		if d.Score > 0 {
			scored = append(scored, d)
		}
	}
	if len(scored) == 0 {
		return tools.DimensionSummary{}, tools.DimensionSummary{}, false
	}
	best, worst := scored[0], scored[0]
	for _, d := range scored[1:] {
		if d.Score > best.Score {
			best = d
		}
		if d.Score < worst.Score {
			worst = d
		}
	}
	return best, worst, true
}

func adjustments(in []evaluation.Adjustment) []domain.ScoreAdjustment {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.ScoreAdjustment, 0, len(in))
	for _, a := range in {
		out = append(out, domain.ScoreAdjustment{
			Type:   a.Type,
			Quote:  a.Quote,
			Points: a.Points,
			Reason: a.Reason,
		})
	}
	return out
}

func dimensionComment(dimension string, score float64) string {
	switch {
	case score >= 80:
		return "该维度表现稳定，能给出可验证的细节。"
	case score >= 60:
		return "该维度基本达标，但深度与取舍仍需补强。"
	case score > 0:
		return "该维度明显偏弱，建议针对性练习。"
	default:
		return "本次没有可用于该维度的有效评估。"
	}
}

func improvements(overview tools.Overview, limit int) []string {
	type point struct {
		text  string
		count int
	}
	counts := map[string]int{}
	for _, turn := range overview.Turns {
		for _, missing := range turn.MissingList {
			missing = strings.TrimSpace(missing)
			if missing != "" {
				counts[missing]++
			}
		}
	}
	var points []point
	for text, count := range counts {
		points = append(points, point{text: text, count: count})
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].count == points[j].count {
			return points[i].text < points[j].text
		}
		return points[i].count > points[j].count
	})

	var out []string
	for _, p := range points {
		text := p.text
		if !strings.Contains(text, "建议") && !strings.Contains(text, "练习") {
			text = "补齐「" + text + "」，并用一个真实项目案例讲清取舍"
		}
		out = append(out, text)
		if len(out) == limit {
			return out
		}
	}
	for _, topic := range overview.WeakTopics {
		out = append(out, "围绕「"+topic+"」准备一段 2 分钟的深入讲解")
		if len(out) == limit {
			return out
		}
	}
	defaults := []string{
		"用「结论-原理-取舍-数据」四段式重构每道题的回答",
		"为每个核心知识点准备一个可追问的线上问题案例",
		"补充量化指标，说明优化前后的对比与验证方式",
	}
	for _, d := range defaults {
		if len(out) == limit {
			break
		}
		out = append(out, d)
	}
	return out
}

func nextSteps(improvements []string) []string {
	var out []string
	for i, point := range improvements {
		if i >= 2 {
			break
		}
		out = append(out, "针对「"+short(point, 40)+"」做一轮专项练习，并录音复述。")
	}
	out = append(out, "把本次报告的逐题点评整理成自查清单，下一次面试前逐条核对。")
	if len(out) < 2 {
		out = append(out, "完成一场新的模拟面试，验证短板是否已经补上。")
	}
	return out
}

func short(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

func dimensionLabel(dimension string) string {
	switch dimension {
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
		return dimension
	}
}
