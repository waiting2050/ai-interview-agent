package followup

import (
	"testing"
	"time"
)

func baseInput() Input {
	return Input{
		Elapsed:                 0,
		MaxElapsed:              60 * time.Minute,
		PlanCursor:              1,
		RemainingPrimaries:      3,
		Depth:                   1,
		MaxDepth:                3,
		FollowupsThisQuestion:   0,
		MaxFollowupsPerQuestion: 2,
		FollowupsUsed:           0,
		MaxTotalFollowups:       6,
		SuggestFollowup:         true,
		SuggestTarget:           "为什么选择这种锁",
		SuggestionHasEvidence:   true,
	}
}

func TestDecideTable(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Input)
		action Action
		reason string
	}{
		{"time limit wins", func(in *Input) { in.Elapsed = in.MaxElapsed }, ActionFinish, ReasonTimeLimit},
		{"plan exhausted", func(in *Input) { in.RemainingPrimaries = 0 }, ActionFinish, ReasonPlanExhausted},
		{"total followup budget", func(in *Input) { in.FollowupsUsed = 6 }, ActionNextQuestion, ReasonFollowupBudget},
		{"depth limit", func(in *Input) { in.Depth = 3 }, ActionNextQuestion, ReasonDepthLimit},
		{"per-question limit", func(in *Input) { in.FollowupsThisQuestion = 2 }, ActionNextQuestion, ReasonQuestionFollowupLimit},
		{"unknown answer", func(in *Input) { in.AnswerUnknown = true }, ActionNextQuestion, ReasonAnswerUnknown},
		{"degraded evaluation", func(in *Input) { in.EvaluationDegraded = true }, ActionNextQuestion, ReasonEvaluationDegraded},
		{"coverage satisfied", func(in *Input) { in.CoveredAllPoints = true; in.SuggestFollowup = false }, ActionNextQuestion, ReasonCoverageSatisfied},
		{"coverage satisfied wins over model followup", func(in *Input) {
			in.CoveredAllPoints = true
		}, ActionNextQuestion, ReasonCoverageSatisfied},
		{"model suggestion with evidence", func(in *Input) {}, ActionFollowUp, ReasonModelSuggestion},
		{"model suggestion without evidence", func(in *Input) { in.SuggestionHasEvidence = false }, ActionNextQuestion, ReasonNoFollowupSignal},
		{"model suggestion without target", func(in *Input) { in.SuggestTarget = "  " }, ActionNextQuestion, ReasonNoFollowupSignal},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput()
			tc.mutate(&in)
			got := Decide(in)
			if got.Action != tc.action || got.Reason != tc.reason {
				t.Fatalf("got %s/%s, want %s/%s", got.Action, got.Reason, tc.action, tc.reason)
			}
		})
	}
}

func TestDecideNextQuestionDegradesToFinish(t *testing.T) {
	in := baseInput()
	in.RemainingPrimaries = 0
	in.FollowupsUsed = in.MaxTotalFollowups
	if got := Decide(in); got.Action != ActionFinish || got.Reason != ReasonPlanExhausted {
		t.Fatalf("got %+v, want finish/plan_exhausted", got)
	}
}

func TestFollowupTargetCarriedThrough(t *testing.T) {
	got := Decide(baseInput())
	if got.Target != "为什么选择这种锁" {
		t.Fatalf("target not carried through: %+v", got)
	}
}

func TestIsUnknownAnswer(t *testing.T) {
	unknown := []string{"不会", "这个我不知道", "没做过类似项目", "不太清楚，没接触过"}
	for _, answer := range unknown {
		if !IsUnknownAnswer(answer) {
			t.Fatalf("%q should be unknown", answer)
		}
	}

	long := "不会这个词只是引用，我实际做过一个高并发网关，负责连接池、超时重试与熔断降级，" +
		"线上把 P99 从 420ms 降到 180ms，也做过压测和容量评估，具体细节可以展开讲。" +
		"那套网关承载了日均两亿次调用，我用令牌桶做入口限流，用自适应并发控制保护下游，" +
		"同时把链路追踪接入 OpenTelemetry，按租户维度暴露 RED 指标并配置分级告警，" +
		"故障演练每季度一次，Chaos Mesh 注入网络分区和慢依赖，回滚剧本演练到分钟级。"
	if IsUnknownAnswer(long) {
		t.Fatal("long answer containing a marker must not be treated as unknown")
	}
}
