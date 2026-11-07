package interview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/agent/prompt"
	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
)

const (
	ErrorCodeModelUnavailable  = "model_unavailable"
	ErrorCodeQuestionInvalid   = "question_output_invalid"
	ErrorCodeQuestionDuplicate = "question_duplicate"

	minQuestionRunes = 15
	maxQuestionRunes = 400
)

// QuestionInput drives one question-generation call.
type QuestionInput struct {
	Kind           string
	TargetRole     string
	Difficulty     string
	JDText         string
	Profile        resume.Profile
	Topic          string
	Objective      string
	ExpectedPoints []string
	History        []string
	FollowupTarget string
	LastQuestion   string
	Answer         string
}

// QuestionOutcome is always usable: on transport failure or repeated invalid
// output the generator degrades to a deterministic question so a turn can
// still advance. Degraded and ErrorCode are for logs/metrics.
type QuestionOutcome struct {
	Text             string
	Degraded         bool
	ErrorCode        string
	Attempts         int
	RawOutput        string
	PromptTokens     int
	CompletionTokens int
	ModelName        string
	PromptVersion    string
}

// QuestionGenerator turns an outline item plus profile into one interview
// question, with history-based deduplication and a deterministic fallback.
type QuestionGenerator struct {
	client      llm.Client
	modelName   string
	maxRepairs  int
	temperature float32
}

func NewQuestionGenerator(client llm.Client, modelName string) *QuestionGenerator {
	return &QuestionGenerator{
		client:      client,
		modelName:   modelName,
		maxRepairs:  2,
		temperature: 0.7,
	}
}

// Generate never returns an error for provider or validation failures: it
// falls back to a deterministic question. The only error is a canceled
// caller context, which should abort the run.
func (g *QuestionGenerator) Generate(ctx context.Context, in QuestionInput) (QuestionOutcome, error) {
	out := QuestionOutcome{ModelName: g.modelName, PromptVersion: prompt.QuestionVersion}
	if g.client == nil {
		out.Text = FallbackQuestion(in)
		out.Degraded = true
		out.ErrorCode = ErrorCodeModelUnavailable
		return out, nil
	}

	kind := in.Kind
	if kind == "" {
		kind = prompt.QuestionKindPrimary
	}
	promptIn := prompt.QuestionInput{
		Kind:           kind,
		TargetRole:     in.TargetRole,
		Difficulty:     in.Difficulty,
		JDText:         in.JDText,
		ProfileText:    in.Profile.Text(),
		Topic:          in.Topic,
		Objective:      in.Objective,
		ExpectedPoints: in.ExpectedPoints,
		History:        in.History,
		FollowupTarget: in.FollowupTarget,
		LastQuestion:   in.LastQuestion,
		Answer:         in.Answer,
	}
	user := prompt.QuestionUser(promptIn)

	var issues []string
	for attempt := 1; attempt <= 1+g.maxRepairs; attempt++ {
		req := llm.Request{
			System:      prompt.QuestionSystem(kind),
			User:        user,
			Temperature: g.temperature,
			MaxTokens:   600,
		}
		if attempt > 1 {
			req.User = user + "\n\n" + prompt.QuestionRepairMessage(issues)
		}

		resp, err := g.client.Generate(ctx, req)
		if err != nil {
			if ctx.Err() != nil {
				return QuestionOutcome{}, ctx.Err()
			}
			out.Attempts = attempt
			out.Text = FallbackQuestion(in)
			out.Degraded = true
			out.ErrorCode = ErrorCodeModelUnavailable
			return out, nil
		}
		out.Attempts = attempt
		out.RawOutput = resp.Text
		out.PromptTokens += resp.PromptTokens
		out.CompletionTokens += resp.CompletionTokens

		question, parseErr := parseQuestion(resp.Text)
		if parseErr != nil {
			issues = []string{parseErr.Error()}
			out.ErrorCode = ErrorCodeQuestionInvalid
			continue
		}
		issues = validateQuestion(question, in.History)
		if len(issues) == 0 {
			out.Text = question
			out.Degraded = false
			out.ErrorCode = ""
			return out, nil
		}
		out.ErrorCode = ErrorCodeQuestionInvalid
		for _, issue := range issues {
			if strings.Contains(issue, "重复") {
				out.ErrorCode = ErrorCodeQuestionDuplicate
				break
			}
		}
	}

	out.Text = FallbackQuestion(in)
	out.Degraded = true
	return out, nil
}

func parseQuestion(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if start, end := strings.Index(text, "{"), strings.LastIndex(text, "}"); start >= 0 && end > start {
		text = text[start : end+1]
	}

	var envelope struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		return "", fmt.Errorf("输出不是合法 JSON：%v", err)
	}
	question := strings.TrimSpace(envelope.Question)
	if question == "" {
		return "", fmt.Errorf("question 字段为空")
	}
	return question, nil
}

func validateQuestion(question string, history []string) []string {
	var issues []string
	length := len([]rune(question))
	if length < minQuestionRunes {
		issues = append(issues, fmt.Sprintf("问题过短（%d 字，至少 %d 字）", length, minQuestionRunes))
	}
	if length > maxQuestionRunes {
		issues = append(issues, fmt.Sprintf("问题过长（%d 字，最多 %d 字）", length, maxQuestionRunes))
	}
	normalized := normalizeQuestion(question)
	for _, previous := range history {
		if normalizeQuestion(previous) == normalized {
			issues = append(issues, "与历史问题重复")
			break
		}
	}
	return issues
}

// normalizeQuestion keeps only letters, digits and Han characters so
// punctuation and whitespace differences do not defeat deduplication.
func normalizeQuestion(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// FallbackQuestion generates a deterministic, history-aware question. It is
// used when the model is unavailable or keeps returning invalid/duplicate
// output.
func FallbackQuestion(in QuestionInput) string {
	focus := in.Objective
	if len(in.ExpectedPoints) > 0 {
		focus = in.ExpectedPoints[len(in.History)%len(in.ExpectedPoints)]
	}
	if focus == "" {
		focus = in.Topic
	}
	topic := in.Topic
	if topic == "" {
		topic = "当前知识点"
	}

	var question string
	if in.Kind == prompt.QuestionKindFollowup {
		target := in.FollowupTarget
		if target == "" {
			target = focus
		}
		question = fmt.Sprintf("针对你刚才的回答，请继续展开「%s」：它在高并发或故障场景下会怎样表现？请给出具体的实现细节和验证方式。", target)
	} else {
		templates := []string{
			"请结合一个真实项目，说明「%s」的实现原理、关键取舍以及你会如何验证。重点讲讲「%s」。",
			"如果要在生产环境落地「%s」，你会如何设计它的边界条件和失败处理？请重点说明「%s」。",
			"围绕「%s」，请讲一个你实际遇到过的问题：现象是什么、如何定位、最终怎么解决。请覆盖「%s」。",
		}
		template := templates[len(in.History)%len(templates)]
		question = fmt.Sprintf(template, topic, focus)
	}

	if len(validateQuestion(question, in.History)) > 0 {
		angle := focus
		if len(in.ExpectedPoints) > 0 {
			angle = in.ExpectedPoints[(len(in.History)+1)%len(in.ExpectedPoints)]
		}
		question = fmt.Sprintf("%s（请从第 %d 个角度补充：%s）", question, len(in.History)+1, angle)
	}
	return question
}
