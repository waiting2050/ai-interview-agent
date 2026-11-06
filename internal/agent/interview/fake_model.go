package interview

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/agent/prompt"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
)

// NewHeuristicClient is the default model for local runs: no API key, but the
// evaluation path, JSON validation and persistence run end to end. It quotes
// the answer verbatim so the evidence checks behave like a real model.
func NewHeuristicClient() llm.Client {
	return llm.NewEinoClient(NewHeuristicChatModel(), "fake-heuristic")
}

func NewHeuristicChatModel() *llm.FakeChatModel {
	return llm.NewFakeChatModel("fake-heuristic", func(ctx context.Context, req llm.Request) (string, error) {
		if strings.Contains(req.System, "[TASK:report]") {
			return heuristicReport(req.User), nil
		}
		if strings.Contains(req.System, "[TASK:question]") || strings.Contains(req.System, "[TASK:followup]") {
			return heuristicQuestion(req.System, req.User), nil
		}
		answer := between(req.User, "<answer>", "</answer>")
		expected := expectedPoints(req.User)
		return heuristicJSON(answer, expected), nil
	})
}

func heuristicReport(user string) string {
	var (
		turns []struct {
			ID    string `json:"turn_id"`
			Topic string `json:"topic"`
		}
		verdict = "本次面试整体完成度较好，回答能覆盖主要考察点，但在深度与量化表达上仍有提升空间，建议针对薄弱题继续专项练习。"
		seen    = map[string]bool{}
	)
	for _, line := range strings.Split(user, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- turn_id=") {
			continue
		}
		id := reportValue(line, "turn_id=", " seq=", " topic=")
		topic := reportValue(line, "topic=", " score=")
		if id != "" && !seen[id] {
			seen[id] = true
			turns = append(turns, struct {
				ID    string `json:"turn_id"`
				Topic string `json:"topic"`
			}{ID: id, Topic: topic})
		}
	}
	if len(turns) > 0 {
		verdict = "本次面试共覆盖多个技术主题，整体表达较清晰，能给出基本正确的结论；但在实现细节、边界条件与量化验证上仍有明显缺口，建议围绕薄弱题做专项复盘后再进行下一轮模拟面试。"
	}

	type question struct {
		TurnID        string   `json:"turn_id"`
		Comment       string   `json:"comment"`
		Strengths     []string `json:"strengths"`
		MissingPoints []string `json:"missing_points"`
	}
	questions := make([]question, 0, len(turns))
	for _, turn := range turns {
		topic := turn.Topic
		if topic == "" {
			topic = "本题"
		}
		questions = append(questions, question{
			TurnID:        turn.ID,
			Comment:       "该题围绕「" + topic + "」展开，结论基本正确，但实现细节与边界条件不够充分，建议补充真实项目中的取舍与量化数据。",
			Strengths:     []string{"结论方向正确"},
			MissingPoints: []string{"实现细节与量化数据"},
		})
	}

	result := struct {
		Verdict           string            `json:"verdict"`
		DimensionComments map[string]string `json:"dimension_comments"`
		Questions         []question        `json:"questions"`
		Improvements      []string          `json:"improvements"`
		NextSteps         []string          `json:"next_steps"`
	}{
		Verdict: verdict,
		DimensionComments: map[string]string{
			"correctness":          "结论基本正确，但需要更多边界条件验证。",
			"depth":                "能讲到基本原理，深层的机制与权衡还不够。",
			"engineering_judgment": "有工程意识，但取舍依据与风险说明偏少。",
			"structure":            "表达有条理，建议先给结论再展开论据。",
			"evidence_credibility": "细节尚可，建议补充可验证的量化指标。",
		},
		Questions: questions,
		Improvements: []string{
			"为每个核心知识点补充一个可追问的线上问题案例",
			"用「结论-原理-取舍-数据」四段式重构回答",
			"补充压测或监控数据，说明优化前后的对比",
		},
		NextSteps: []string{
			"针对薄弱题做一轮专项练习，并录音复述检查结构。",
			"把本次逐题点评整理成自查清单，下次面试前逐条核对。",
		},
	}
	raw, _ := json.Marshal(result)
	return string(raw)
}

func reportValue(line, key string, nextKeys ...string) string {
	i := strings.Index(line, key)
	if i < 0 {
		return ""
	}
	rest := line[i+len(key):]
	end := len(rest)
	for _, next := range nextKeys {
		if j := strings.Index(rest, next); j >= 0 && j < end {
			end = j
		}
	}
	return strings.TrimSpace(rest[:end])
}

func heuristicQuestion(system, user string) string {
	kind := prompt.QuestionKindPrimary
	if strings.Contains(system, "[TASK:followup]") {
		kind = prompt.QuestionKindFollowup
	}
	in := QuestionInput{
		Kind:           kind,
		Topic:          between(user, "<topic>", "</topic>"),
		Objective:      between(user, "<objective>", "</objective>"),
		ExpectedPoints: splitPoints(between(user, "<expected_points>", "</expected_points>")),
		History:        historyLines(between(user, "<history>", "</history>")),
		FollowupTarget: between(user, "<followup_target>", "</followup_target>"),
		LastQuestion:   between(user, "<last_question>", "</last_question>"),
		Answer:         between(user, "<last_answer>", "</last_answer>"),
	}
	raw, _ := json.Marshal(struct {
		Question string `json:"question"`
	}{Question: FallbackQuestion(in)})
	return string(raw)
}

func heuristicJSON(answer string, expected []string) string {
	answer = strings.TrimSpace(answer)
	length := len([]rune(answer))

	score := 3
	missing := []string{}
	suggestion := evaluation.FollowupSuggestion{}
	switch {
	case length < 40:
		score = 2
		missing = []string{"缺少具体的实现细节与取舍"}
		suggestion = evaluation.FollowupSuggestion{
			Needed: true,
			Target: "请补充具体实现方式，以及遇到的关键取舍",
			Reason: "回答过于概括，缺少可验证的实现细节",
		}
	case length < 120:
		score = 3
	default:
		score = 4
	}

	quote := answer
	if r := []rune(answer); len(r) > 40 {
		quote = string(r[:40])
	}

	evidence := []evaluation.Evidence{
		{Dimension: evaluation.DimensionCorrectness, Quote: quote, Reason: "回答原文片段"},
	}
	// With a real outline the heuristic covers every expected point for a
	// substantive answer, which lets the coverage gate be exercised offline.
	if len(expected) > 0 {
		evidence = evidence[:0]
		if length >= 120 {
			for _, point := range expected {
				evidence = append(evidence, evaluation.Evidence{
					Dimension: evaluation.DimensionCorrectness,
					Quote:     quote,
					Reason:    "覆盖：" + point,
				})
			}
			missing = nil
			suggestion = evaluation.FollowupSuggestion{}
		} else {
			if len(missing) == 0 {
				missing = append(missing, expected...)
			}
			suggestion = evaluation.FollowupSuggestion{
				Needed: true,
				Target: "请补充：" + expected[0],
				Reason: "回答没有覆盖本题的期望要点",
			}
			// Uncovered points belong in missing_points, not evidence: marking
			// them as evidence would make coveredAllPoints treat them as covered
			// and silently skip the follow-up. Keep one quote-backed evidence so
			// the suggestion passes the "has evidence" gate.
			evidence = append(evidence, evaluation.Evidence{
				Dimension: evaluation.DimensionCorrectness,
				Quote:     quote,
				Reason:    "回答原文片段",
			})
		}
	}

	result := evaluation.Result{
		SchemaVersion: evaluation.SchemaVersion,
		Scores: map[evaluation.Dimension]int{
			evaluation.DimensionCorrectness:         score,
			evaluation.DimensionDepth:               score,
			evaluation.DimensionEngineeringJudgment: score,
			evaluation.DimensionStructure:           score,
			evaluation.DimensionEvidenceCredibility: score,
		},
		Evidence:      evidence,
		Bonus:         []evaluation.Adjustment{},
		Penalty:       []evaluation.Adjustment{},
		Confidence:    0.55,
		MissingPoints: missing,
		Followup:      suggestion,
	}
	raw, _ := json.Marshal(result)
	return string(raw)
}

func expectedPoints(user string) []string {
	for _, line := range strings.Split(user, "\n") {
		if strings.Contains(line, "本题期望覆盖：") {
			_, value, _ := strings.Cut(line, "本题期望覆盖：")
			return splitPoints(value)
		}
	}
	return nil
}

func splitPoints(raw string) []string {
	raw = strings.ReplaceAll(raw, "；", ";")
	parts := strings.Split(raw, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func historyLines(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if i := strings.Index(line, ". "); i > 0 {
			line = line[i+2:]
		}
		out = append(out, strings.TrimSpace(line))
	}
	return out
}

func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	s = s[i+len(start):]
	j := strings.Index(s, end)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(s[:j])
}
