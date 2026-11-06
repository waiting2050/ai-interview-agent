package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

var fixedNow = time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

func newTestService(t *testing.T, opts Options) (*Service, *memory.Store) {
	t.Helper()
	store := memory.New()
	opts.Synchronous = true
	if opts.Now == nil {
		opts.Now = func() time.Time { return fixedNow }
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return New(store, opts), store
}

func createSession(t *testing.T, svc *Service) *session.Session {
	t.Helper()
	sess, err := svc.CreateSession(context.Background(), CreateSessionInput{
		CandidateName: "张三",
		TargetRole:    "Go 后端开发",
		ResumeText:    "三年 Go 经验",
		JDText:        "熟悉分布式系统",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sess
}

func listEvents(t *testing.T, store *memory.Store, sessionID string) []session.Event {
	t.Helper()
	events, err := store.Repos().Events.ListAfter(context.Background(), sessionID, 0, 100)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	return events
}

func eventTypes(events []session.Event) []string {
	types := make([]string, 0, len(events))
	for _, e := range events {
		types = append(types, e.Type)
	}
	return types
}

func TestStartMinimalChainAndIdempotentReplay(t *testing.T) {
	svc, store := newTestService(t, Options{})
	sess := createSession(t, svc)

	result, err := svc.Start(context.Background(), StartCommand{
		SessionID:      sess.ID,
		IdempotencyKey: "start-1",
		RequestBody:    []byte(`{"action":"start"}`),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if result.RunID == "" || result.Replayed {
		t.Fatalf("unexpected result: %+v", result)
	}

	got, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != session.StatusActive || got.Phase != session.PhaseAwaitingAnswer {
		t.Fatalf("session = %s/%s", got.Status, got.Phase)
	}
	if got.StateVersion != 1 {
		t.Fatalf("state_version = %d", got.StateVersion)
	}

	events := listEvents(t, store, sess.ID)
	wantTypes := []string{
		session.EventSessionCreated,
		session.EventRunStarted,
		session.EventSessionStarted,
		session.EventQuestionAsked,
		session.EventSessionPhase,
	}
	if diff := compareTypes(eventTypes(events), wantTypes); diff != "" {
		t.Fatalf("event order: %s", diff)
	}

	var question struct {
		TurnID string `json:"turn_id"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(events[3].Payload, &question); err != nil {
		t.Fatalf("decode question payload: %v", err)
	}
	if question.TurnID == "" || question.Text == "" {
		t.Fatalf("question payload = %+v", question)
	}
	if got.PendingTurnID != question.TurnID {
		t.Fatalf("pending_turn_id = %q, want %q", got.PendingTurnID, question.TurnID)
	}

	replay, err := svc.Start(context.Background(), StartCommand{
		SessionID:      sess.ID,
		IdempotencyKey: "start-1",
		RequestBody:    []byte(`{"action":"start"}`),
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed || replay.RunID != result.RunID {
		t.Fatalf("replay = %+v, original run = %s", replay, result.RunID)
	}

	after := listEvents(t, store, sess.ID)
	if len(after) != len(events) {
		t.Fatalf("replay appended events: %d -> %d", len(events), len(after))
	}

	// A different key on an already started session is a state conflict.
	if _, err := svc.Start(context.Background(), StartCommand{
		SessionID:      sess.ID,
		IdempotencyKey: "start-2",
	}); !errors.Is(err, ports.ErrStateConflict) {
		t.Fatalf("different key error = %v, want state conflict", err)
	}
}

func TestIdempotencyKeyReusedForDifferentBody(t *testing.T) {
	svc, _ := newTestService(t, Options{})
	sess := createSession(t, svc)

	if _, err := svc.Start(context.Background(), StartCommand{
		SessionID:      sess.ID,
		IdempotencyKey: "same-key",
		RequestBody:    []byte(`{"n":1}`),
	}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Start(context.Background(), StartCommand{
		SessionID:      sess.ID,
		IdempotencyKey: "same-key",
		RequestBody:    []byte(`{"n":2}`),
	})
	if !errors.Is(err, ErrIdempotencyReused) {
		t.Fatalf("error = %v, want idempotency reuse", err)
	}
}

func TestRecoveryResumesFromLastSuccessfulStep(t *testing.T) {
	planCalls, askCalls := 0, 0
	steps := StartSteps{
		Plan: func(context.Context, StartInput) (PlanResult, error) {
			planCalls++
			return PlanResult{
				Items: []interview.PlanItemView{
					{Seq: 1, Topic: "fresh-topic", ExpectedPoints: []string{"point-a"}},
				},
			}, nil
		},
		Ask: func(_ context.Context, _ StartInput, plan PlanResult) (AskResult, error) {
			askCalls++
			return AskResult{
				TurnID:         "turn-recovered",
				Topic:          plan.Items[0].Topic,
				Text:           "fresh question from model",
				Depth:          1,
				ExpectedPoints: plan.Items[0].ExpectedPoints,
			}, nil
		},
	}

	svc, store := newTestService(t, Options{
		RunStaleAfter: time.Minute,
		Steps:         steps,
	})
	sess := createSession(t, svc)

	runTime := fixedNow.Add(-3 * time.Minute)
	run := &session.Run{
		ID:             "run-crashed",
		SessionID:      sess.ID,
		Kind:           session.RunKindStart,
		Status:         session.RunStatusRunning,
		IdempotencyKey: "crash-key",
		RequestHash:    "hash",
		Attempt:        1,
		LastStep:       stepPlan,
		CreatedAt:      runTime,
		UpdatedAt:      runTime.Add(time.Minute),
	}
	if _, _, err := store.Repos().Runs.Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	cachedPlan := PlanResult{
		Items: []interview.PlanItemView{
			{Seq: 1, Topic: "cached-topic", ExpectedPoints: []string{"point-b"}},
		},
	}
	body, err := json.Marshal(cachedPlan)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Repos().Steps.Start(context.Background(), &session.RunStep{
		RunID:     run.ID,
		StepName:  stepPlan,
		Status:    session.StepStatusRunning,
		Attempt:   1,
		StartedAt: runTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Repos().Steps.Complete(context.Background(), run.ID, stepPlan, body, runTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.Repos().Steps.Start(context.Background(), &session.RunStep{
		RunID:     run.ID,
		StepName:  stepAsk,
		Status:    session.StepStatusRunning,
		Attempt:   1,
		StartedAt: runTime.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	resumed, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if resumed != 1 {
		t.Fatalf("resumed = %d", resumed)
	}
	if planCalls != 0 {
		t.Fatalf("plan re-executed %d times, want replay from run_steps", planCalls)
	}
	if askCalls != 1 {
		t.Fatalf("ask calls = %d, want 1", askCalls)
	}

	got, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != session.StatusActive || got.PendingTurnID != "turn-recovered" {
		t.Fatalf("session = %s/%s pending=%s", got.Status, got.Phase, got.PendingTurnID)
	}

	recoveredRun, err := store.Repos().Runs.Get(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredRun.Status != session.RunStatusSucceeded {
		t.Fatalf("run status = %s", recoveredRun.Status)
	}

	planStep, err := store.Repos().Steps.Get(context.Background(), run.ID, stepPlan)
	if err != nil {
		t.Fatal(err)
	}
	if planStep.Attempt != 1 {
		t.Fatalf("plan attempt = %d, want 1 (replayed)", planStep.Attempt)
	}
	askStep, err := store.Repos().Steps.Get(context.Background(), run.ID, stepAsk)
	if err != nil {
		t.Fatal(err)
	}
	if askStep.Attempt != 2 {
		t.Fatalf("ask attempt = %d, want 2", askStep.Attempt)
	}

	events := listEvents(t, store, sess.ID)
	if diff := compareTypes(eventTypes(events), []string{
		session.EventSessionCreated,
		session.EventRunInterrupted,
		session.EventSessionStarted,
		session.EventQuestionAsked,
		session.EventSessionPhase,
	}); diff != "" {
		t.Fatalf("event order: %s", diff)
	}
}

func TestAbortIsIdempotent(t *testing.T) {
	svc, store := newTestService(t, Options{})
	sess := createSession(t, svc)

	if err := svc.Abort(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	events := listEvents(t, store, sess.ID)
	if diff := compareTypes(eventTypes(events), []string{
		session.EventSessionCreated,
		session.EventSessionAborted,
		session.EventSessionPhase,
	}); diff != "" {
		t.Fatalf("event order: %s", diff)
	}

	if err := svc.Abort(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	if got := len(listEvents(t, store, sess.ID)); got != len(events) {
		t.Fatalf("second abort appended events: %d -> %d", len(events), got)
	}
}

func compareTypes(got, want []string) string {
	if len(got) != len(want) {
		return "length mismatch"
	}
	for i := range want {
		if got[i] != want[i] {
			return "index " + want[i] + " != " + got[i]
		}
	}
	return ""
}
