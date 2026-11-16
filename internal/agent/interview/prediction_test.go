package interview

import (
	"context"
	"errors"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
)

type failingPredictionClient struct{}

func (failingPredictionClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("model down")
}

func heuristicPredictor() *Predictor {
	return NewPredictor(NewQuestionGenerator(NewHeuristicClient(), "fake-heuristic"))
}

func predictionInput(count int) PredictionInput {
	return PredictionInput{
		TargetRole: "Go 后端开发工程师",
		Domain:     "go-backend",
		JDText:     "负责订单履约系统，使用 Go、Redis、MySQL",
		Profile: resume.Profile{
			Years:  3,
			Skills: []string{"Go", "Redis", "MySQL"},
			Projects: []resume.Project{{
				Name:       "订单履约系统",
				Role:       "后端开发",
				TechStack:  []string{"Go", "Redis"},
				Highlights: []string{"缓存热点数据"},
			}},
		},
		Count: count,
	}
}

func TestPredictIsDeterministicAndDeduplicated(t *testing.T) {
	predictor := heuristicPredictor()
	first, err := predictor.Predict(context.Background(), predictionInput(4))
	if err != nil {
		t.Fatal(err)
	}
	second, err := predictor.Predict(context.Background(), predictionInput(4))
	if err != nil {
		t.Fatal(err)
	}

	if first.Domain != "go-backend" {
		t.Fatalf("domain = %q", first.Domain)
	}
	if len(first.Questions) != 4 || len(second.Questions) != 4 {
		t.Fatalf("questions = %d/%d, want 4", len(first.Questions), len(second.Questions))
	}
	seen := map[string]struct{}{}
	for i, question := range first.Questions {
		if question.Topic == "" || question.Question == "" {
			t.Fatalf("question %d is empty: %+v", i, question)
		}
		if _, dup := seen[question.Question]; dup {
			t.Fatalf("duplicate question: %q", question.Question)
		}
		seen[question.Question] = struct{}{}
		if question.Question != second.Questions[i].Question {
			t.Fatalf("prediction is not deterministic at %d:\n%s\n%s",
				i, question.Question, second.Questions[i].Question)
		}
	}
}

func TestPredictClampsCount(t *testing.T) {
	predictor := heuristicPredictor()

	zero, err := predictor.Predict(context.Background(), predictionInput(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(zero.Questions) == 0 || len(zero.Questions) > defaultPredictionCount {
		t.Fatalf("default count = %d", len(zero.Questions))
	}

	many, err := predictor.Predict(context.Background(), predictionInput(99))
	if err != nil {
		t.Fatal(err)
	}
	if len(many.Questions) == 0 || len(many.Questions) > maxPredictionCount {
		t.Fatalf("clamped count = %d", len(many.Questions))
	}
}

func TestPredictDegradesWhenModelFails(t *testing.T) {
	predictor := NewPredictor(NewQuestionGenerator(failingPredictionClient{}, "failing-model"))
	result, err := predictor.Predict(context.Background(), predictionInput(3))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Degraded || !result.ModelCalled {
		t.Fatalf("degraded = %v, model_called = %v", result.Degraded, result.ModelCalled)
	}
	if len(result.Questions) != 3 {
		t.Fatalf("questions = %d", len(result.Questions))
	}
	for i, question := range result.Questions {
		if question.Question == "" || !question.Degraded {
			t.Fatalf("question %d did not degrade cleanly: %+v", i, question)
		}
	}
}
