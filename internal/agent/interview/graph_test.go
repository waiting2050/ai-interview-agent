package interview

import (
	"context"
	"errors"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/domain/followup"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
)

func testSnapshot(cursor int, items []PlanItemView) TurnSnapshot {
	return TurnSnapshot{
		SessionID:      "session-1",
		TurnID:         "turn-1",
		Topic:          "Go 并发模型",
		Question:       "请说明 goroutine 调度",
		ExpectedPoints: []string{"goroutine 调度与 G-M-P 模型"},
		Depth:          1,
		PlanCursor:     cursor,
		PlanItems:      items,
		Config: session.Config{
			DurationMinutes:         30,
			MaxDepth:                3,
			MaxFollowupsPerQuestion: 2,
			MaxTotalFollowups:       6,
		},
	}
}

func TestAnswerGraphCompiles(t *testing.T) {
	graph := NewAnswerGraph(context.Background())
	if err := graph.Err(); err != nil {
		t.Fatalf("compile graph: %v", err)
	}
}

func TestAnswerGraphRoutesDecisionBranches(t *testing.T) {
	items := []PlanItemView{{Seq: 1, Topic: "Go 并发模型"}, {Seq: 2, Topic: "内存管理与 GC"}}

	tests := []struct {
		name         string
		cursor       int
		action       followup.Action
		wantAsked    bool
		wantFollowup bool
		wantNext     bool
		wantStep     string
		wantErr      bool
	}{
		{name: "follow up", cursor: 1, action: followup.ActionFollowUp, wantAsked: true, wantFollowup: true},
		{name: "next question", cursor: 1, action: followup.ActionNextQuestion, wantAsked: true, wantNext: true},
		{name: "finish", cursor: 2, action: followup.ActionFinish, wantAsked: false},
		{name: "next without plan item", cursor: 2, action: followup.ActionNextQuestion, wantErr: true, wantStep: nodeAskNext},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var followupCalls, nextCalls int
			graph := NewAnswerGraph(context.Background())
			if err := graph.Err(); err != nil {
				t.Fatal(err)
			}

			state, err := graph.Run(context.Background(), TurnInput{
				SessionID: "session-1",
				TurnID:    "turn-1",
				Answer:    "answer",
			}, AnswerDeps{
				Snapshot: func(context.Context) (TurnSnapshot, error) {
					return testSnapshot(tt.cursor, items), nil
				},
				Evaluate: func(context.Context, TurnSnapshot) (Outcome, error) {
					return Outcome{}, nil
				},
				Decide: func(context.Context, TurnSnapshot, Outcome) (followup.Decision, error) {
					return followup.Decision{Action: tt.action, Reason: "test"}, nil
				},
				AskFollowup: func(context.Context, TurnSnapshot, Outcome, followup.Decision) (Question, error) {
					followupCalls++
					return Question{TurnID: "turn-followup", Text: "followup question", Depth: 2}, nil
				},
				AskNext: func(_ context.Context, _ TurnSnapshot, item PlanItemView) (Question, error) {
					nextCalls++
					return Question{TurnID: "turn-next", Topic: item.Topic, Text: "next question", Depth: 1}, nil
				},
			})

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				if got := StepName(err); got != tt.wantStep {
					t.Fatalf("step = %q, want %q", got, tt.wantStep)
				}
				return
			}
			if err != nil {
				t.Fatalf("run graph: %v", err)
			}
			if state.Asked != tt.wantAsked {
				t.Fatalf("asked = %v, want %v", state.Asked, tt.wantAsked)
			}
			if tt.wantFollowup != (followupCalls == 1) || tt.wantNext != (nextCalls == 1) {
				t.Fatalf("followup calls=%d next calls=%d", followupCalls, nextCalls)
			}
			if tt.action == followup.ActionFinish && state.Question.TurnID != "" {
				t.Fatalf("finish should not ask, got %+v", state.Question)
			}
		})
	}
}

func TestAnswerGraphDecideErrorMarksStep(t *testing.T) {
	graph := NewAnswerGraph(context.Background())
	boom := errors.New("boom")

	_, err := graph.Run(context.Background(), TurnInput{SessionID: "s", TurnID: "t"}, AnswerDeps{
		Snapshot: func(context.Context) (TurnSnapshot, error) { return testSnapshot(1, nil), nil },
		Evaluate: func(context.Context, TurnSnapshot) (Outcome, error) { return Outcome{}, nil },
		Decide: func(context.Context, TurnSnapshot, Outcome) (followup.Decision, error) {
			return followup.Decision{}, boom
		},
	})
	if err == nil {
		t.Fatal("expected decide error")
	}
	if StepName(err) != nodeDecide {
		t.Fatalf("step = %q", StepName(err))
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error does not wrap cause: %v", err)
	}
}
