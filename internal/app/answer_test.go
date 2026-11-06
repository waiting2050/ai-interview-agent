package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

type stubEvaluator struct {
	outcome interview.Outcome
	err     error
	calls   int
}

func (s *stubEvaluator) Evaluate(context.Context, interview.EvaluateInput) (interview.Outcome, error) {
	s.calls++
	return s.outcome, s.err
}

func outcomeFor(answer string, score int, suggestFollowup bool) interview.Outcome {
	quote := answer
	if r := []rune(answer); len(r) > 12 {
		quote = string(r[:12])
	}
	suggestion := evaluation.FollowupSuggestion{}
	if suggestFollowup {
		suggestion = evaluation.FollowupSuggestion{
			Needed: true,
			Target: "请具体说明锁的选型与压测数据",
			Reason: "缺少工程细节",
		}
	}
	scores := map[evaluation.Dimension]int{}
	for _, d := range evaluation.DimensionOrder {
		scores[d] = score
	}
	return interview.Outcome{
		Result: evaluation.Result{
			SchemaVersion: evaluation.SchemaVersion,
			Scores:        scores,
			Evidence: []evaluation.Evidence{
				{Dimension: evaluation.DimensionCorrectness, Quote: quote, Reason: "回答原文"},
			},
			Confidence: 0.8,
			Followup:   suggestion,
		},
		ModelName:     "stub",
		PromptVersion: "eval-v1",
	}
}

func createSessionWithConfig(t *testing.T, svc *Service, cfg session.Config) *session.Session {
	t.Helper()
	sess, err := svc.CreateSession(context.Background(), CreateSessionInput{
		CandidateName: "张三",
		TargetRole:    "Go 后端开发",
		ResumeText:    "三年 Go 经验",
		JDText:        "熟悉分布式系统",
		Config:        cfg,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

func reload(t *testing.T, svc *Service, id string) *session.Session {
	t.Helper()
	sess, err := svc.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	return sess
}

func startAndGetTurn(t *testing.T, svc *Service, sess *session.Session) string {
	t.Helper()
	if _, err := svc.Start(context.Background(), StartCommand{SessionID: sess.ID}); err != nil {
		t.Fatalf("start: %v", err)
	}
	return reload(t, svc, sess.ID).PendingTurnID
}

func turnsOf(t *testing.T, store *memory.Store, sessionID string) []session.Turn {
	t.Helper()
	turns, err := store.Repos().Turns.ListBySession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	return turns
}

func hasEvent(events []session.Event, eventType string) bool {
	for _, e := range events {
		if e.Type == eventType {
			return true
		}
	}
	return false
}

func TestAnswerHighScoreAdvancesToNextTopic(t *testing.T) {
	answer := "我用本地消息表加定时对账保证最终一致性，重试采用指数退避，压测验证重复消费不会产生脏数据。"
	stub := &stubEvaluator{outcome: outcomeFor(answer, 4, false)}
	svc, store := newTestService(t, Options{Evaluator: stub})
	sess := createSession(t, svc)
	turnID := startAndGetTurn(t, svc, sess)

	res, err := svc.Answer(context.Background(), AnswerCommand{SessionID: sess.ID, TurnID: turnID, Answer: answer})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if res.RunID == "" {
		t.Fatal("answer must return a run id")
	}

	updated := reload(t, svc, sess.ID)
	if updated.PlanCursor != 2 {
		t.Fatalf("plan cursor should advance to 2, got %d", updated.PlanCursor)
	}
	if updated.FollowupsUsed != 0 {
		t.Fatalf("no followup expected, got %d", updated.FollowupsUsed)
	}
	if updated.PendingTurnID == "" || updated.PendingTurnID == turnID {
		t.Fatalf("pending turn should be a new question, got %q", updated.PendingTurnID)
	}

	turns := turnsOf(t, store, sess.ID)
	if len(turns) != 2 || turns[1].Kind != session.TurnKindPrimary || turns[1].Depth != 1 {
		t.Fatalf("expected a new primary turn, got %+v", turns)
	}

	events := listEvents(t, store, sess.ID)
	for _, want := range []string{
		session.EventAnswerReceived, session.EventEvaluationDone, session.EventDecisionMade,
		session.EventQuestionAsked, session.EventSessionPhase,
	} {
		if !hasEvent(events, want) {
			t.Fatalf("missing event %s in %v", want, eventTypes(events))
		}
	}

	// SSE clients render the next question from this event, so the payload
	// must carry a non-empty text and the pending turn id.
	asked := lastEventOfType(events, session.EventQuestionAsked)
	var askedPayload struct {
		TurnID     string `json:"turn_id"`
		Text       string `json:"text"`
		IsFollowup bool   `json:"is_followup"`
	}
	if err := json.Unmarshal(asked.Payload, &askedPayload); err != nil {
		t.Fatalf("decode question.asked payload: %v", err)
	}
	if askedPayload.TurnID != updated.PendingTurnID || askedPayload.TurnID == turnID {
		t.Fatalf("question.asked turn_id = %q, pending = %q", askedPayload.TurnID, updated.PendingTurnID)
	}
	if strings.TrimSpace(askedPayload.Text) == "" {
		t.Fatal("question.asked text must not be empty")
	}
	if askedPayload.IsFollowup {
		t.Fatal("next-topic question must not be marked as followup")
	}

	rec, err := store.Repos().Evaluations.GetByTurn(context.Background(), turnID)
	if err != nil {
		t.Fatalf("evaluation not persisted: %v", err)
	}
	if rec.WeightedScore != 80 || rec.Degraded {
		t.Fatalf("unexpected evaluation record: %+v", rec)
	}
}

func TestAnswerLowScoreFollowsUp(t *testing.T) {
	answer := "我加了个 mutex。"
	stub := &stubEvaluator{outcome: outcomeFor(answer, 2, true)}
	svc, store := newTestService(t, Options{Evaluator: stub})
	sess := createSession(t, svc)
	turnID := startAndGetTurn(t, svc, sess)

	if _, err := svc.Answer(context.Background(), AnswerCommand{SessionID: sess.ID, TurnID: turnID, Answer: answer}); err != nil {
		t.Fatalf("answer: %v", err)
	}

	updated := reload(t, svc, sess.ID)
	if updated.FollowupsUsed != 1 {
		t.Fatalf("followups_used should be 1, got %d", updated.FollowupsUsed)
	}
	if updated.PlanCursor != 1 {
		t.Fatalf("followup must not advance the outline, got cursor %d", updated.PlanCursor)
	}

	turns := turnsOf(t, store, sess.ID)
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}
	next := turns[1]
	if next.Kind != session.TurnKindFollowup || next.Depth != 2 || next.ParentTurnID != turnID {
		t.Fatalf("expected depth-2 followup of %s, got %+v", turnID, next)
	}
}

func TestAnswerFollowupBudgetExhaustedAdvances(t *testing.T) {
	answer := "我只用了一个互斥锁，没有做压测。"
	cfg := session.DefaultConfig()
	cfg.MaxTotalFollowups = 1
	stub := &stubEvaluator{outcome: outcomeFor(answer, 2, true)}
	svc, store := newTestService(t, Options{Evaluator: stub})
	sess := createSessionWithConfig(t, svc, cfg)

	first := startAndGetTurn(t, svc, sess)
	if _, err := svc.Answer(context.Background(), AnswerCommand{SessionID: sess.ID, TurnID: first, Answer: answer}); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	second := reload(t, svc, sess.ID).PendingTurnID
	if second == "" || second == first {
		t.Fatalf("expected a followup question, got %q", second)
	}

	if _, err := svc.Answer(context.Background(), AnswerCommand{SessionID: sess.ID, TurnID: second, Answer: answer}); err != nil {
		t.Fatalf("second answer: %v", err)
	}

	updated := reload(t, svc, sess.ID)
	if updated.FollowupsUsed != 1 {
		t.Fatalf("followups must stay capped at 1, got %d", updated.FollowupsUsed)
	}
	if updated.PlanCursor != 2 {
		t.Fatalf("budget exhaustion should advance the outline, got %d", updated.PlanCursor)
	}

	events := listEvents(t, store, sess.ID)
	lastDecision := lastEventOfType(events, session.EventDecisionMade)
	if lastDecision == nil {
		t.Fatal("decision event missing")
	}
	if string(lastDecision.Payload) == "" || !containsReason(lastDecision.Payload, "followup_budget_exhausted") {
		t.Fatalf("expected followup_budget_exhausted, got %s", lastDecision.Payload)
	}
}

func TestAnswerFinishesWhenPrimaryBudgetExhausted(t *testing.T) {
	answer := "我用 Redis 分布式锁控制库存扣减，失败重试三次。"
	cfg := session.DefaultConfig()
	cfg.MaxPrimaryQuestions = 1
	stub := &stubEvaluator{outcome: outcomeFor(answer, 4, true)}
	svc, store := newTestService(t, Options{Evaluator: stub})
	sess := createSessionWithConfig(t, svc, cfg)
	turnID := startAndGetTurn(t, svc, sess)

	if _, err := svc.Answer(context.Background(), AnswerCommand{SessionID: sess.ID, TurnID: turnID, Answer: answer}); err != nil {
		t.Fatalf("answer: %v", err)
	}

	updated := reload(t, svc, sess.ID)
	if updated.Status != session.StatusActive || updated.Phase != session.PhaseReadyToFinalize {
		t.Fatalf("expected active/ready_to_finalize, got %s/%s", updated.Status, updated.Phase)
	}
	if updated.PendingTurnID != "" {
		t.Fatalf("no pending turn after finish, got %q", updated.PendingTurnID)
	}

	if turns := turnsOf(t, store, sess.ID); len(turns) != 1 {
		t.Fatalf("no new question should be created, got %d turns", len(turns))
	}

	lastDecision := lastEventOfType(listEvents(t, store, sess.ID), session.EventDecisionMade)
	if lastDecision == nil || !containsReason(lastDecision.Payload, "plan_exhausted") {
		t.Fatalf("expected plan_exhausted decision")
	}
}

func TestAnswerDegradesOnInvalidModelOutput(t *testing.T) {
	metrics := obs.NewMetrics()
	svc, store := newTestService(t, Options{
		Metrics:   metrics,
		Evaluator: interview.NewEvaluator(llm.NewEinoClient(llm.Scripted("bad-model", "this is not json"), "bad-model"), "bad-model"),
	})
	sess := createSession(t, svc)
	turnID := startAndGetTurn(t, svc, sess)
	answer := "回答内容"

	if _, err := svc.Answer(context.Background(), AnswerCommand{SessionID: sess.ID, TurnID: turnID, Answer: answer}); err != nil {
		t.Fatalf("answer should degrade, not fail: %v", err)
	}

	events := listEvents(t, store, sess.ID)
	if !hasEvent(events, session.EventEvaluationFailed) {
		t.Fatalf("expected evaluation.degraded event, got %v", eventTypes(events))
	}
	if hasEvent(events, session.EventEvaluationDone) {
		t.Fatal("degraded run must not emit evaluation.completed")
	}

	rec, err := store.Repos().Evaluations.GetByTurn(context.Background(), turnID)
	if err != nil {
		t.Fatalf("degraded record must be persisted: %v", err)
	}
	if !rec.Degraded || len(rec.Scores) != 0 || rec.ErrorCode != interview.ErrorCodeModelOutputInvalid {
		t.Fatalf("unexpected degraded record: %+v", rec)
	}

	if updated := reload(t, svc, sess.ID); updated.PlanCursor != 2 {
		t.Fatalf("degraded evaluation should still advance, got cursor %d", updated.PlanCursor)
	}
	if got := metrics.EvaluationDegradedTotal(interview.ErrorCodeModelOutputInvalid); got != 1 {
		t.Fatalf("evaluation_degraded_total = %d, want 1", got)
	}
}

func TestAnswerIsIdempotent(t *testing.T) {
	answer := "我用分片加本地缓存，热点 key 做二级缓存并设置随机过期。"
	stub := &stubEvaluator{outcome: outcomeFor(answer, 4, false)}
	svc, store := newTestService(t, Options{Evaluator: stub})
	sess := createSession(t, svc)
	turnID := startAndGetTurn(t, svc, sess)

	first, err := svc.Answer(context.Background(), AnswerCommand{SessionID: sess.ID, TurnID: turnID, Answer: answer})
	if err != nil {
		t.Fatalf("first answer: %v", err)
	}
	eventsAfterFirst := len(listEvents(t, store, sess.ID))

	second, err := svc.Answer(context.Background(), AnswerCommand{SessionID: sess.ID, TurnID: turnID, Answer: answer})
	if err != nil {
		t.Fatalf("replayed answer: %v", err)
	}
	if second.RunID != first.RunID || !second.Replayed {
		t.Fatalf("expected replayed run %s, got %+v", first.RunID, second)
	}
	if got := len(listEvents(t, store, sess.ID)); got != eventsAfterFirst {
		t.Fatalf("replay must not append events: %d -> %d", eventsAfterFirst, got)
	}
	if rec, err := store.Repos().Evaluations.GetByTurn(context.Background(), turnID); err != nil || rec.TurnID != turnID {
		t.Fatalf("evaluation should exist once: %+v, %v", rec, err)
	}
}

func TestAnswerRejectsNonPendingTurnAndUnknownSession(t *testing.T) {
	svc, _ := newTestService(t, Options{Evaluator: &stubEvaluator{}})
	sess := createSession(t, svc)
	turnID := startAndGetTurn(t, svc, sess)

	_, err := svc.Answer(context.Background(), AnswerCommand{SessionID: sess.ID, TurnID: "01OTHER", Answer: "x"})
	if !errors.Is(err, ports.ErrStateConflict) {
		t.Fatalf("expected state conflict, got %v", err)
	}

	_, err = svc.Answer(context.Background(), AnswerCommand{SessionID: "01NOSUCH", TurnID: turnID, Answer: "x"})
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

type coverageEvaluator struct {
	calls int
}

func (c *coverageEvaluator) Evaluate(_ context.Context, in interview.EvaluateInput) (interview.Outcome, error) {
	c.calls++
	scores := map[evaluation.Dimension]int{}
	for _, d := range evaluation.DimensionOrder {
		scores[d] = 5
	}
	evidence := make([]evaluation.Evidence, 0, len(in.ExpectedPoints))
	for _, point := range in.ExpectedPoints {
		evidence = append(evidence, evaluation.Evidence{
			Dimension: evaluation.DimensionCorrectness,
			Quote:     "goroutine调度与GMP模型",
			Reason:    "覆盖：" + point,
		})
	}
	return interview.Outcome{
		Result: evaluation.Result{
			SchemaVersion: evaluation.SchemaVersion,
			Scores:        scores,
			Evidence:      evidence,
			Confidence:    0.9,
			// The model still wants a follow-up; coverage must win.
			Followup: evaluation.FollowupSuggestion{
				Needed: true,
				Target: "继续追问",
				Reason: "模型建议追问",
			},
		},
		ModelName:     "coverage-stub",
		PromptVersion: "eval-v1",
	}, nil
}

func TestAnswerCoverageSatisfiedAdvancesOverModelSuggestion(t *testing.T) {
	answer := "goroutine 由 G-M-P 调度，channel 有缓冲与阻塞两种语义，context 负责取消和超时传递。"
	svc, store := newTestService(t, Options{Evaluator: &coverageEvaluator{}})
	sess := createSession(t, svc)
	turnID := startAndGetTurn(t, svc, sess)

	firstTurn := turnsOf(t, store, sess.ID)[0]
	if len(firstTurn.ExpectedPoints) == 0 {
		t.Fatal("first turn must carry syllabus expected points")
	}

	if _, err := svc.Answer(context.Background(), AnswerCommand{SessionID: sess.ID, TurnID: turnID, Answer: answer}); err != nil {
		t.Fatalf("answer: %v", err)
	}

	updated := reload(t, svc, sess.ID)
	if updated.PlanCursor != 2 {
		t.Fatalf("coverage satisfied should advance the outline, cursor=%d", updated.PlanCursor)
	}
	if updated.FollowupsUsed != 0 {
		t.Fatalf("coverage gate must win over model suggestion, followups=%d", updated.FollowupsUsed)
	}

	lastDecision := lastEventOfType(listEvents(t, store, sess.ID), session.EventDecisionMade)
	if lastDecision == nil || !containsReason(lastDecision.Payload, "coverage_satisfied") {
		t.Fatalf("expected coverage_satisfied decision, got %s", lastDecision.Payload)
	}

	turns := turnsOf(t, store, sess.ID)
	next := turns[1]
	if next.Kind != session.TurnKindPrimary || len(next.ExpectedPoints) == 0 {
		t.Fatalf("next question must carry its outline expected points: %+v", next)
	}
	if next.Question == turns[0].Question {
		t.Fatal("next question must not repeat the previous question")
	}
}

func TestCoveredAllPointsNormalizesPunctuation(t *testing.T) {
	evidence := []evaluation.Evidence{
		{Reason: "覆盖：goroutine调度与GMP模型"},
		{Reason: "覆盖：channel 的阻塞与缓冲"},
	}
	if !coveredAllPoints([]string{"goroutine 调度与 G-M-P 模型", "channel 的阻塞与缓冲"}, evidence) {
		t.Fatal("normalized coverage match should succeed")
	}
	if coveredAllPoints([]string{"goroutine 调度与 G-M-P 模型", "context 取消"}, evidence) {
		t.Fatal("missing point must not be reported covered")
	}
}

func lastEventOfType(events []session.Event, eventType string) *session.Event {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == eventType {
			return &events[i]
		}
	}
	return nil
}

func containsReason(payload []byte, reason string) bool {
	return strings.Contains(string(payload), reason)
}
