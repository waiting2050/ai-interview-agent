package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	reportagent "github.com/waiting2050/ai-interview-agent/internal/agent/report"
	"github.com/waiting2050/ai-interview-agent/internal/agent/tools"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

type countingGenerator struct {
	inner reportagent.Generator
	calls int
}

func (c *countingGenerator) Generate(ctx context.Context, ev tools.Bundle) (reportagent.Outcome, error) {
	c.calls++
	return c.inner.Generate(ctx, ev)
}

func startedSession(t *testing.T, svc *Service) *session.Session {
	t.Helper()
	ctx := context.Background()
	sess := createSession(t, svc)
	if _, err := svc.Start(ctx, StartCommand{
		SessionID:      sess.ID,
		IdempotencyKey: "m4-start",
		RequestBody:    []byte(`{}`),
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	started, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get started session: %v", err)
	}
	return started
}

func answerCurrentTurn(t *testing.T, svc *Service, sess *session.Session, answer, key string) {
	t.Helper()
	if _, err := svc.Answer(context.Background(), AnswerCommand{
		SessionID:      sess.ID,
		TurnID:         sess.PendingTurnID,
		Answer:         answer,
		IdempotencyKey: key,
	}); err != nil {
		t.Fatalf("answer: %v", err)
	}
}

func TestFinishGeneratesReportAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	generator := &countingGenerator{inner: reportagent.NewBuilder(interview.NewHeuristicClient(), "fake-heuristic")}
	svc, store := newTestService(t, Options{
		Evaluator:       &stubEvaluator{outcome: outcomeFor("我在订单系统里用本地消息表保证最终一致性。", 4, false)},
		ReportGenerator: generator,
	})

	sess := startedSession(t, svc)
	answerCurrentTurn(t, svc, sess, "我在订单系统里用本地消息表和定时补偿保证最终一致性，压测峰值 1.2 万 TPS。", "m4-answer-1")

	first, err := svc.Finish(ctx, FinishCommand{SessionID: sess.ID, IdempotencyKey: "m4-finish"})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if first.Replayed {
		t.Fatalf("first finish should not be a replay: %+v", first)
	}

	report, err := svc.GetReport(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if report.Report.OverallScore != 80 || len(report.Report.Sections) != 5 {
		t.Fatalf("report = %+v", report.Report)
	}
	if report.Markdown == "" || len(report.Report.Improvements) != 3 {
		t.Fatalf("report markdown/improvements incomplete")
	}

	finished, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get finished session: %v", err)
	}
	if finished.Status != session.StatusCompleted {
		t.Fatalf("session status = %s", finished.Status)
	}

	events := listEvents(t, store, sess.ID)
	if len(events) == 0 {
		t.Fatalf("no events")
	}
	var reportEvents []session.Event
	seen := false
	for _, e := range events {
		if e.Type == session.EventReportStarted {
			seen = true
		}
		if seen {
			reportEvents = append(reportEvents, e)
		}
	}
	if len(reportEvents) == 0 || reportEvents[0].Type != session.EventReportStarted {
		t.Fatalf("report event order = %v", eventTypes(reportEvents))
	}
	sectionIndex := -1
	sections := 0
	completed := false
	for _, e := range reportEvents {
		switch e.Type {
		case session.EventReportSection:
			if completed {
				t.Fatalf("section after completed: %v", eventTypes(reportEvents))
			}
			var payload struct {
				Index int `json:"index"`
			}
			if err := json.Unmarshal(e.Payload, &payload); err != nil {
				t.Fatalf("decode section: %v", err)
			}
			if payload.Index != sectionIndex+1 {
				t.Fatalf("section index = %d after %d", payload.Index, sectionIndex)
			}
			sectionIndex = payload.Index
			sections++
		case session.EventReportCompleted:
			if sections != 5 {
				t.Fatalf("completed after %d sections", sections)
			}
			completed = true
		}
	}
	if sections != 5 {
		t.Fatalf("sections = %d", sections)
	}
	if got := reportEvents[len(reportEvents)-1].Type; got != session.EventSessionCompleted {
		t.Fatalf("terminal event = %s", got)
	}

	replay, err := svc.Finish(ctx, FinishCommand{SessionID: sess.ID, IdempotencyKey: "m4-finish"})
	if err != nil {
		t.Fatalf("replay finish: %v", err)
	}
	if !replay.Replayed || !replay.ReportReady || replay.ReportID != report.ID || replay.RunID != first.RunID {
		t.Fatalf("replay = %+v, first = %+v", replay, first)
	}

	otherKey, err := svc.Finish(ctx, FinishCommand{SessionID: sess.ID, IdempotencyKey: "another-key"})
	if err != nil {
		t.Fatalf("finish with another key: %v", err)
	}
	if !otherKey.Replayed || otherKey.ReportID != report.ID {
		t.Fatalf("different key must return the existing report: %+v", otherKey)
	}
	if generator.calls != 1 {
		t.Fatalf("generator called %d times, want exactly 1", generator.calls)
	}
	if after := len(listEvents(t, store, sess.ID)); after != len(events) {
		t.Fatalf("replays appended events: %d -> %d", len(events), after)
	}
}

func TestFinishDegradesWhenModelFails(t *testing.T) {
	ctx := context.Background()
	broken := llm.NewEinoClient(llm.NewFakeChatModel("broken", func(context.Context, llm.Request) (string, error) {
		return "", errors.New("upstream unavailable")
	}), "broken")
	metrics := obs.NewMetrics()
	svc, _ := newTestService(t, Options{
		Metrics:         metrics,
		Evaluator:       &stubEvaluator{outcome: outcomeFor("回答内容", 3, false)},
		ReportGenerator: reportagent.NewBuilder(broken, "broken"),
	})

	sess := startedSession(t, svc)
	answerCurrentTurn(t, svc, sess, "我在订单系统里用本地消息表保证最终一致性，并通过定时任务补偿失败消息，线上运行稳定。", "m4-degraded-answer")

	if _, err := svc.Finish(ctx, FinishCommand{SessionID: sess.ID}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	report, err := svc.GetReport(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if !report.Report.Degraded || report.Report.ErrorCode != reportagent.ErrorCodeModelUnavailable {
		t.Fatalf("report = %+v", report.Report)
	}
	if len(report.Report.Sections) != 5 || report.Markdown == "" {
		t.Fatalf("degraded report incomplete")
	}
	finished, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if finished.Status != session.StatusCompleted {
		t.Fatalf("degraded report must still complete the session: %s", finished.Status)
	}
	if got := metrics.ReportDegradedTotal(reportagent.ErrorCodeModelUnavailable); got != 1 {
		t.Fatalf("report_degraded_total = %d, want 1", got)
	}
}

func TestReportCarriesSessionUsageSnapshot(t *testing.T) {
	ctx := context.Background()
	usage := obs.NewUsageTracker()
	svc, store := newTestService(t, Options{
		Evaluator: &stubEvaluator{outcome: outcomeFor("回答内容", 3, false)},
		Usage:     usage,
	})

	sess := startedSession(t, svc)
	answerCurrentTurn(t, svc, sess, "我在订单系统里用本地消息表保证最终一致性。", "m7-cost-answer")

	// Simulate the instrumented model clients that the server wires in.
	usage.Record(sess.ID, "evaluate", 320, 90)
	usage.Record(sess.ID, "question", 180, 40)

	if _, err := svc.Finish(ctx, FinishCommand{SessionID: sess.ID}); err != nil {
		t.Fatalf("finish: %v", err)
	}
	report, err := svc.GetReport(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}

	doc := report.Report
	if doc.ModelCalls != 2 || doc.PromptTokens != 500 || doc.CompletionTokens != 130 || doc.TotalTokens != 630 {
		t.Fatalf("usage fields = calls %d, tokens %d/%d/%d",
			doc.ModelCalls, doc.PromptTokens, doc.CompletionTokens, doc.TotalTokens)
	}
	if doc.ModelCallBreakdown["evaluate"] != 1 || doc.ModelCallBreakdown["question"] != 1 {
		t.Fatalf("usage breakdown = %+v", doc.ModelCallBreakdown)
	}

	var completedPayload map[string]any
	for _, e := range listEvents(t, store, sess.ID) {
		if e.Type == session.EventReportCompleted {
			if err := json.Unmarshal(e.Payload, &completedPayload); err != nil {
				t.Fatalf("decode completed payload: %v", err)
			}
		}
	}
	if completedPayload["model_calls"] != float64(2) || completedPayload["total_tokens"] != float64(630) {
		t.Fatalf("report.completed usage payload = %+v", completedPayload)
	}
}

func TestReportRefusesToCutInterviewShort(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t, Options{
		Evaluator: &stubEvaluator{outcome: outcomeFor("回答", 3, false)},
	})
	sess := startedSession(t, svc)

	if _, err := svc.Report(ctx, FinishCommand{SessionID: sess.ID}); !errors.Is(err, ports.ErrStateConflict) {
		t.Fatalf("report error = %v, want state conflict", err)
	}

	created := createSession(t, svc)
	if _, err := svc.Finish(ctx, FinishCommand{SessionID: created.ID}); !errors.Is(err, ports.ErrStateConflict) {
		t.Fatalf("finish before start error = %v, want state conflict", err)
	}
}

func TestGetReportBeforeFinish(t *testing.T) {
	svc, _ := newTestService(t, Options{Evaluator: &stubEvaluator{outcome: outcomeFor("回答", 3, false)}})
	sess := startedSession(t, svc)
	if _, err := svc.GetReport(context.Background(), sess.ID); !errors.Is(err, ErrReportNotReady) {
		t.Fatalf("error = %v, want ErrReportNotReady", err)
	}
}
