package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/agent/report"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/platform/idgen"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
	"github.com/waiting2050/ai-interview-agent/internal/platform/trace"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

const runErrorStepFailed = "step_failed"

// Runner executes a StartRun node graph. Nodes never write business state
// directly: each node product is stored in run_steps, and the session projection
// plus SSE events are committed together at the end.
type Runner struct {
	store       ports.Store
	steps       StartSteps
	answerSteps AnswerSteps
	answerGraph *interview.AnswerGraph
	reports     report.Generator
	now         func() time.Time
	log         *slog.Logger
	metrics     *obs.Metrics
	usage       *obs.UsageTracker
	tracer      *trace.Tracer
	maxFailures int
}

func NewRunner(store ports.Store, steps StartSteps, answerSteps AnswerSteps, answerGraph *interview.AnswerGraph, reports report.Generator, now func() time.Time, log *slog.Logger, metrics *obs.Metrics, usage *obs.UsageTracker, tracer *trace.Tracer, maxFailures int) *Runner {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	if metrics == nil {
		metrics = obs.NewMetrics()
	}
	if usage == nil {
		usage = obs.NewUsageTracker()
	}
	if maxFailures <= 0 {
		maxFailures = 3
	}
	return &Runner{
		store: store, steps: steps, answerSteps: answerSteps, answerGraph: answerGraph,
		reports: reports, now: now, log: log, metrics: metrics, usage: usage,
		tracer: tracer, maxFailures: maxFailures,
	}
}

func (r *Runner) Execute(ctx context.Context, runID string) error {
	return r.execute(ctx, runID, "")
}

// ExecuteClaimed runs a run already leased by a worker. The claim incremented
// the attempt, so this path must not call MarkRunning again; it also fences
// the terminal write with the lease id.
func (r *Runner) ExecuteClaimed(ctx context.Context, runID, leaseID string) error {
	return r.execute(ctx, runID, leaseID)
}

func (r *Runner) execute(ctx context.Context, runID, leaseID string) (runErr error) {
	run, err := r.store.Repos().Runs.Get(ctx, runID)
	if err != nil {
		return fmt.Errorf("load run %s: %w", runID, err)
	}
	if run.Status == session.RunStatusSucceeded {
		return nil
	}
	if run.Status == session.RunStatusDead {
		return fmt.Errorf("run %s is dead-lettered", runID)
	}

	// From here on every step, event and model call shares the run identity.
	if run.TraceID != "" {
		ctx = reqctx.WithTrace(ctx, run.TraceID)
	}
	ctx = reqctx.WithSession(ctx, run.SessionID)
	ctx = reqctx.WithRun(ctx, run.ID)
	ctx, endSpan := r.tracer.Start(ctx, "run."+string(run.Kind), map[string]any{
		"run.kind":    string(run.Kind),
		"run.attempt": run.Attempt,
	})
	defer func() { endSpan(runErr) }()

	now := r.now().UTC()
	if leaseID == "" {
		if err := r.store.Repos().Runs.MarkRunning(ctx, run.ID, run.Attempt+1, now); err != nil {
			return fmt.Errorf("mark run running: %w", err)
		}
		run.Attempt++
	} else if run.LeaseID != leaseID {
		return fmt.Errorf("run %s is owned by another worker", runID)
	}
	r.logger(ctx).Info("run executing", "kind", string(run.Kind), "attempt", run.Attempt)

	switch run.Kind {
	case session.RunKindStart:
		return r.executeStart(ctx, run)
	case session.RunKindAnswer:
		return r.executeAnswer(ctx, run)
	case session.RunKindReport:
		return r.executeReport(ctx, run)
	default:
		r.failRun(ctx, run, "dispatch", fmt.Errorf("unsupported run kind %q", run.Kind))
		return fmt.Errorf("unsupported run kind %q", run.Kind)
	}
}

// markRunSucceeded is the shared terminal write for all run kinds. Runs leased
// by a worker use the fenced variant, so a stale owner cannot mark succeeded
// after another worker reclaimed the run.
func markRunSucceeded(ctx context.Context, repos ports.Repositories, run *session.Run, at time.Time) error {
	if run.LeaseID != "" {
		return repos.Runs.MarkFinishedOwned(ctx, run.ID, run.LeaseID, session.RunStatusSucceeded, "", at)
	}
	return repos.Runs.MarkFinished(ctx, run.ID, session.RunStatusSucceeded, "", at)
}

func (r *Runner) executeStart(ctx context.Context, run *session.Run) error {
	sess, err := r.store.Repos().Sessions.Get(ctx, run.SessionID)
	if err != nil {
		r.failRun(ctx, run, "load_session", err)
		return fmt.Errorf("load session: %w", err)
	}

	in := StartInput{
		SessionID:  sess.ID,
		TargetRole: sess.TargetRole,
		ResumeText: sess.ResumeText,
		Profile:    sess.ResumeProfile,
		JDText:     sess.JDText,
		Domain:     sess.Domain,
		Config:     sess.Config,
	}

	plan, err := recordStep(ctx, r.store, r.log, r.now, run.ID, stepPlan, func(ctx context.Context) (PlanResult, error) {
		return r.steps.Plan(ctx, in)
	})
	if err != nil {
		r.failRun(ctx, run, stepPlan, err)
		return err
	}

	ask, err := recordStep(ctx, r.store, r.log, r.now, run.ID, stepAsk, func(ctx context.Context) (AskResult, error) {
		return r.steps.Ask(ctx, in, plan)
	})
	if err != nil {
		r.failRun(ctx, run, stepAsk, err)
		return err
	}

	if err := r.commitStart(ctx, run, plan, ask); err != nil {
		r.failRun(ctx, run, "commit", err)
		return err
	}

	r.logger(reqctx.WithTurn(ctx, ask.TurnID)).Info("run succeeded")
	return nil
}

// commitStart atomically projects the node products into the session and appends
// the corresponding SSE events.
func (r *Runner) commitStart(ctx context.Context, run *session.Run, plan PlanResult, ask AskResult) error {
	now := r.now().UTC()

	return r.store.WithTx(ctx, func(ctx context.Context, repos ports.Repositories) error {
		sess, err := repos.Sessions.Get(ctx, run.SessionID)
		if err != nil {
			return fmt.Errorf("load session for commit: %w", err)
		}

		switch {
		case sess.Status == session.StatusCreated:
			expected := sess.StateVersion
			if err := sess.Start(now); err != nil {
				return err
			}
			sess.PendingTurnID = ask.TurnID
			sess.PlanCursor = 1
			sess.UpdatedAt = now
			if err := repos.Sessions.UpdateState(ctx, sess, expected); err != nil {
				return fmt.Errorf("update session state: %w", err)
			}

			items := make([]session.PlanItem, 0, len(plan.Items))
			for i, item := range plan.Items {
				items = append(items, session.PlanItem{
					ID:             idgen.New(),
					SessionID:      sess.ID,
					Seq:            i + 1,
					Topic:          item.Topic,
					Objective:      item.Objective,
					ExpectedPoints: item.ExpectedPoints,
					Difficulty:     item.Difficulty,
					Status:         "pending",
				})
			}
			if err := repos.Plans.CreateItems(ctx, items); err != nil {
				return fmt.Errorf("create plan items: %w", err)
			}
			if err := createTurn(ctx, repos, ask.TurnID, sess.ID, 1, session.TurnKindPrimary, "",
				ask.Topic, 1, ask.Text, ask.ExpectedPoints, now); err != nil {
				return err
			}

			if err := appendEvent(ctx, r.logger(ctx), repos.Events, sess.ID, run.ID, session.EventSessionStarted, map[string]any{
				"first_turn_id": ask.TurnID,
			}); err != nil {
				return err
			}
			if err := appendEvent(ctx, r.logger(ctx), repos.Events, sess.ID, run.ID, session.EventQuestionAsked, map[string]any{
				"turn_id":     ask.TurnID,
				"seq":         1,
				"topic":       ask.Topic,
				"text":        ask.Text,
				"is_followup": false,
				"depth":       1,
			}); err != nil {
				return err
			}
			if err := appendEvent(ctx, r.logger(ctx), repos.Events, sess.ID, run.ID, session.EventSessionPhase, map[string]any{
				"status":        string(sess.Status),
				"phase":         string(sess.Phase),
				"state_version": sess.StateVersion,
			}); err != nil {
				return err
			}

		case sess.Status == session.StatusActive && sess.PendingTurnID == ask.TurnID:
			// A previous attempt committed the projection but did not mark the
			// run as succeeded. Do not append the events twice.

		default:
			return fmt.Errorf("%w: session %s is %s/%s, cannot commit start run",
				ports.ErrStateConflict, sess.ID, sess.Status, sess.Phase)
		}

		return markRunSucceeded(ctx, repos, run, now)
	})
}

func (r *Runner) failRun(ctx context.Context, run *session.Run, step string, cause error) {
	class := failureClass(cause)
	now := r.now().UTC()
	dead, failureCount, err := r.store.Repos().Runs.RecordFailure(ctx, run.ID, run.LeaseID, runErrorStepFailed, class, truncateError(cause), now, r.maxFailures)
	if err != nil {
		// A stale owner whose lease was reclaimed must not append a second
		// failure event; the repository fences the update by lease id.
		r.logger(ctx).Error("record run failure", "error", err)
		return
	}
	if err := appendEvent(ctx, r.logger(ctx), r.store.Repos().Events, run.SessionID, run.ID, session.EventRunFailed, map[string]any{
		"run_id":        run.ID,
		"step_name":     step,
		"code":          runErrorStepFailed,
		"retryable":     !dead,
		"error_class":   class,
		"failure_count": failureCount,
		"dead_letter":   dead,
	}); err != nil {
		r.logger(ctx).Error("append run failed event", "error", err)
	}
	r.metrics.IncRunFailure(string(run.Kind), class)
	if dead {
		r.metrics.IncDeadLetter(string(run.Kind), class)
		r.logger(ctx).Error("run dead-lettered", "step_name", step, "error_class", class,
			"failure_count", failureCount, "max_failures", r.maxFailures, "error", cause)
		return
	}
	r.logger(ctx).Error("run failed", "step_name", step, "error_class", class,
		"failure_count", failureCount, "max_failures", r.maxFailures, "error", cause)
}

// truncateError keeps last_error bounded in the runs table; the full error is
// still written to the execution log.
func truncateError(err error) string {
	if err == nil {
		return ""
	}
	const maxLen = 1024
	text := err.Error()
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen]
}

// failureClass maps an error to the low-cardinality label used by run metrics.
// LLM transport errors keep their llm.FailureClass; storage and state errors
// get application-level classes.
func failureClass(err error) string {
	if err == nil {
		return "unknown"
	}
	if class, _, _ := llm.Classify(err); class != llm.FailureUnknown {
		return string(class)
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ports.ErrStateConflict):
		return "state_conflict"
	case errors.Is(err, ports.ErrNotFound):
		return "not_found"
	case errors.Is(err, ports.ErrLockBusy):
		return "lock_busy"
	case errors.Is(err, ports.ErrLockBackend):
		return "lock_backend"
	default:
		return "internal"
	}
}

func lockErrorClass(err error) string {
	switch {
	case errors.Is(err, ports.ErrLockBusy):
		return "busy"
	case errors.Is(err, ports.ErrLockBackend):
		return "backend"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "internal"
	}
}

// recordStep implements node-level recovery: a succeeded step is decoded and
// replayed, otherwise the node runs and its product is persisted.
func recordStep[T any](
	ctx context.Context,
	store ports.Store,
	log *slog.Logger,
	now func() time.Time,
	runID, name string,
	fn func(ctx context.Context) (T, error),
) (T, error) {
	var zero T

	existing, err := store.Repos().Steps.Get(ctx, runID, name)
	switch {
	case err == nil && existing.Status == session.StepStatusSucceeded:
		var out T
		if err := json.Unmarshal(existing.Output, &out); err != nil {
			return zero, fmt.Errorf("decode cached step %s: %w", name, err)
		}
		loggerWith(log, ctx).Info("run step replayed", "step_name", name, "attempt", existing.Attempt)
		return out, nil
	case err != nil && !errors.Is(err, ports.ErrNotFound):
		return zero, fmt.Errorf("load run step %s: %w", name, err)
	}

	attempt := 1
	if existing != nil {
		attempt = existing.Attempt + 1
	}

	at := now().UTC()
	if err := store.Repos().Runs.SetLastStep(ctx, runID, name); err != nil {
		return zero, fmt.Errorf("set last step: %w", err)
	}
	if err := store.Repos().Steps.Start(ctx, &session.RunStep{
		RunID:     runID,
		StepName:  name,
		Status:    session.StepStatusRunning,
		Attempt:   attempt,
		StartedAt: at,
	}); err != nil {
		return zero, fmt.Errorf("start step %s: %w", name, err)
	}

	loggerWith(log, ctx).Info("run step started", "step_name", name, "attempt", attempt)
	out, err := fn(ctx)
	if err != nil {
		if failErr := store.Repos().Steps.Fail(ctx, runID, name, runErrorStepFailed, now().UTC()); failErr != nil {
			loggerWith(log, ctx).Error("mark step failed", "step_name", name, "error", failErr)
		}
		return zero, err
	}

	body, err := json.Marshal(out)
	if err != nil {
		return zero, fmt.Errorf("marshal step %s output: %w", name, err)
	}
	if err := store.Repos().Steps.Complete(ctx, runID, name, body, now().UTC()); err != nil {
		return zero, fmt.Errorf("complete step %s: %w", name, err)
	}

	loggerWith(log, ctx).Info("run step succeeded", "step_name", name, "attempt", attempt)
	return out, nil
}

// appendEvent persists one SSE event and logs its identity. The payload is
// never logged: it can contain answer text.
func appendEvent(ctx context.Context, log *slog.Logger, events ports.EventRepository, sessionID, runID, eventType string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event %s: %w", eventType, err)
	}
	event := &session.Event{
		SessionID: sessionID,
		RunID:     runID,
		Type:      eventType,
		Payload:   body,
		CreatedAt: time.Now().UTC(),
	}
	if err := events.Append(ctx, event); err != nil {
		return fmt.Errorf("append event %s: %w", eventType, err)
	}

	attrs := []any{"event_type", eventType, "event_id", event.ID}
	if turnID, ok := payload["turn_id"].(string); ok && turnID != "" {
		attrs = append(attrs, "turn_id", turnID)
	}
	loggerWith(log, ctx).Info("event appended", attrs...)
	return nil
}

func loggerWith(log *slog.Logger, ctx context.Context) *slog.Logger {
	if log == nil {
		log = slog.Default()
	}
	if attrs := reqctx.Attrs(ctx); len(attrs) > 0 {
		return log.With(attrs...)
	}
	return log
}

func (r *Runner) logger(ctx context.Context) *slog.Logger {
	return loggerWith(r.log, ctx)
}
