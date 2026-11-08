package report_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	reportagent "github.com/waiting2050/ai-interview-agent/internal/agent/report"
	"github.com/waiting2050/ai-interview-agent/internal/agent/tools"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

func toolAction(tool, args string) string {
	return `{"action":"tool","tool":"` + tool + `","args":` + args + `}`
}

func finalAction() string {
	return `{"action":"final","report":` + validJSON + `}`
}

// toolFixture builds a session whose turn ids match bundle() so a model final
// referencing t1/t2 passes validation.
func toolFixture(t *testing.T) (*memory.Store, string) {
	t.Helper()
	store := memory.New()
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

	sess := &session.Session{
		ID: "sess-1", TargetRole: "Go 后端开发",
		Config: session.Config{Difficulty: "mid", MaxPrimaryQuestions: 8},
		Status: session.StatusCompleted, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Repos().Sessions.Create(ctx, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	for i, id := range []string{"t1", "t2"} {
		turn := &session.Turn{
			ID: id, SessionID: sess.ID, Seq: i + 1, Kind: session.TurnKindPrimary,
			Topic: "Go 内存模型", Question: "说说逃逸分析",
			AnswerText: strings.Repeat("我把结论和实现细节讲了一遍。", 120),
			Status:     session.TurnStatusAnswered, CreatedAt: now, UpdatedAt: now,
		}
		if err := store.Repos().Turns.Create(ctx, turn); err != nil {
			t.Fatalf("create turn %s: %v", id, err)
		}
		if err := store.Repos().Evaluations.Upsert(ctx, &evaluation.Record{
			TurnID: id, SessionID: sess.ID, SchemaVersion: evaluation.SchemaVersion,
			Scores: map[evaluation.Dimension]int{
				evaluation.DimensionCorrectness: 2, evaluation.DimensionDepth: 2,
				evaluation.DimensionEngineeringJudgment: 2, evaluation.DimensionStructure: 2,
				evaluation.DimensionEvidenceCredibility: 2,
			},
			WeightedScore: 60, Confidence: 0.6, CreatedAt: now,
		}); err != nil {
			t.Fatalf("upsert evaluation: %v", err)
		}
	}

	other := &session.Session{ID: "sess-b", TargetRole: "Java", Status: session.StatusCompleted, CreatedAt: now, UpdatedAt: now}
	if err := store.Repos().Sessions.Create(ctx, other); err != nil {
		t.Fatalf("create other session: %v", err)
	}
	if err := store.Repos().Turns.Create(ctx, &session.Turn{
		ID: "turn-b1", SessionID: "sess-b", Seq: 1, Kind: session.TurnKindPrimary,
		Topic: "JVM", Question: "JVM 内存区域", AnswerText: "堆、栈、方法区",
		Status: session.TurnStatusAnswered, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create other turn: %v", err)
	}
	return store, sess.ID
}

type recordedEvent struct {
	kind    string
	payload map[string]any
}

func sinkOf(events *[]recordedEvent) reportagent.EventSink {
	return func(ev reportagent.Event) {
		*events = append(*events, recordedEvent{kind: ev.Type, payload: ev.Payload})
	}
}

// fakeExec lets timing and budget tests control latency without a database.
type fakeExec struct {
	calls   int
	records []tools.CallRecord
	fn      func(ctx context.Context, tool string, args json.RawMessage) (tools.Result, error)
}

func (f *fakeExec) Execute(ctx context.Context, tool string, args json.RawMessage) (tools.Result, error) {
	f.calls++
	res, err := f.fn(ctx, tool, args)
	status := res.Status
	if status == "" {
		status = tools.StatusOK
	}
	f.records = append(f.records, tools.CallRecord{Tool: tool, Status: status, DurationMS: res.DurationMS, ResultBytes: res.ResultBytes, Truncated: res.Truncated})
	return res, err
}

func (f *fakeExec) Records() []tools.CallRecord { return f.records }
func (f *fakeExec) Calls() int                  { return f.calls }
func (f *fakeExec) Breakdown() map[string]int {
	out := map[string]int{}
	for _, r := range f.records {
		out[r.Tool]++
	}
	return out
}

func TestToolLoopRejectsUnknownTool(t *testing.T) {
	client := llm.NewEinoClient(llm.Scripted("loop", toolAction("delete_session", "{}"), finalAction()), "fake")
	builder := reportagent.NewBuilder(client, "fake").WithAutonomous(true, reportagent.AutonomousConfig{MaxRounds: 4})
	store, sessionID := toolFixture(t)
	q := tools.NewQuery(sessionID, store.Repos(), tools.Options{})

	var events []recordedEvent
	out, err := builder.GenerateWithTools(context.Background(), bundle(), q, sinkOf(&events))
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if out.Degraded || len(out.Report.Sections) != 5 {
		t.Fatalf("outcome = %+v", out)
	}
	if !hasEvent(events, reportagent.EventToolDenied, "unknown_tool") {
		t.Fatalf("missing unknown_tool denial: %+v", events)
	}
	if q.Calls() != 1 {
		t.Fatalf("unknown tool must count as one call, got %d", q.Calls())
	}
}

func TestToolLoopRejectsOutOfScopeTurnID(t *testing.T) {
	client := llm.NewEinoClient(llm.Scripted("loop", toolAction("get_turn", `{"turn_id":"turn-b1"}`), finalAction()), "fake")
	builder := reportagent.NewBuilder(client, "fake").WithAutonomous(true, reportagent.AutonomousConfig{MaxRounds: 4})
	store, sessionID := toolFixture(t)
	q := tools.NewQuery(sessionID, store.Repos(), tools.Options{})

	var events []recordedEvent
	out, err := builder.GenerateWithTools(context.Background(), bundle(), q, sinkOf(&events))
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if !hasEvent(events, reportagent.EventToolDenied, "out_of_scope") {
		t.Fatalf("missing out_of_scope denial: %+v", events)
	}
	if out.Degraded {
		t.Fatalf("outcome should recover after denial: %+v", out)
	}
}

func TestToolLoopRejectsInvalidArgs(t *testing.T) {
	bad := toolAction("search_turns", `{"limit":99}`)
	client := llm.NewEinoClient(llm.Scripted("loop", bad), "fake")
	builder := reportagent.NewBuilder(client, "fake").WithAutonomous(true, reportagent.AutonomousConfig{MaxRounds: 6})
	store, sessionID := toolFixture(t)
	q := tools.NewQuery(sessionID, store.Repos(), tools.Options{})

	var events []recordedEvent
	out, err := builder.GenerateWithTools(context.Background(), bundle(), q, sinkOf(&events))
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if out.ToolDegradedReason != reportagent.ReasonInvalidArgs {
		t.Fatalf("reason = %q", out.ToolDegradedReason)
	}
	if !out.Degraded || len(out.Report.Sections) != 5 {
		t.Fatalf("fallback report incomplete: %+v", out)
	}
	if !hasEvent(events, reportagent.EventToolDegraded, reportagent.ReasonInvalidArgs) {
		t.Fatalf("missing invalid_args degradation: %+v", events)
	}
}

func TestToolLoopSingleToolTimeout(t *testing.T) {
	client := llm.NewEinoClient(llm.Scripted("loop", toolAction("search_turns", `{"limit":2}`), finalAction()), "fake")
	builder := reportagent.NewBuilder(client, "fake").WithAutonomous(true, reportagent.AutonomousConfig{MaxRounds: 4})
	exec := &fakeExec{fn: func(context.Context, string, json.RawMessage) (tools.Result, error) {
		return tools.Result{Tool: "search_turns", Status: tools.StatusTimeout, DurationMS: 2000}, tools.ErrTimeout
	}}

	var events []recordedEvent
	out, err := builder.GenerateWithTools(context.Background(), bundle(), exec, sinkOf(&events))
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if out.Degraded || len(out.Report.Sections) != 5 {
		t.Fatalf("single timeout must not kill the run: %+v", out)
	}
	if !hasEventStatus(events, reportagent.EventToolCall, tools.StatusTimeout) {
		t.Fatalf("missing timeout call event: %+v", events)
	}
}

func TestToolLoopMaxRoundsDegrades(t *testing.T) {
	client := llm.NewEinoClient(llm.Scripted("loop", toolAction("search_turns", `{"limit":2}`)), "fake")
	builder := reportagent.NewBuilder(client, "fake").WithAutonomous(true, reportagent.AutonomousConfig{MaxRounds: 2})
	exec := &fakeExec{fn: func(context.Context, string, json.RawMessage) (tools.Result, error) {
		return tools.Result{Tool: "search_turns", Status: tools.StatusOK, ResultBytes: 10}, nil
	}}

	var events []recordedEvent
	out, err := builder.GenerateWithTools(context.Background(), bundle(), exec, sinkOf(&events))
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if out.ToolDegradedReason != reportagent.ReasonMaxRounds {
		t.Fatalf("reason = %q", out.ToolDegradedReason)
	}
	if !hasEvent(events, reportagent.EventToolDegraded, reportagent.ReasonMaxRounds) {
		t.Fatalf("missing max_rounds event: %+v", events)
	}
	if out.Report.ToolRounds != 3 { // two loop rounds plus the forced final
		t.Fatalf("rounds = %d", out.Report.ToolRounds)
	}
	if len(out.Report.Sections) != 5 {
		t.Fatalf("fallback report incomplete")
	}
}

func TestToolLoopMaxCallsDegrades(t *testing.T) {
	client := llm.NewEinoClient(llm.Scripted("loop", toolAction("search_turns", `{"limit":2}`)), "fake")
	builder := reportagent.NewBuilder(client, "fake").WithAutonomous(true, reportagent.AutonomousConfig{MaxRounds: 6, MaxCalls: 2})
	exec := &fakeExec{fn: func(context.Context, string, json.RawMessage) (tools.Result, error) {
		return tools.Result{Tool: "search_turns", Status: tools.StatusOK, ResultBytes: 10}, nil
	}}

	var events []recordedEvent
	out, err := builder.GenerateWithTools(context.Background(), bundle(), exec, sinkOf(&events))
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if out.ToolDegradedReason != reportagent.ReasonMaxCalls {
		t.Fatalf("reason = %q", out.ToolDegradedReason)
	}
	if exec.calls != 2 {
		t.Fatalf("executed calls = %d, want 2", exec.calls)
	}
	if !hasEvent(events, reportagent.EventToolDegraded, reportagent.ReasonMaxCalls) {
		t.Fatalf("missing max_calls event: %+v", events)
	}
}

func TestToolLoopTotalBudgetDegrades(t *testing.T) {
	client := llm.NewEinoClient(llm.Scripted("loop", toolAction("search_turns", `{"limit":2}`)), "fake")
	builder := reportagent.NewBuilder(client, "fake").WithAutonomous(true, reportagent.AutonomousConfig{
		MaxRounds: 6, MaxCalls: 12, Timeout: 40 * time.Millisecond,
	})
	exec := &fakeExec{fn: func(ctx context.Context, _ string, _ json.RawMessage) (tools.Result, error) {
		select {
		case <-ctx.Done():
			return tools.Result{Tool: "search_turns", Status: tools.StatusTimeout}, ctx.Err()
		case <-time.After(300 * time.Millisecond):
			return tools.Result{Tool: "search_turns", Status: tools.StatusOK}, nil
		}
	}}

	var events []recordedEvent
	out, err := builder.GenerateWithTools(context.Background(), bundle(), exec, sinkOf(&events))
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if out.ToolDegradedReason != reportagent.ReasonBudget {
		t.Fatalf("reason = %q", out.ToolDegradedReason)
	}
	if !out.Degraded || !out.Report.Degraded {
		t.Fatalf("budget fallback must be marked degraded: %+v", out)
	}
	if len(out.Report.Sections) != 5 {
		t.Fatalf("budget fallback report incomplete")
	}
}

func TestToolLoopTruncatesLargeResult(t *testing.T) {
	client := llm.NewEinoClient(llm.Scripted("loop", toolAction("get_turn", `{"turn_id":"t1"}`), finalAction()), "fake")
	builder := reportagent.NewBuilder(client, "fake").WithAutonomous(true, reportagent.AutonomousConfig{MaxRounds: 4})
	store, sessionID := toolFixture(t)
	// An intentionally small cap proves the model still sees a valid envelope.
	q := tools.NewQuery(sessionID, store.Repos(), tools.Options{MaxResultRunes: 400})

	var events []recordedEvent
	out, err := builder.GenerateWithTools(context.Background(), bundle(), q, sinkOf(&events))
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if !hasEventTruncated(events) {
		t.Fatalf("tool call not marked truncated: %+v", events)
	}
	if out.Degraded || len(out.Report.Sections) != 5 {
		t.Fatalf("model did not produce a valid final after truncation: %+v", out)
	}
	var detail *tools.CallRecord
	records := q.Records()
	for i := range records {
		if records[i].Tool == "get_turn" {
			detail = &records[i]
		}
	}
	if detail == nil || !detail.Truncated {
		t.Fatalf("audit record missing truncation: %+v", q.Records())
	}
	if len(out.Report.ToolCallsDetail) == 0 || !out.Report.ToolCallsDetail[len(out.Report.ToolCallsDetail)-1].Truncated {
		t.Fatalf("report detail did not persist truncation: %+v", out.Report.ToolCallsDetail)
	}
}

func TestToolLoopEmptyResultDoesNotFabricate(t *testing.T) {
	client := llm.NewEinoClient(llm.Scripted("loop", toolAction("search_turns", `{"query":"zzzz-nonexistent"}`), finalAction()), "fake")
	builder := reportagent.NewBuilder(client, "fake").WithAutonomous(true, reportagent.AutonomousConfig{MaxRounds: 4})
	store, sessionID := toolFixture(t)
	q := tools.NewQuery(sessionID, store.Repos(), tools.Options{})

	var events []recordedEvent
	out, err := builder.GenerateWithTools(context.Background(), bundle(), q, sinkOf(&events))
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if !hasEventStatus(events, reportagent.EventToolCall, tools.StatusEmpty) {
		t.Fatalf("missing empty status event: %+v", events)
	}
	var empty *tools.Result
	records := q.Records()
	for i := range records {
		if records[i].Tool == "search_turns" && records[i].Status == tools.StatusEmpty {
			empty = &tools.Result{Tool: records[i].Tool}
		}
	}
	if empty == nil {
		t.Fatalf("empty call was not recorded: %+v", records)
	}
	if out.Degraded || len(out.Report.Sections) != 5 {
		t.Fatalf("empty result should not block the report: %+v", out)
	}
	// The report must keep the deterministic fallback improvement list instead of
	// inventing knowledge hits.
	if len(out.Report.Improvements) == 0 {
		t.Fatalf("report lost its deterministic content")
	}
}

func TestToolLoopHeuristicFallsBack(t *testing.T) {
	client := llm.NewEinoClient(llm.Scripted("loop", toolAction("search_turns", `{"limit":2}`)), "fake")
	builder := reportagent.NewBuilder(client, "fake").WithAutonomous(false, reportagent.AutonomousConfig{MaxRounds: 4})
	exec := &fakeExec{fn: func(context.Context, string, json.RawMessage) (tools.Result, error) {
		return tools.Result{Tool: "search_turns", Status: tools.StatusOK}, nil
	}}

	var events []recordedEvent
	out, err := builder.GenerateWithTools(context.Background(), bundle(), exec, sinkOf(&events))
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if out.ToolDegradedReason != reportagent.ReasonHeuristic {
		t.Fatalf("reason = %q", out.ToolDegradedReason)
	}
	if out.Attempts != 0 || exec.calls != 0 {
		t.Fatalf("heuristic path must not call the model or tools: attempts=%d calls=%d", out.Attempts, exec.calls)
	}
	if out.Degraded || len(out.Report.Sections) != 5 {
		t.Fatalf("heuristic report should be complete: %+v", out)
	}
}

func TestToolLoopEventOrder(t *testing.T) {
	client := llm.NewEinoClient(llm.Scripted("loop", toolAction("get_turn", `{"turn_id":"t2"}`), finalAction()), "fake")
	builder := reportagent.NewBuilder(client, "fake").WithAutonomous(true, reportagent.AutonomousConfig{MaxRounds: 4})
	store, sessionID := toolFixture(t)
	q := tools.NewQuery(sessionID, store.Repos(), tools.Options{})
	var events []recordedEvent

	out, err := builder.GenerateWithTools(context.Background(), bundle(), q, sinkOf(&events))
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].kind != reportagent.EventToolRound {
		t.Fatalf("first event = %q", events[0].kind)
	}
	if events[1].kind != reportagent.EventToolCall {
		t.Fatalf("second event = %q", events[1].kind)
	}
	// The builder never emits report.section; the app streams sections after
	// the tool phase. Anything arriving here must be a tool-phase event.
	for _, ev := range events {
		if ev.kind == "report.section" {
			t.Fatalf("section emitted inside the tool phase: %+v", events)
		}
	}
	if out.Degraded || len(out.Report.Sections) != 5 || len(out.Report.ToolCallsDetail) == 0 {
		t.Fatalf("outcome = %+v", out)
	}
}

func hasEvent(events []recordedEvent, kind, reason string) bool {
	for _, ev := range events {
		if ev.kind == kind && ev.payload["reason"] == reason {
			return true
		}
	}
	return false
}

func hasEventStatus(events []recordedEvent, kind, status string) bool {
	for _, ev := range events {
		if ev.kind == kind && ev.payload["status"] == status {
			return true
		}
	}
	return false
}

func hasEventTruncated(events []recordedEvent) bool {
	for _, ev := range events {
		if ev.kind == reportagent.EventToolCall && ev.payload["truncated"] == true {
			return true
		}
	}
	return false
}
