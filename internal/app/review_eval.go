package app

import (
	"context"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	"github.com/waiting2050/ai-interview-agent/internal/domain/review"
)

// ReviewEvaluator adapts the existing interview evaluation chain to the review
// variant-answer workflow. The injected evaluator already degrades to the
// heuristic model when no real API key is configured, so the review path works
// in both modes without any extra branching here.
type ReviewEvaluator struct {
	evaluator *interview.Evaluator
	objective bool
}

// NewReviewEvaluator wraps the shared evaluator. objective must be true only
// when the underlying client is a real model; the heuristic fallback must be
// reported as length_heuristic so it never drives SM-2.
func NewReviewEvaluator(evaluator *interview.Evaluator, objective bool) *ReviewEvaluator {
	return &ReviewEvaluator{evaluator: evaluator, objective: objective}
}

// EvaluateVariant runs the existing evaluation chain and converts the rubric
// result into a 0-100 objective score.
func (e *ReviewEvaluator) EvaluateVariant(ctx context.Context, in review.VariantEvaluationInput) (review.VariantEvaluation, error) {
	out, err := e.evaluator.Evaluate(ctx, interview.EvaluateInput{
		TurnID:         in.CardID,
		Topic:          in.Topic,
		Question:       in.Question,
		Answer:         in.Answer,
		TargetRole:     "复习复盘",
		Difficulty:     "mid",
		ExpectedPoints: in.ExpectedPoints,
		Rubric:         in.Rubric,
	})
	if err != nil {
		return review.VariantEvaluation{}, err
	}
	result := review.VariantEvaluation{
		Degraded:      out.Degraded,
		ErrorCode:     out.ErrorCode,
		ModelName:     out.ModelName,
		PromptVersion: out.PromptVersion,
		ScoringMode:   review.ScoringModeLengthHeuristic,
	}
	if e.objective {
		result.ScoringMode = review.ScoringModeObjective
	}
	if out.Degraded {
		return result, nil
	}
	score := evaluation.ComputeScoreWith(out.Result.Scores, out.Result.Bonus, out.Result.Penalty, in.Rubric)
	result.Score = score.Total
	result.MissingPoints = append([]string(nil), out.Result.MissingPoints...)
	return result, nil
}
