package tools_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/tools"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

func seed(t *testing.T) (*memory.Store, string) {
	t.Helper()
	store := memory.New()
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

	sess := &session.Session{
		ID:         "sess-a",
		TargetRole: "Go 后端开发",
		Config:     session.Config{Difficulty: "mid", MaxPrimaryQuestions: 8},
		Status:     session.StatusCompleted,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := store.Repos().Sessions.Create(ctx, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	turn := &session.Turn{
		ID:         "turn-a1",
		SessionID:  "sess-a",
		Seq:        1,
		Kind:       session.TurnKindPrimary,
		Topic:      "Go 并发与调度",
		Question:   "说说 GMP 调度模型",
		AnswerText: strings.Repeat("我", 1500),
		Status:     session.TurnStatusAnswered,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := store.Repos().Turns.Create(ctx, turn); err != nil {
		t.Fatalf("create turn: %v", err)
	}
	if err := store.Repos().Evaluations.Upsert(ctx, &evaluation.Record{
		TurnID:        turn.ID,
		SessionID:     sess.ID,
		SchemaVersion: evaluation.SchemaVersion,
		Scores: map[evaluation.Dimension]int{
			evaluation.DimensionCorrectness:         2,
			evaluation.DimensionDepth:               2,
			evaluation.DimensionEngineeringJudgment: 2,
			evaluation.DimensionStructure:           2,
			evaluation.DimensionEvidenceCredibility: 2,
		},
		WeightedScore: 40,
		Confidence:    0.6,
		MissingPoints: []string{"GMP 的本地队列与全局队列"},
		CreatedAt:     now,
	}); err != nil {
		t.Fatalf("upsert evaluation: %v", err)
	}

	other := &session.Session{
		ID:         "sess-b",
		TargetRole: "Java 后端",
		Config:     session.Config{Difficulty: "mid"},
		Status:     session.StatusCompleted,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := store.Repos().Sessions.Create(ctx, other); err != nil {
		t.Fatalf("create other session: %v", err)
	}
	otherTurn := &session.Turn{
		ID:         "turn-b1",
		SessionID:  "sess-b",
		Seq:        1,
		Kind:       session.TurnKindPrimary,
		Topic:      "JVM 内存模型",
		Question:   "说说 JVM 内存区域",
		AnswerText: "堆、栈、方法区",
		Status:     session.TurnStatusAnswered,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := store.Repos().Turns.Create(ctx, otherTurn); err != nil {
		t.Fatalf("create other turn: %v", err)
	}
	return store, sess.ID
}

func TestTurnRejectsCrossSessionRead(t *testing.T) {
	store, sessionID := seed(t)
	q := tools.NewQuery(sessionID, store.Repos(), tools.Options{MaxResultRunes: 600})

	if _, err := q.Turn(context.Background(), "turn-b1"); !errors.Is(err, tools.ErrForbidden) {
		t.Fatalf("cross-session read error = %v, want ErrForbidden", err)
	}
	if _, err := q.Turn(context.Background(), "  "); !errors.Is(err, tools.ErrInvalidArgument) {
		t.Fatalf("empty turn id error = %v, want ErrInvalidArgument", err)
	}

	detail, err := q.Turn(context.Background(), "turn-a1")
	if err != nil {
		t.Fatalf("own turn: %v", err)
	}
	if !detail.Truncated {
		t.Fatalf("long answer should be truncated: %d", len([]rune(detail.Answer)))
	}
	if q.Calls() != 2 {
		t.Fatalf("calls = %d, want 2 (the rejected empty id consumes no call)", q.Calls())
	}
}

func TestSearchTurnsValidatesArguments(t *testing.T) {
	store, sessionID := seed(t)
	q := tools.NewQuery(sessionID, store.Repos(), tools.Options{})

	if _, err := q.SearchTurns(context.Background(), tools.SearchInput{Limit: 9}); !errors.Is(err, tools.ErrInvalidArgument) {
		t.Fatalf("limit error = %v, want ErrInvalidArgument", err)
	}
	min, max := 80.0, 40.0
	if _, err := q.SearchTurns(context.Background(), tools.SearchInput{MinScore: &min, MaxScore: &max}); !errors.Is(err, tools.ErrInvalidArgument) {
		t.Fatalf("score bounds error = %v, want ErrInvalidArgument", err)
	}
	if _, err := q.ListWeakTurns(context.Background(), 20); !errors.Is(err, tools.ErrInvalidArgument) {
		t.Fatalf("weak limit error = %v, want ErrInvalidArgument", err)
	}

	result, err := q.SearchTurns(context.Background(), tools.SearchInput{Topic: "并发", Limit: 4})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(result.Turns) != 1 || result.Turns[0].TurnID != "turn-a1" {
		t.Fatalf("search result = %+v", result)
	}
}

func TestToolBudgets(t *testing.T) {
	store, sessionID := seed(t)
	q := tools.NewQuery(sessionID, store.Repos(), tools.Options{MaxCalls: 2, MaxSectionCalls: 1})
	ctx := context.Background()

	q.BeginSection("evidence")
	if _, err := q.SessionOverview(ctx); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := q.ListWeakTurns(ctx, 5); !errors.Is(err, tools.ErrBudgetExceeded) {
		t.Fatalf("section budget error = %v, want ErrBudgetExceeded", err)
	}

	q2 := tools.NewQuery(sessionID, store.Repos(), tools.Options{MaxCalls: 2})
	if _, err := q2.SessionOverview(ctx); err != nil {
		t.Fatalf("overview: %v", err)
	}
	if _, err := q2.ListWeakTurns(ctx, 5); err != nil {
		t.Fatalf("weak: %v", err)
	}
	if _, err := q2.SearchTurns(ctx, tools.SearchInput{}); !errors.Is(err, tools.ErrBudgetExceeded) {
		t.Fatalf("total budget error = %v, want ErrBudgetExceeded", err)
	}
}

func TestCollectUsesBoundedEvidence(t *testing.T) {
	store, sessionID := seed(t)
	q := tools.NewQuery(sessionID, store.Repos(), tools.Options{MaxResultRunes: 600})

	bundle, err := q.Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if bundle.Calls != 3 {
		t.Fatalf("collect calls = %d, want 3", bundle.Calls)
	}
	if len(bundle.Overview.Turns) != 1 || bundle.Overview.OverallScore != 40 {
		t.Fatalf("overview = %+v", bundle.Overview)
	}
	if len(bundle.WeakTurns) != 1 || !strings.Contains(bundle.WeakTurns[0].AnswerExcerpt, "…") {
		t.Fatalf("weak turns = %+v", bundle.WeakTurns)
	}
	if len(bundle.ToolBreakdown) != 3 {
		t.Fatalf("breakdown = %+v", bundle.ToolBreakdown)
	}
}
