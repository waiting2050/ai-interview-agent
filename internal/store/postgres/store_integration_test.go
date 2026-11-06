//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	domainreport "github.com/waiting2050/ai-interview-agent/internal/domain/report"
	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	platformlock "github.com/waiting2050/ai-interview-agent/internal/platform/lock"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
	"github.com/waiting2050/ai-interview-agent/internal/store/postgres"
	storeredis "github.com/waiting2050/ai-interview-agent/internal/store/redis"
	"github.com/waiting2050/ai-interview-agent/migrations"
)

func openStore(t *testing.T) *postgres.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set; integration tests need PostgreSQL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(store.Close)

	if err := migrations.Apply(ctx, store.Pool()); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if _, err := store.Pool().Exec(ctx, `TRUNCATE sessions, runs, run_steps, session_events, reports RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return store
}

func TestRepositoriesContract(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	sess := &session.Session{
		ID:            "sess-integration",
		CandidateName: "李四",
		TargetRole:    "Go 后端",
		ResumeText:    "resume",
		JDText:        "jd",
		Config:        session.DefaultConfig(),
		Status:        session.StatusCreated,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := store.Repos().Sessions.Create(ctx, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	got, err := store.Repos().Sessions.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Status != session.StatusCreated || got.StateVersion != 0 {
		t.Fatalf("session = %s v%d", got.Status, got.StateVersion)
	}

	expected := got.StateVersion
	if err := got.Start(now); err != nil {
		t.Fatal(err)
	}
	if err := store.Repos().Sessions.UpdateState(ctx, got, expected); err != nil {
		t.Fatalf("update state: %v", err)
	}
	if got.StateVersion != expected+1 {
		t.Fatalf("state version = %d", got.StateVersion)
	}

	got2, err := store.Repos().Sessions.Get(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := got2.Finish(now); err != nil {
		t.Fatal(err)
	}
	if err := store.Repos().Sessions.UpdateState(ctx, got2, expected); !errors.Is(err, ports.ErrStateConflict) {
		t.Fatalf("stale update error = %v, want state conflict", err)
	}

	run := &session.Run{
		ID:             "run-integration",
		SessionID:      sess.ID,
		Kind:           session.RunKindStart,
		Status:         session.RunStatusRunning,
		IdempotencyKey: "key-1",
		RequestHash:    "hash-1",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	created, _, err := store.Repos().Runs.Create(ctx, run)
	if err != nil || !created {
		t.Fatalf("create run: created=%v err=%v", created, err)
	}

	duplicate := *run
	duplicate.ID = "run-duplicate"
	created, existing, err := store.Repos().Runs.Create(ctx, &duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if created || existing == nil || existing.ID != run.ID {
		t.Fatalf("duplicate create: created=%v existing=%+v", created, existing)
	}

	if err := store.Repos().Steps.Start(ctx, &session.RunStep{
		RunID:     run.ID,
		StepName:  "plan",
		Status:    session.StepStatusRunning,
		Attempt:   1,
		StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Repos().Steps.Complete(ctx, run.ID, "plan", []byte(`{"ok":true}`), now); err != nil {
		t.Fatal(err)
	}
	step, err := store.Repos().Steps.Get(ctx, run.ID, "plan")
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]bool
	if err := json.Unmarshal(step.Output, &output); err != nil {
		t.Fatalf("decode step output %s: %v", step.Output, err)
	}
	if step.Status != session.StepStatusSucceeded || !output["ok"] {
		t.Fatalf("step = %s %s", step.Status, step.Output)
	}

	first := &session.Event{SessionID: sess.ID, RunID: run.ID, Type: session.EventRunStarted, Payload: []byte(`{}`)}
	if err := store.Repos().Events.Append(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := &session.Event{SessionID: sess.ID, RunID: run.ID, Type: session.EventQuestionAsked, Payload: []byte(`{"turn_id":"t1"}`)}
	if err := store.Repos().Events.Append(ctx, second); err != nil {
		t.Fatal(err)
	}

	events, err := store.Repos().Events.ListAfter(ctx, sess.ID, first.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != second.ID || events[0].Type != session.EventQuestionAsked {
		t.Fatalf("events after cursor = %+v", events)
	}
}

func TestAnswerFlowWithPostgres(t *testing.T) {
	store := openStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	evaluator := interview.NewEvaluator(interview.NewHeuristicClient(), "fake-heuristic")
	svc := app.New(store, app.Options{
		Logger:      logger,
		Synchronous: true,
		Evaluator:   evaluator,
	})
	ctx := context.Background()

	sess, err := svc.CreateSession(ctx, app.CreateSessionInput{
		CandidateName: "钱七",
		TargetRole:    "Go 后端开发",
		ResumeText:    "四年 Go 经验，做过支付与订单系统",
		JDText:        "分布式系统、消息队列",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.ResumeProfile.Years != 4 || !hasSkill(sess.ResumeProfile, "Go") {
		t.Fatalf("parsed profile = %+v", sess.ResumeProfile)
	}
	if _, err := svc.Start(ctx, app.StartCommand{
		SessionID:      sess.ID,
		IdempotencyKey: "pg-answer-start",
		RequestBody:    []byte(`{}`),
	}); err != nil {
		t.Fatalf("start: %v", err)
	}

	started, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.PendingTurnID == "" || started.PlanCursor != 1 {
		t.Fatalf("started session = %+v", started)
	}
	if started.ResumeProfile.Years != 4 {
		t.Fatalf("persisted profile = %+v", started.ResumeProfile)
	}

	firstTurn, err := store.Repos().Turns.Get(ctx, started.PendingTurnID)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstTurn.ExpectedPoints) == 0 || firstTurn.ExpectedPoints[0] == "" {
		t.Fatalf("first turn expected points = %+v", firstTurn.ExpectedPoints)
	}

	answer := "我在订单系统里用本地消息表和定时补偿保证最终一致性，压测峰值 1.2 万 TPS。"
	result, err := svc.Answer(ctx, app.AnswerCommand{
		SessionID:      sess.ID,
		TurnID:         started.PendingTurnID,
		Answer:         answer,
		IdempotencyKey: "pg-answer-1",
	})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if result.RunID == "" || result.Replayed {
		t.Fatalf("unexpected answer result: %+v", result)
	}

	turn, err := store.Repos().Turns.Get(ctx, started.PendingTurnID)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Status != session.TurnStatusAnswered || turn.AnswerText != answer {
		t.Fatalf("turn = %s %q", turn.Status, turn.AnswerText)
	}

	rec, err := store.Repos().Evaluations.GetByTurn(ctx, started.PendingTurnID)
	if err != nil {
		t.Fatalf("evaluation: %v", err)
	}
	if rec.Degraded || len(rec.Scores) != len(evaluation.DimensionOrder) || rec.WeightedScore <= 0 {
		t.Fatalf("evaluation = %+v", rec)
	}

	updated, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PlanCursor != 2 || updated.PendingTurnID == started.PendingTurnID {
		t.Fatalf("session did not advance: cursor=%d pending=%s", updated.PlanCursor, updated.PendingTurnID)
	}

	plans, err := store.Repos().Plans.ListBySession(ctx, sess.ID)
	if err != nil || len(plans) < 2 {
		t.Fatalf("plan items = %d err=%v", len(plans), err)
	}
	for _, plan := range plans {
		if plan.Objective == "" || len(plan.ExpectedPoints) == 0 || plan.Difficulty == "" {
			t.Fatalf("plan item not fully persisted: %+v", plan)
		}
	}

	events, err := svc.ListEvents(ctx, sess.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Type]++
	}
	for _, want := range []string{
		session.EventAnswerReceived,
		session.EventEvaluationDone,
		session.EventDecisionMade,
		session.EventQuestionAsked,
	} {
		if counts[want] == 0 {
			t.Fatalf("missing event %s in %+v", want, counts)
		}
	}

	replay, err := svc.Answer(ctx, app.AnswerCommand{
		SessionID:      sess.ID,
		TurnID:         started.PendingTurnID,
		Answer:         answer,
		IdempotencyKey: "pg-answer-1",
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed || replay.RunID != result.RunID {
		t.Fatalf("replay = %+v, original = %s", replay, result.RunID)
	}
	after, err := svc.ListEvents(ctx, sess.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(events) {
		t.Fatalf("replay appended events: %d -> %d", len(events), len(after))
	}
}

func TestReconcileWithPostgres(t *testing.T) {
	store := openStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.New(store, app.Options{Logger: logger, Synchronous: true, RunStaleAfter: time.Minute})
	ctx := context.Background()

	sess, err := svc.CreateSession(ctx, app.CreateSessionInput{
		CandidateName: "赵六",
		TargetRole:    "Go 后端开发",
		ResumeText:    "resume",
		JDText:        "jd",
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	run := &session.Run{
		ID:             "run-crashed-pg",
		SessionID:      sess.ID,
		Kind:           session.RunKindStart,
		Status:         session.RunStatusRunning,
		IdempotencyKey: "pg-crash",
		RequestHash:    "hash",
		Attempt:        1,
		LastStep:       "plan",
		CreatedAt:      now.Add(-4 * time.Minute),
		UpdatedAt:      now.Add(-2 * time.Minute),
	}
	if _, _, err := store.Repos().Runs.Create(ctx, run); err != nil {
		t.Fatal(err)
	}

	cachedPlan, err := json.Marshal(app.PlanResult{
		Direction: "go",
		Items: []interview.PlanItemView{
			{Seq: 1, Topic: "cached-topic", Objective: "cached objective", ExpectedPoints: []string{"point-a", "point-b"}, Difficulty: "mid"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Repos().Steps.Start(ctx, &session.RunStep{
		RunID: run.ID, StepName: "plan", Status: session.StepStatusRunning, Attempt: 1, StartedAt: now.Add(-4 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Repos().Steps.Complete(ctx, run.ID, "plan", cachedPlan, now.Add(-4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.Repos().Steps.Start(ctx, &session.RunStep{
		RunID: run.ID, StepName: "ask", Status: session.StepStatusRunning, Attempt: 1, StartedAt: now.Add(-3 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	resumed, err := svc.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if resumed != 1 {
		t.Fatalf("resumed = %d", resumed)
	}

	got, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != session.StatusActive || got.PendingTurnID == "" {
		t.Fatalf("session = %s/%s pending=%s", got.Status, got.Phase, got.PendingTurnID)
	}

	planStep, err := store.Repos().Steps.Get(ctx, run.ID, "plan")
	if err != nil {
		t.Fatal(err)
	}
	if planStep.Attempt != 1 {
		t.Fatalf("plan attempt = %d, want replay", planStep.Attempt)
	}

	askStep, err := store.Repos().Steps.Get(ctx, run.ID, "ask")
	if err != nil {
		t.Fatal(err)
	}
	if askStep.Attempt != 2 {
		t.Fatalf("ask attempt = %d, want 2", askStep.Attempt)
	}

	events, err := svc.ListEvents(ctx, sess.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Type]++
	}
	if counts[session.EventRunInterrupted] != 1 || counts[session.EventQuestionAsked] != 1 {
		t.Fatalf("event counts = %+v", counts)
	}
}

func TestStartFlowWithPostgres(t *testing.T) {
	store := openStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := app.New(store, app.Options{Logger: logger, Synchronous: true})
	ctx := context.Background()

	sess, err := svc.CreateSession(ctx, app.CreateSessionInput{
		CandidateName: "王五",
		TargetRole:    "Go 后端开发",
		ResumeText:    "五年 Go 经验",
		JDText:        "分布式系统",
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	result, err := svc.Start(ctx, app.StartCommand{
		SessionID:      sess.ID,
		IdempotencyKey: "integration-start",
		RequestBody:    []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	got, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != session.StatusActive || got.Phase != session.PhaseAwaitingAnswer || got.StateVersion != 1 {
		t.Fatalf("session = %s/%s v%d", got.Status, got.Phase, got.StateVersion)
	}

	events, err := svc.ListEvents(ctx, sess.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("events = %d: %+v", len(events), events)
	}
	want := []string{
		session.EventSessionCreated,
		session.EventRunStarted,
		session.EventSessionStarted,
		session.EventQuestionAsked,
		session.EventSessionPhase,
	}
	for i, event := range events {
		if event.Type != want[i] {
			t.Fatalf("event[%d] = %s, want %s", i, event.Type, want[i])
		}
	}

	replay, err := svc.Start(ctx, app.StartCommand{
		SessionID:      sess.ID,
		IdempotencyKey: "integration-start",
		RequestBody:    []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed || replay.RunID != result.RunID {
		t.Fatalf("replay = %+v", replay)
	}

	after, err := svc.ListEvents(ctx, sess.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(events) {
		t.Fatalf("replay duplicated events: %d -> %d", len(events), len(after))
	}
}

func TestFinishReportWithPostgres(t *testing.T) {
	store := openStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	evaluator := interview.NewEvaluator(interview.NewHeuristicClient(), "fake-heuristic")
	svc := app.New(store, app.Options{Logger: logger, Synchronous: true, Evaluator: evaluator})
	ctx := context.Background()

	sess, err := svc.CreateSession(ctx, app.CreateSessionInput{
		CandidateName: "孙八",
		TargetRole:    "Go 后端开发",
		ResumeText:    "四年 Go 经验，做过订单与支付系统",
		JDText:        "分布式系统、消息队列",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Start(ctx, app.StartCommand{
		SessionID:      sess.ID,
		IdempotencyKey: "pg-report-start",
		RequestBody:    []byte(`{}`),
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	started, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	answer := "我在订单系统里用本地消息表和定时补偿保证最终一致性，压测峰值 1.2 万 TPS。"
	if _, err := svc.Answer(ctx, app.AnswerCommand{
		SessionID:      sess.ID,
		TurnID:         started.PendingTurnID,
		Answer:         answer,
		IdempotencyKey: "pg-report-answer",
	}); err != nil {
		t.Fatalf("answer: %v", err)
	}

	evals, err := store.Repos().Evaluations.ListBySession(ctx, sess.ID)
	if err != nil || len(evals) != 1 {
		t.Fatalf("evaluations = %d err=%v", len(evals), err)
	}

	finish, err := svc.Finish(ctx, app.FinishCommand{SessionID: sess.ID, IdempotencyKey: "pg-finish"})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if finish.RunID == "" || finish.Replayed {
		t.Fatalf("finish result = %+v", finish)
	}

	rec, err := svc.GetReport(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if rec.SessionID != sess.ID || rec.RunID != finish.RunID || rec.Status != "completed" {
		t.Fatalf("report record = %+v", rec)
	}
	if rec.Report.SchemaVersion != domainreport.SchemaVersion {
		t.Fatalf("schema version = %d", rec.Report.SchemaVersion)
	}
	if rec.Report.Degraded || rec.Report.OverallScore <= 0 || rec.Markdown == "" {
		t.Fatalf("report = degraded=%v code=%s score=%.1f markdown=%d", rec.Report.Degraded, rec.Report.ErrorCode, rec.Report.OverallScore, len(rec.Markdown))
	}
	if len(rec.Report.Sections) != 5 || len(rec.Report.Dimensions) != 5 || len(rec.Report.Improvements) != 3 {
		t.Fatalf("sections=%d dimensions=%d improvements=%d",
			len(rec.Report.Sections), len(rec.Report.Dimensions), len(rec.Report.Improvements))
	}
	if rec.Report.ToolCalls != 3 {
		t.Fatalf("tool calls = %d, want 3", rec.Report.ToolCalls)
	}

	final, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != session.StatusCompleted {
		t.Fatalf("session = %s/%s", final.Status, final.Phase)
	}

	events, err := svc.ListEvents(ctx, sess.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	var sectionIndexes []int
	for _, event := range events {
		counts[event.Type]++
		if event.Type == session.EventReportSection {
			var payload struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("decode section payload: %v", err)
			}
			sectionIndexes = append(sectionIndexes, payload.Index)
		}
	}
	if counts[session.EventReportStarted] != 1 || counts[session.EventReportSection] != 5 ||
		counts[session.EventReportToolCalls] != 1 || counts[session.EventReportCompleted] != 1 ||
		counts[session.EventSessionCompleted] != 1 {
		t.Fatalf("report event counts = %+v", counts)
	}
	for i, index := range sectionIndexes {
		if index != i {
			t.Fatalf("section indexes = %v", sectionIndexes)
		}
	}
	if events[len(events)-1].Type != session.EventSessionCompleted {
		t.Fatalf("terminal event = %s", events[len(events)-1].Type)
	}

	replay, err := svc.Finish(ctx, app.FinishCommand{SessionID: sess.ID, IdempotencyKey: "pg-finish"})
	if err != nil {
		t.Fatalf("replay finish: %v", err)
	}
	if !replay.Replayed || !replay.ReportReady || replay.ReportID != rec.ID || replay.RunID != finish.RunID {
		t.Fatalf("replay = %+v", replay)
	}

	otherKey, err := svc.Finish(ctx, app.FinishCommand{SessionID: sess.ID, IdempotencyKey: "pg-finish-other"})
	if err != nil {
		t.Fatalf("finish with other key: %v", err)
	}
	if !otherKey.Replayed || !otherKey.ReportReady || otherKey.ReportID != rec.ID {
		t.Fatalf("other key = %+v", otherKey)
	}

	after, err := svc.ListEvents(ctx, sess.ID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(events) {
		t.Fatalf("repeated finish appended events: %d -> %d", len(events), len(after))
	}

	if _, err := svc.GetReport(ctx, "missing-session"); !errors.Is(err, app.ErrReportNotReady) {
		t.Fatalf("missing report error = %v", err)
	}
}

func openSecondStore(t *testing.T) *postgres.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set; integration tests need PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open second postgres store: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

// TestNotifyReachesSecondStore proves LISTEN/NOTIFY crosses independent
// connection pools (the two instances of a real deployment use separate
// processes and thus separate LISTEN connections).
func TestNotifyReachesSecondStore(t *testing.T) {
	storeA := openStore(t)
	storeB := openSecondStore(t)
	ctx := context.Background()

	svcA := app.New(storeA, app.Options{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Synchronous: true,
		Evaluator:   interview.NewEvaluator(interview.NewHeuristicClient(), "fake-heuristic"),
	})
	sess, err := svcA.CreateSession(ctx, app.CreateSessionInput{
		CandidateName: "通知测试",
		TargetRole:    "Go 后端开发",
		ResumeText:    "三年 Go 经验",
		JDText:        "分布式系统",
	})
	if err != nil {
		t.Fatal(err)
	}

	subCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	signals, err := storeB.SubscribeEvents(subCtx, sess.ID)
	if err != nil {
		t.Fatalf("subscribe from second store: %v", err)
	}

	if err := svcA.Abort(ctx, sess.ID); err != nil {
		t.Fatalf("abort: %v", err)
	}

	select {
	case <-signals:
	case <-time.After(4 * time.Second):
		t.Fatal("second store never received NOTIFY")
	}

	events, err := storeB.Repos().Events.ListAfter(ctx, sess.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type == session.EventSessionAborted {
			found = true
		}
	}
	if !found {
		t.Fatalf("second store missing aborted event: %v", events)
	}
}

// TestTwoInstancesDoNotDoubleWriteSameTurn runs two app.Service instances with
// two Redis clients against the same PostgreSQL database. The shared Redis lock
// must admit exactly one answer, and the answer must be persisted once.
func TestTwoInstancesDoNotDoubleWriteSameTurn(t *testing.T) {
	storeA := openStore(t)
	storeB := openSecondStore(t)
	ctx := context.Background()

	lockAddr := miniredis.RunT(t).Addr()
	if realAddr := os.Getenv("REDIS_ADDR"); realAddr != "" {
		lockAddr = realAddr
	}
	t.Logf("session lock backend: %s", lockAddr)
	clientA := goredis.NewClient(&goredis.Options{Addr: lockAddr})
	clientB := goredis.NewClient(&goredis.Options{Addr: lockAddr})
	t.Cleanup(func() {
		_ = clientA.Close()
		_ = clientB.Close()
	})

	lockOpts := storeredis.LockOptions{
		TTL:            2 * time.Second,
		AcquireTimeout: 3 * time.Second,
		RetryInterval:  10 * time.Millisecond,
	}
	newService := func(store *postgres.Store, locker ports.Locker) *app.Service {
		return app.New(store, app.Options{
			Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			Synchronous: true,
			Evaluator:   interview.NewEvaluator(interview.NewHeuristicClient(), "fake-heuristic"),
			Locker:      locker,
		})
	}
	svcA := newService(storeA, storeredis.NewLockerWithClient(clientA, lockOpts))
	svcB := newService(storeB, storeredis.NewLockerWithClient(clientB, lockOpts))

	sess, err := svcA.CreateSession(ctx, app.CreateSessionInput{
		CandidateName: "并发测试",
		TargetRole:    "Go 后端开发",
		ResumeText:    "四年 Go 经验",
		JDText:        "分布式系统",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svcA.Start(ctx, app.StartCommand{
		SessionID:      sess.ID,
		IdempotencyKey: "concurrent-start",
		RequestBody:    []byte(`{}`),
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	started, err := svcA.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	turnID := started.PendingTurnID

	answer := "我在订单服务里用本地消息表加定时补偿保证最终一致性，压测下单峰值 1.2 万 TPS。"
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, svc := range []*app.Service{svcA, svcB} {
		wg.Add(1)
		go func(i int, svc *app.Service) {
			defer wg.Done()
			<-start
			_, errs[i] = svc.Answer(ctx, app.AnswerCommand{
				SessionID:      sess.ID,
				TurnID:         turnID,
				Answer:         answer,
				IdempotencyKey: "concurrent-answer-" + string(rune('a'+i)),
			})
		}(i, svc)
	}
	close(start)
	wg.Wait()

	var successes int
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successes = %d (errs=%v), want exactly 1", successes, errs)
	}

	var answerRuns int
	if err := storeA.Pool().QueryRow(ctx,
		`SELECT count(*) FROM runs WHERE session_id = $1 AND kind = 'answer'`, sess.ID).Scan(&answerRuns); err != nil {
		t.Fatal(err)
	}
	if answerRuns != 1 {
		t.Fatalf("answer runs = %d, want 1", answerRuns)
	}

	var answeredTurns int
	if err := storeA.Pool().QueryRow(ctx,
		`SELECT count(*) FROM turns WHERE session_id = $1 AND status = 'answered'`, sess.ID).Scan(&answeredTurns); err != nil {
		t.Fatal(err)
	}
	if answeredTurns != 1 {
		t.Fatalf("answered turns = %d, want 1", answeredTurns)
	}

	evals, err := storeA.Repos().Evaluations.ListBySession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evals) != 1 {
		t.Fatalf("evaluations = %d, want 1", len(evals))
	}

	// PG uniqueness is the second line of defence: even if the lock is
	// bypassed, the turns UPDATE ... WHERE status='pending' CAS and the
	// (session_id, seq) uniqueness prevent duplicate answers.
	t.Run("pg_constraints_guard_without_shared_lock", func(t *testing.T) {
		svcC := newService(storeA, platformlock.NewProcessLocker())
		svcD := newService(storeB, platformlock.NewProcessLocker())
		current, err := svcC.GetSession(ctx, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		nextTurn := current.PendingTurnID
		if nextTurn == "" {
			t.Fatal("no pending turn for second round")
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, svc := range []*app.Service{svcC, svcD} {
			wg.Add(1)
			go func(i int, svc *app.Service) {
				defer wg.Done()
				<-start
				_, _ = svc.Answer(ctx, app.AnswerCommand{
					SessionID:      sess.ID,
					TurnID:         nextTurn,
					Answer:         answer,
					IdempotencyKey: "unlocked-answer-" + string(rune('a'+i)),
				})
			}(i, svc)
		}
		close(start)
		wg.Wait()

		var answered int
		if err := storeA.Pool().QueryRow(ctx,
			`SELECT count(*) FROM turns WHERE id = $1 AND status = 'answered'`, nextTurn).Scan(&answered); err != nil {
			t.Fatal(err)
		}
		if answered != 1 {
			t.Fatalf("turn %s answered %d times", nextTurn, answered)
		}
		evals, err := storeA.Repos().Evaluations.ListBySession(ctx, sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(evals) != 2 {
			t.Fatalf("evaluations = %d, want 2 (one per answered turn)", len(evals))
		}
	})
}

func hasSkill(p resume.Profile, skill string) bool {
	for _, s := range p.Skills {
		if s == skill {
			return true
		}
	}
	return false
}
