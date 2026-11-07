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

func TestDecideWeakTopicGrantsOneBonusFollowup(t *testing.T) {
	in := baseInput()
	in.SuggestFollowup = false
	in.SuggestionHasEvidence = false
	in.History = &TopicHistory{Topic: "Go 并发", Answered: 2, AvgScore: 32.5, LowStreak: 2}

	got := Decide(in)
	if got.Action != ActionFollowUp || got.Reason != ReasonWeakTopicBonus {
		t.Fatalf("weak topic should grant one follow-up, got %s/%s", got.Action, got.Reason)
	}

	// The granted follow-up consumes the topic's one bonus: the next decision
	// has no model signal and must fall back to advancing.
	in.FollowupsThisQuestion = 1
	in.History.LowStreak = 3
	got = Decide(in)
	if got.Action != ActionNextQuestion || got.Reason != ReasonNoFollowupSignal {
		t.Fatalf("weak bonus must be granted only once, got %s/%s", got.Action, got.Reason)
	}

	// The per-question cap is still a hard limit even if the bonus somehow
	// qualified again.
	in.FollowupsThisQuestion = in.MaxFollowupsPerQuestion
	got = Decide(in)
	if got.Action != ActionNextQuestion || got.Reason != ReasonQuestionFollowupLimit {
		t.Fatalf("weak bonus must not exceed the per-question cap, got %s/%s", got.Action, got.Reason)
	}
}

func TestDecideWeakTopicNeedsConsecutiveLowScores(t *testing.T) {
	in := baseInput()
	in.SuggestFollowup = false
	in.History = &TopicHistory{Answered: 1, LowStreak: 1}
	if got := Decide(in); got.Action != ActionNextQuestion || got.Reason != ReasonNoFollowupSignal {
		t.Fatalf("one low answer is not a streak, got %s/%s", got.Action, got.Reason)
	}

	in.History.LowStreak = WeakStreakThreshold
	if got := Decide(in); got.Action != ActionFollowUp || got.Reason != ReasonWeakTopicBonus {
		t.Fatalf("weak streak at threshold should grant a bonus, got %s/%s", got.Action, got.Reason)
	}
}

func TestDecideStrongTopicSkipsFollowup(t *testing.T) {
	in := baseInput()
	in.History = &TopicHistory{Answered: 3, AvgScore: 88, HighStreak: 2}

	got := Decide(in)
	if got.Action != ActionNextQuestion || got.Reason != ReasonStrongTopicSkip {
		t.Fatalf("strong topic should advance, got %s/%s", got.Action, got.Reason)
	}

	in.History.HighStreak = StrongStreakThreshold - 1
	got = Decide(in)
	if got.Action != ActionFollowUp || got.Reason != ReasonModelSuggestion {
		t.Fatalf("below the strong threshold the suggestion must stand, got %s/%s", got.Action, got.Reason)
	}
}

func TestDecideAdaptiveRulesCannotBreakHardGates(t *testing.T) {
	weak := &TopicHistory{Answered: 3, LowStreak: 3, AvgScore: 30}
	strong := &TopicHistory{Answered: 3, HighStreak: 3, AvgScore: 90}

	cases := []struct {
		name    string
		history *TopicHistory
		mutate  func(*Input)
		action  Action
		reason  string
	}{
		{"weak cannot override time limit", weak, func(in *Input) { in.Elapsed = in.MaxElapsed }, ActionFinish, ReasonTimeLimit},
		{"strong cannot override time limit", strong, func(in *Input) { in.Elapsed = in.MaxElapsed }, ActionFinish, ReasonTimeLimit},
		{"weak cannot override exhausted outline", weak, func(in *Input) { in.RemainingPrimaries = 0 }, ActionFinish, ReasonPlanExhausted},
		{"weak cannot override total budget", weak, func(in *Input) { in.FollowupsUsed = in.MaxTotalFollowups }, ActionNextQuestion, ReasonFollowupBudget},
		{"weak cannot override depth limit", weak, func(in *Input) { in.Depth = in.MaxDepth }, ActionNextQuestion, ReasonDepthLimit},
		{"weak cannot override per-question limit", weak, func(in *Input) { in.FollowupsThisQuestion = in.MaxFollowupsPerQuestion }, ActionNextQuestion, ReasonQuestionFollowupLimit},
		{"weak cannot override unknown answer", weak, func(in *Input) { in.AnswerUnknown = true }, ActionNextQuestion, ReasonAnswerUnknown},
		{"weak cannot override degraded evaluation", weak, func(in *Input) { in.EvaluationDegraded = true }, ActionNextQuestion, ReasonEvaluationDegraded},
		{"weak cannot override coverage", weak, func(in *Input) { in.CoveredAllPoints = true }, ActionNextQuestion, ReasonCoverageSatisfied},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput()
			in.History = tc.history
			// Remove the model signal so only the adaptive rule could change
			// the outcome.
			in.SuggestFollowup = false
			in.SuggestionHasEvidence = false
			tc.mutate(&in)
			got := Decide(in)
			if got.Action != tc.action || got.Reason != tc.reason {
				t.Fatalf("got %s/%s, want %s/%s", got.Action, got.Reason, tc.action, tc.reason)
			}
		})
	}
}

func TestDecideNoHistoryKeepsLegacyBehaviour(t *testing.T) {
	for _, history := range []*TopicHistory{nil, {}} {
		in := baseInput()
		in.History = history
		got := Decide(in)
		if got.Action != ActionFollowUp || got.Reason != ReasonModelSuggestion {
			t.Fatalf("history %+v changed the default decision: %s/%s", history, got.Action, got.Reason)
		}

		in.SuggestFollowup = false
		in.SuggestionHasEvidence = false
		got = Decide(in)
		if got.Action != ActionNextQuestion || got.Reason != ReasonNoFollowupSignal {
			t.Fatalf("history %+v changed the fallback decision: %s/%s", history, got.Action, got.Reason)
		}
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
