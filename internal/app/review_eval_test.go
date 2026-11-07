package app

import (
	"context"
	"strings"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/domain/domainpack"
	"github.com/waiting2050/ai-interview-agent/internal/domain/review"
)

func TestReviewEvaluatorRunsWithoutAPIKey(t *testing.T) {
	evaluator := NewReviewEvaluator(interview.NewEvaluator(interview.NewHeuristicClient(), "fake-heuristic"), false)
	pack := domainpack.Default().MustGet("go-backend")

	long, err := evaluator.EvaluateVariant(context.Background(), review.VariantEvaluationInput{
		CardID:         "card-1",
		Topic:          "Go 并发模型",
		Question:       "请说明 Go 并发模型",
		Answer:         strings.Repeat("goroutine 调度、channel 阻塞与 context 取消都需要说明取舍。", 8),
		ExpectedPoints: []string{"goroutine 调度与 G-M-P 模型", "channel 阻塞与缓冲区设计"},
		Rubric:         pack.EvaluationRubric(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if long.Degraded {
		t.Fatalf("heuristic mode must not degrade: %+v", long)
	}
	if long.Score < 60 || long.Score > 100 {
		t.Fatalf("long answer score = %v", long.Score)
	}
	if long.ModelName != "fake-heuristic" {
		t.Fatalf("model name = %q", long.ModelName)
	}
	if long.ScoringMode != review.ScoringModeLengthHeuristic {
		t.Fatalf("heuristic scoring mode = %q", long.ScoringMode)
	}

	short, err := evaluator.EvaluateVariant(context.Background(), review.VariantEvaluationInput{
		CardID:   "card-2",
		Topic:    "Go 并发模型",
		Question: "请说明 Go 并发模型",
		Answer:   "不知道",
		Rubric:   pack.EvaluationRubric(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if short.Degraded {
		t.Fatalf("heuristic short answer must not degrade: %+v", short)
	}
	if short.Score >= long.Score {
		t.Fatalf("short score %v must be lower than long score %v", short.Score, long.Score)
	}
}
