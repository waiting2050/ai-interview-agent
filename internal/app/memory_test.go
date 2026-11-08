package app

import (
	"context"
	"errors"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	agentmemory "github.com/waiting2050/ai-interview-agent/internal/agent/memory"
	"github.com/waiting2050/ai-interview-agent/internal/platform/idgen"
)

type fakeMemoryStore struct {
	state   agentmemory.State
	ok      bool
	loadErr error
	saveErr error
}

func (f *fakeMemoryStore) Load(context.Context, string) (agentmemory.State, bool, error) {
	if f.loadErr != nil {
		return agentmemory.State{}, false, f.loadErr
	}
	return f.state, f.ok, nil
}

func (f *fakeMemoryStore) Save(context.Context, string, agentmemory.State) error {
	return f.saveErr
}

// TestAnswerSnapshotCarriesLayeredMemory proves the main chain injects both
// layers: the persisted summary and the last N answered turns verbatim.
func TestAnswerSnapshotCarriesLayeredMemory(t *testing.T) {
	fake := &fakeMemoryStore{
		ok:    true,
		state: agentmemory.State{Summary: "第 2 轮：数据库选 PostgreSQL，因为需要 SKIP LOCKED 的队列语义。", UptoSeq: 2},
	}
	builder := agentmemory.NewBuilder(fake, nil, agentmemory.Options{Recent: 4, Every: 5})

	var snapshots []interview.TurnSnapshot
	steps := AnswerSteps{
		Evaluate: func(_ context.Context, snap interview.TurnSnapshot) (interview.Outcome, error) {
			snapshots = append(snapshots, snap)
			return outcomeFor(snap.Answer, 4, false), nil
		},
		AskNext: func(_ context.Context, _ interview.TurnSnapshot, item interview.PlanItemView) (interview.Question, error) {
			return interview.Question{TurnID: idgen.New(), Topic: item.Topic, Text: "请继续下一题", Depth: 1}, nil
		},
	}
	svc, _ := newTestService(t, Options{AnswerSteps: steps, Memory: builder})
	sess := createSession(t, svc)

	first := "第一题：我用 PostgreSQL 而不是 MySQL，因为需要 SKIP LOCKED 做任务队列。"
	turnID := startAndGetTurn(t, svc, sess)
	if _, err := svc.Answer(context.Background(), AnswerCommand{SessionID: sess.ID, TurnID: turnID, Answer: first}); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	second := "第二题：我用 Redis 做缓存，热点数据加本地 LRU。"
	next := reload(t, svc, sess.ID).PendingTurnID
	if _, err := svc.Answer(context.Background(), AnswerCommand{SessionID: sess.ID, TurnID: next, Answer: second}); err != nil {
		t.Fatalf("second answer: %v", err)
	}

	if len(snapshots) != 2 {
		t.Fatalf("expected two snapshots, got %d", len(snapshots))
	}
	if snapshots[0].MemorySummary == "" || len(snapshots[0].MemoryRecentTurns) != 0 {
		t.Fatalf("first snapshot memory = %+v", snapshots[0])
	}
	if snapshots[1].MemorySummary == "" || len(snapshots[1].MemoryRecentTurns) != 1 {
		t.Fatalf("second snapshot recent = %+v", snapshots[1].MemoryRecentTurns)
	}
	if snapshots[1].MemoryRecentTurns[0] == "" {
		t.Fatal("recent turn must be rendered as question plus answer")
	}
}

// TestMemoryFailureDoesNotFailAnswerRun locks the "summary failure must not
// block the main flow" requirement at the app boundary.
func TestMemoryFailureDoesNotFailAnswerRun(t *testing.T) {
	fake := &fakeMemoryStore{loadErr: errors.New("store unavailable"), saveErr: errors.New("store unavailable")}
	builder := agentmemory.NewBuilder(fake, nil, agentmemory.Options{Recent: 4, Every: 5})

	answer := "我用本地消息表加定时对账保证最终一致性，重试采用指数退避。"
	svc, _ := newTestService(t, Options{Evaluator: &stubEvaluator{outcome: outcomeFor(answer, 4, false)}, Memory: builder})
	sess := createSession(t, svc)
	turnID := startAndGetTurn(t, svc, sess)

	if _, err := svc.Answer(context.Background(), AnswerCommand{SessionID: sess.ID, TurnID: turnID, Answer: answer}); err != nil {
		t.Fatalf("answer must succeed when memory store fails: %v", err)
	}
	if reload(t, svc, sess.ID).PendingTurnID == "" {
		t.Fatal("interview must continue after a memory failure")
	}
}
