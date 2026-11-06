package interview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/agent/prompt"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
)

// ErrorCodeModelOutputInvalid marks an evaluation that stayed unparseable
// after the repair budget. Such a record is persisted as degraded.
const ErrorCodeModelOutputInvalid = "model_output_invalid"

type Evaluator struct {
	client      llm.Client
	modelName   string
	maxRepairs  int
	temperature float32
}

func NewEvaluator(client llm.Client, modelName string) *Evaluator {
	return &Evaluator{
		client:      client,
		modelName:   modelName,
		maxRepairs:  2,
		temperature: 0.1,
	}
}

type EvaluateInput struct {
	SessionID      string
	TurnID         string
	Topic          string
	Question       string
	Answer         string
	TargetRole     string
	Difficulty     string
	ResumeText     string
	JDText         string
	ExpectedPoints []string
}

type Outcome struct {
	Result           evaluation.Result
	Degraded         bool
	ErrorCode        string
	RawOutput        string
	Attempts         int
	PromptTokens     int
	CompletionTokens int
	ModelName        string
	PromptVersion    string
}

// Evaluate calls the chat model, validates the JSON against the rubric and
// retries with explicit repair instructions. Transport errors are returned as
// errors (the run can be retried); unparseable output after the repair budget
// degrades to an unscored record so the interview can continue.
func (e *Evaluator) Evaluate(ctx context.Context, in EvaluateInput) (Outcome, error) {
	user := prompt.EvaluationUser(prompt.EvaluationInput{
		TargetRole:     in.TargetRole,
		Difficulty:     in.Difficulty,
		Topic:          in.Topic,
		Question:       in.Question,
		Answer:         in.Answer,
		ResumeText:     in.ResumeText,
		JDText:         in.JDText,
		ExpectedPoints: in.ExpectedPoints,
	})

	out := Outcome{ModelName: e.modelName, PromptVersion: prompt.EvaluationVersion}
	var issues []string

	for attempt := 1; attempt <= 1+e.maxRepairs; attempt++ {
		req := llm.Request{
			System:      prompt.EvaluationSystem(),
			User:        user,
			Temperature: e.temperature,
			MaxTokens:   800,
		}
		if attempt > 1 {
			req.User = user + "\n\n" + prompt.RepairMessage(issues)
		}

		resp, err := e.client.Generate(ctx, req)
		if err != nil {
			return Outcome{}, fmt.Errorf("evaluate answer: %w", err)
		}
		out.Attempts = attempt
		out.RawOutput = resp.Text
		out.PromptTokens += resp.PromptTokens
		out.CompletionTokens += resp.CompletionTokens

		result, parseErr := parseResult(resp.Text)
		if parseErr != nil {
			issues = []string{parseErr.Error()}
			continue
		}
		result.TurnID = in.TurnID
		result.Sanitize(in.Answer)
		issues = result.Validate()
		if len(issues) == 0 {
			out.Result = *result
			return out, nil
		}
	}

	out.Degraded = true
	out.ErrorCode = ErrorCodeModelOutputInvalid
	out.Result = evaluation.Result{
		SchemaVersion: evaluation.SchemaVersion,
		TurnID:        in.TurnID,
		Scores:        map[evaluation.Dimension]int{},
		Evidence:      []evaluation.Evidence{},
		Bonus:         []evaluation.Adjustment{},
		Penalty:       []evaluation.Adjustment{},
		Confidence:    0,
	}
	return out, nil
}

func parseResult(raw string) (*evaluation.Result, error) {
	text := strings.TrimSpace(raw)
	if start, end := strings.Index(text, "{"), strings.LastIndex(text, "}"); start >= 0 && end > start {
		text = text[start : end+1]
	}

	var result evaluation.Result
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil, fmt.Errorf("输出不是合法 JSON：%v", err)
	}
	if result.Scores == nil {
		result.Scores = map[evaluation.Dimension]int{}
	}
	return &result, nil
}
