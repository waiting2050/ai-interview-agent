package app

import (
	"context"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
)

// TestRunUsageIsPersistedPerStep proves the run and its step both get the
// model spend, and that configured prices turn tokens into micro-CNY.
func TestRunUsageIsPersistedPerStep(t *testing.T) {
	tracker := obs.NewUsageTracker()
	var sessionID string
	steps := AnswerSteps{
		Evaluate: func(_ context.Context, snap interview.TurnSnapshot) (interview.Outcome, error) {
			tracker.Record(sessionID, "evaluate", 1000, 200)
			return outcomeFor(snap.Answer, 4, false), nil
		},
	}
	svc, st := newTestService(t, Options{
		AnswerSteps:     steps,
		Usage:           tracker,
		PriceInCNYPerM:  2,
		PriceOutCNYPerM: 8,
	})
	sess := createSession(t, svc)
	sessionID = sess.ID
	answer := "我用 PostgreSQL 做唯一事实源，Redis 只做锁和限流。"
	turnID := startAndGetTurn(t, svc, sess)

	res, err := svc.Answer(context.Background(), AnswerCommand{SessionID: sess.ID, TurnID: turnID, Answer: answer})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if res.RunID == "" {
		t.Fatal("answer result has no run id")
	}

	run, err := st.Repos().Runs.Get(context.Background(), res.RunID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if run.PromptTokens != 1000 || run.CompletionTokens != 200 || run.ModelCalls != 1 {
		t.Fatalf("run usage = %d/%d/%d", run.PromptTokens, run.CompletionTokens, run.ModelCalls)
	}
	// 1000*2 + 200*8 = 3600 micro-CNY.
	if run.CostMicros != 3600 {
		t.Fatalf("run cost = %d micro-CNY, want 3600", run.CostMicros)
	}

	step, err := st.Repos().Steps.Get(context.Background(), res.RunID, stepEvaluate)
	if err != nil {
		t.Fatalf("get step: %v", err)
	}
	if step.PromptTokens != 1000 || step.CompletionTokens != 200 || step.ModelCalls != 1 || step.CostMicros != 3600 {
		t.Fatalf("step usage = %d/%d/%d/%d", step.PromptTokens, step.CompletionTokens, step.ModelCalls, step.CostMicros)
	}
}
