package interview

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
)

func validJSON(quote string) string {
	return fmt.Sprintf(`{
		"schema_version": 1,
		"scores": {"correctness": 4, "depth": 4, "engineering_judgment": 3, "structure": 4, "evidence_credibility": 4},
		"evidence": [{"dimension": "correctness", "quote": %q, "reason": "回答给出了具体做法"}],
		"bonus": [],
		"penalty": [],
		"confidence": 0.7,
		"missing_points": [],
		"followup": {"needed": false, "target": "", "reason": ""}
	}`, quote)
}

func evalInput(answer string) EvaluateInput {
	return EvaluateInput{
		SessionID:  "sess-1",
		TurnID:     "turn-1",
		Topic:      "并发与性能",
		Question:   "如何控制并发？",
		Answer:     answer,
		TargetRole: "Go 后端",
		Difficulty: "middle",
	}
}

func TestEvaluateValidJSONOnFirstTry(t *testing.T) {
	const answer = "用 channel 控制并发，压测 QPS 从 800 提升到 1500。"
	ev := NewEvaluator(llm.NewEinoClient(llm.Scripted("fake", validJSON(answer)), "fake"), "fake")

	out, err := ev.Evaluate(context.Background(), evalInput(answer))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if out.Degraded || out.Attempts != 1 {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	if out.Result.TurnID != "turn-1" {
		t.Fatalf("turn_id must be authoritative, got %q", out.Result.TurnID)
	}
	if out.ModelName != "fake" || out.PromptVersion == "" {
		t.Fatalf("model metadata missing: %+v", out)
	}
}

func TestEvaluateRepairsInvalidJSONOnce(t *testing.T) {
	const answer = "用分段锁降低锁粒度，读多写少场景把读路径改为无锁缓存。"
	ev := NewEvaluator(llm.NewEinoClient(llm.Scripted("fake", "这是一段解释，不是 JSON", validJSON(answer)), "fake"), "fake")

	out, err := ev.Evaluate(context.Background(), evalInput(answer))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if out.Degraded {
		t.Fatalf("repair should have recovered: %+v", out)
	}
	if out.Attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", out.Attempts)
	}
}

func TestEvaluateDegradesAfterRepairBudget(t *testing.T) {
	ev := NewEvaluator(llm.NewEinoClient(llm.Scripted("fake", "not json at all"), "fake"), "fake")

	out, err := ev.Evaluate(context.Background(), evalInput("随便的回答"))
	if err != nil {
		t.Fatalf("degrade must not be a transport error: %v", err)
	}
	if !out.Degraded {
		t.Fatal("expected degraded outcome")
	}
	if out.ErrorCode != ErrorCodeModelOutputInvalid {
		t.Fatalf("unexpected error code %q", out.ErrorCode)
	}
	if out.Attempts != 3 {
		t.Fatalf("expected 1 attempt + 2 repairs, got %d", out.Attempts)
	}
	if len(out.Result.Scores) != 0 {
		t.Fatalf("degraded result must not fabricate scores: %+v", out.Result)
	}
}

func TestEvaluateDropsEvidenceNotInAnswer(t *testing.T) {
	ev := NewEvaluator(llm.NewEinoClient(llm.Scripted("fake", validJSON("我用了 Kafka 做削峰")), "fake"), "fake")

	out, err := ev.Evaluate(context.Background(), evalInput("我用 channel 做并发控制"))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !out.Degraded {
		t.Fatalf("all evidence was invented, expected degradation: %+v", out)
	}
}

func TestEvaluateReturnsTransportError(t *testing.T) {
	model := llm.NewFakeChatModel("fake", func(context.Context, llm.Request) (string, error) {
		return "", errors.New("upstream timeout")
	})
	ev := NewEvaluator(llm.NewEinoClient(model, "fake"), "fake")

	_, err := ev.Evaluate(context.Background(), evalInput("answer"))
	if err == nil || !strings.Contains(err.Error(), "upstream timeout") {
		t.Fatalf("expected transport error, got %v", err)
	}
}

func TestHeuristicModelProducesValidEvaluation(t *testing.T) {
	answer := "我在支付系统里用本地消息表保证最终一致性，失败重试按指数退避，补偿任务每天对账。"
	out, err := NewEvaluator(NewHeuristicClient(), "fake-heuristic").Evaluate(context.Background(), evalInput(answer))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if out.Degraded {
		t.Fatalf("heuristic model should produce valid JSON: %+v", out)
	}
	if len(out.Result.Evidence) == 0 {
		t.Fatal("heuristic model must cite the answer")
	}
	if out.Result.Evidence[0].Dimension != evaluation.DimensionCorrectness {
		t.Fatalf("unexpected evidence dimension: %+v", out.Result.Evidence[0])
	}
}

// Uncovered expected points must stay in missing_points. If they are also cited
// as evidence, the coverage gate treats them as covered and the follow-up never
// fires.
func TestHeuristicUncoveredPointsSuggestFollowupWithoutClaimingCoverage(t *testing.T) {
	in := evalInput("用定时任务扫表补发消息。")
	in.ExpectedPoints = []string{"幂等键", "本地消息表"}
	out, err := NewEvaluator(NewHeuristicClient(), "fake-heuristic").Evaluate(context.Background(), in)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !out.Result.Followup.Needed {
		t.Fatalf("expected a follow-up suggestion, got %+v", out.Result.Followup)
	}
	if len(out.Result.MissingPoints) == 0 {
		t.Fatal("uncovered expected points must be reported as missing")
	}
	for _, e := range out.Result.Evidence {
		if strings.Contains(e.Reason, "覆盖：") {
			t.Fatalf("uncovered point must not be recorded as covered: %+v", e)
		}
	}
}
