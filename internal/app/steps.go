package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/domain/syllabus"
	"github.com/waiting2050/ai-interview-agent/internal/platform/idgen"
)

const (
	stepPlan = "plan"
	stepAsk  = "ask"
)

// StartInput is the deterministic input of the StartRun graph.
type StartInput struct {
	SessionID  string
	TargetRole string
	ResumeText string
	Profile    resume.Profile
	JDText     string
	Config     session.Config
}

// PlanResult is the product of the plan step. It is persisted in run_steps so
// a crash after planning can replay it instead of planning again.
type PlanResult struct {
	Direction string                   `json:"direction"`
	Items     []interview.PlanItemView `json:"items"`
}

// AskResult is the product of the ask step.
type AskResult = interview.Question

// StartSteps makes the two StartRun nodes replaceable in tests.
type StartSteps struct {
	Plan func(ctx context.Context, in StartInput) (PlanResult, error)
	Ask  func(ctx context.Context, in StartInput, plan PlanResult) (AskResult, error)
}

// DefaultStartSteps builds an outline from the built-in syllabus and asks the
// first question with the model-backed generator.
func DefaultStartSteps(generator *interview.QuestionGenerator) StartSteps {
	if generator == nil {
		generator = interview.NewQuestionGenerator(interview.NewHeuristicClient(), "fake-heuristic")
	}
	return StartSteps{
		Plan: planFromSyllabus,
		Ask:  askFirstQuestion(generator),
	}
}

// planFromSyllabus selects and merges built-in outlines based on the target
// role, JD and resume profile. The result is deterministic for the same input.
func planFromSyllabus(_ context.Context, in StartInput) (PlanResult, error) {
	text := strings.Join([]string{
		in.TargetRole,
		in.JDText,
		in.Profile.Text(),
		in.ResumeText,
	}, "\n")
	outline := syllabus.Plan(text, in.Config.Difficulty, in.Config.MaxPrimaryQuestions)
	if len(outline.Items) == 0 {
		return PlanResult{}, fmt.Errorf("syllabus produced no items")
	}

	items := make([]interview.PlanItemView, 0, len(outline.Items))
	for i, item := range outline.Items {
		items = append(items, interview.PlanItemView{
			Seq:            i + 1,
			Topic:          item.Topic,
			Objective:      item.Objective,
			ExpectedPoints: append([]string(nil), item.ExpectedPoints...),
			Difficulty:     item.Difficulty,
		})
	}
	return PlanResult{Direction: outline.Direction, Items: items}, nil
}

func askFirstQuestion(generator *interview.QuestionGenerator) func(context.Context, StartInput, PlanResult) (AskResult, error) {
	return func(ctx context.Context, in StartInput, plan PlanResult) (AskResult, error) {
		if len(plan.Items) == 0 {
			return AskResult{}, fmt.Errorf("plan has no items")
		}
		item := plan.Items[0]
		outcome, err := generator.Generate(ctx, interview.QuestionInput{
			Kind:           "primary",
			TargetRole:     in.TargetRole,
			Difficulty:     in.Config.Difficulty,
			JDText:         in.JDText,
			Profile:        in.Profile,
			Topic:          item.Topic,
			Objective:      item.Objective,
			ExpectedPoints: item.ExpectedPoints,
		})
		if err != nil {
			return AskResult{}, err
		}
		return AskResult{
			TurnID:         idgen.New(),
			Topic:          item.Topic,
			Text:           outcome.Text,
			Depth:          1,
			Objective:      item.Objective,
			ExpectedPoints: item.ExpectedPoints,
		}, nil
	}
}
