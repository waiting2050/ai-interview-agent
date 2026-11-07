package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/waiting2050/ai-interview-agent/internal/agent/report"
	"github.com/waiting2050/ai-interview-agent/internal/agent/tools"
	"github.com/waiting2050/ai-interview-agent/internal/domain/domainpack"
	domainreport "github.com/waiting2050/ai-interview-agent/internal/domain/report"
	"github.com/waiting2050/ai-interview-agent/internal/domain/review"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/platform/idgen"
	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

// ErrReportNotReady marks a session whose report has not been generated.
var ErrReportNotReady = errors.New("report not ready")

const (
	stepCollect  = "collect"
	stepGenerate = "generate"
)

type FinishCommand struct {
	SessionID      string
	IdempotencyKey string
}

type FinishResult struct {
	RunID       string
	Status      session.RunStatus
	Replayed    bool
	ReportReady bool
	ReportID    string
}

// Finish closes an active interview (if needed) and starts report generation
// in one state transition: active/awaiting_answer -> reporting. Repeated
// calls replay the same report run or return the already persisted report.
func (s *Service) Finish(ctx context.Context, cmd FinishCommand) (FinishResult, error) {
	return s.startReport(ctx, cmd, true)
}

// Report generates the report for a session that has already ended (for
// example after the plan was exhausted). It refuses to cut an interview short.
func (s *Service) Report(ctx context.Context, cmd FinishCommand) (FinishResult, error) {
	return s.startReport(ctx, cmd, false)
}

func (s *Service) startReport(ctx context.Context, cmd FinishCommand, allowAwaiting bool) (FinishResult, error) {
	if cmd.SessionID == "" {
		return FinishResult{}, fmt.Errorf("%w: session_id is required", ErrInvalidArgument)
	}
	ctx = reqctx.WithSession(ctx, cmd.SessionID)
	unlock, err := s.acquireSession(ctx, cmd.SessionID)
	if err != nil {
		return FinishResult{}, err
	}
	defer unlock()

	key := cmd.IdempotencyKey
	if key == "" {
		key = deriveIdempotencyKey(session.RunKindReport, cmd.SessionID, nil)
	}
	hash := requestHash(session.RunKindReport, cmd.SessionID, nil)

	if existing, err := s.store.Repos().Runs.GetByIdempotencyKey(ctx, cmd.SessionID, session.RunKindReport, key); err == nil {
		if existing.RequestHash != hash {
			return FinishResult{}, ErrIdempotencyReused
		}
		return s.reportResult(ctx, existing)
	} else if !errors.Is(err, ports.ErrNotFound) {
		return FinishResult{}, fmt.Errorf("lookup report idempotency key: %w", err)
	}

	// A persisted report is the strongest idempotency signal: return it
	// regardless of which key the duplicate request used.
	if rec, err := s.store.Repos().Reports.GetBySession(ctx, cmd.SessionID); err == nil {
		return FinishResult{
			RunID:       rec.RunID,
			Status:      session.RunStatusSucceeded,
			Replayed:    true,
			ReportReady: true,
			ReportID:    rec.ID,
		}, nil
	} else if !errors.Is(err, ports.ErrNotFound) {
		return FinishResult{}, fmt.Errorf("lookup report: %w", err)
	}

	sess, err := s.store.Repos().Sessions.Get(ctx, cmd.SessionID)
	if err != nil {
		return FinishResult{}, err
	}
	switch sess.Status {
	case session.StatusFailed, session.StatusAborted, session.StatusCreated:
		return FinishResult{}, fmt.Errorf("%w: session is %s, report requires an active or reporting session",
			ports.ErrStateConflict, sess.Status)
	}

	if running, err := s.store.Repos().Runs.GetRunningBySession(ctx, cmd.SessionID); err == nil {
		if running.Kind == session.RunKindReport {
			return s.reportResult(ctx, running)
		}
		return FinishResult{}, &RunInProgressError{RunID: running.ID}
	} else if !errors.Is(err, ports.ErrNotFound) {
		return FinishResult{}, fmt.Errorf("check running run: %w", err)
	}

	now := s.now().UTC()
	run := &session.Run{
		ID:             idgen.New(),
		SessionID:      cmd.SessionID,
		Kind:           session.RunKindReport,
		Status:         session.RunStatusRunning,
		IdempotencyKey: key,
		RequestHash:    hash,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	var (
		created  bool
		existing *session.Run
	)
	err = s.store.WithTx(ctx, func(ctx context.Context, repos ports.Repositories) error {
		sess, err := repos.Sessions.Get(ctx, cmd.SessionID)
		if err != nil {
			return fmt.Errorf("load session for report: %w", err)
		}

		transitioned := false
		expected := sess.StateVersion
		switch sess.Status {
		case session.StatusActive:
			if !allowAwaiting && sess.Phase != session.PhaseReadyToFinalize {
				return fmt.Errorf("%w: session is still awaiting answers, finish it first", ports.ErrStateConflict)
			}
			if sess.Phase != session.PhaseReadyToFinalize {
				if err := sess.Finish(now); err != nil {
					return err
				}
			}
			if err := sess.BeginReport(); err != nil {
				return err
			}
			sess.UpdatedAt = now
			if err := repos.Sessions.UpdateState(ctx, sess, expected); err != nil {
				return err
			}
			transitioned = true
		case session.StatusReporting:
			// A previous report run crashed after transitioning the session.
		default:
			return fmt.Errorf("%w: session is %s, cannot start report",
				ports.ErrStateConflict, sess.Status)
		}

		created, existing, err = repos.Runs.Create(ctx, run)
		if err != nil {
			return err
		}
		if !created {
			return nil
		}
		if transitioned {
			if err := appendEvent(ctx, s.logger(ctx), repos.Events, sess.ID, run.ID, session.EventSessionPhase, map[string]any{
				"status":        string(sess.Status),
				"phase":         "",
				"state_version": sess.StateVersion,
			}); err != nil {
				return err
			}
		}
		if err := appendEvent(ctx, s.logger(ctx), repos.Events, sess.ID, run.ID, session.EventRunStarted, map[string]any{
			"run_id": run.ID,
			"kind":   string(run.Kind),
		}); err != nil {
			return err
		}
		return appendEvent(ctx, s.logger(ctx), repos.Events, sess.ID, run.ID, session.EventReportStarted, map[string]any{
			"run_id": run.ID,
		})
	})
	if err != nil {
		return FinishResult{}, fmt.Errorf("start report run: %w", err)
	}
	if !created {
		if existing == nil || existing.RequestHash != hash {
			return FinishResult{}, ErrIdempotencyReused
		}
		return s.reportResult(ctx, existing)
	}

	s.logger(ctx).Info("report run accepted", "run_id", run.ID)
	s.launch(ctx, run.ID)
	return FinishResult{RunID: run.ID, Status: session.RunStatusRunning}, nil
}

func (s *Service) reportResult(ctx context.Context, run *session.Run) (FinishResult, error) {
	if run.Status == session.RunStatusRunning {
		return FinishResult{RunID: run.ID, Status: run.Status, Replayed: true}, nil
	}
	result := FinishResult{RunID: run.ID, Status: run.Status, Replayed: true}
	if rec, err := s.store.Repos().Reports.GetBySession(ctx, run.SessionID); err == nil {
		result.ReportReady = true
		result.ReportID = rec.ID
	}
	return result, nil
}

// GetReport returns the persisted report or ErrReportNotReady.
func (s *Service) GetReport(ctx context.Context, sessionID string) (*domainreport.Record, error) {
	rec, err := s.store.Repos().Reports.GetBySession(ctx, sessionID)
	if errors.Is(err, ports.ErrNotFound) {
		return nil, fmt.Errorf("%w: session %s", ErrReportNotReady, sessionID)
	}
	return rec, err
}

// executeReport collects evidence through read-only tools, asks the generator
// for commentary and commits the report. Model failures degrade instead of
// failing the run; only storage or context errors fail it.
func (r *Runner) executeReport(ctx context.Context, run *session.Run) error {
	sess, err := r.store.Repos().Sessions.Get(ctx, run.SessionID)
	if err != nil {
		r.failRun(ctx, run, "load_session", err)
		return fmt.Errorf("load session: %w", err)
	}
	switch {
	case sess.Status == session.StatusReporting:
	case sess.Status == session.StatusActive && sess.Phase == session.PhaseReadyToFinalize:
	case sess.Status == session.StatusCompleted:
	default:
		r.failRun(ctx, run, "load_session", fmt.Errorf("%w: session is %s/%s, cannot generate report",
			ports.ErrStateConflict, sess.Status, sess.Phase))
		return fmt.Errorf("%w: session is %s/%s, cannot generate report",
			ports.ErrStateConflict, sess.Status, sess.Phase)
	}

	// Exactly one report per session: a replay of an already persisted report
	// only needs to mark the run succeeded.
	if _, err := r.store.Repos().Reports.GetBySession(ctx, run.SessionID); err == nil {
		now := r.now().UTC()
		if err := markRunSucceeded(ctx, r.store.Repos(), run, now); err != nil {
			return fmt.Errorf("mark replayed report run: %w", err)
		}
		return nil
	}

	evidence, err := recordStep(ctx, r.store, r.log, r.now, run.ID, stepCollect, func(ctx context.Context) (tools.Bundle, error) {
		pack := domainpack.Default().MustGet(sess.Domain)
		opts := tools.Options{}
		if pack != nil {
			opts.Rubric = pack.EvaluationRubric()
		}
		q := tools.NewQuery(run.SessionID, r.store.Repos(), opts)
		return q.Collect(ctx)
	})
	if err != nil {
		r.failRun(ctx, run, stepCollect, err)
		return err
	}

	outcome, err := recordStep(ctx, r.store, r.log, r.now, run.ID, stepGenerate, func(ctx context.Context) (report.Outcome, error) {
		out, err := r.reports.Generate(ctx, evidence)
		if err == nil && out.Degraded {
			r.metrics.IncReportDegraded(out.ErrorCode)
		}
		return out, err
	})
	if err != nil {
		r.failRun(ctx, run, stepGenerate, err)
		return err
	}

	if err := r.commitReport(ctx, run, outcome); err != nil {
		r.failRun(ctx, run, "commit", err)
		return err
	}

	if outcome.Degraded {
		r.logger(ctx).Warn("report run degraded",
			"error_code", outcome.ErrorCode,
			"validation_issues", outcome.ValidationIssues,
			"attempts", outcome.Attempts,
			"tool_calls", evidence.Calls)
	} else {
		r.logger(ctx).Info("report run succeeded", "degraded", false,
			"validation_warnings", outcome.ValidationIssues,
			"attempts", outcome.Attempts,
			"tool_calls", evidence.Calls, "sections", len(outcome.Report.Sections))
	}
	return nil
}

func (r *Runner) commitReport(ctx context.Context, run *session.Run, outcome report.Outcome) error {
	now := r.now().UTC()
	doc := outcome.Report
	doc.Sections = doc.RenderSections()
	// The report is the authoritative per-session usage snapshot. It uses the
	// same model_calls / *_breakdown naming as the M4 tool_calls fields.
	usage := r.usage.Snapshot(run.SessionID)
	doc.ModelCalls = usage.ModelCalls
	doc.ModelCallBreakdown = usage.ModelCallBreakdown
	doc.PromptTokens = usage.PromptTokens
	doc.CompletionTokens = usage.CompletionTokens
	doc.TotalTokens = usage.TotalTokens
	markdown := doc.RenderMarkdown()

	return r.store.WithTx(ctx, func(ctx context.Context, repos ports.Repositories) error {
		sess, err := repos.Sessions.Get(ctx, run.SessionID)
		if err != nil {
			return fmt.Errorf("load session for report commit: %w", err)
		}

		if _, err := repos.Reports.GetBySession(ctx, run.SessionID); err == nil {
			// A concurrent or replayed commit already persisted the report.
			return markRunSucceeded(ctx, repos, run, now)
		}

		expected := sess.StateVersion
		switch sess.Status {
		case session.StatusActive:
			if sess.Phase != session.PhaseReadyToFinalize {
				return fmt.Errorf("%w: session is %s/%s, cannot complete report",
					ports.ErrStateConflict, sess.Status, sess.Phase)
			}
			if err := sess.BeginReport(); err != nil {
				return err
			}
		case session.StatusReporting:
		default:
			return fmt.Errorf("%w: session is %s, cannot complete report",
				ports.ErrStateConflict, sess.Status)
		}
		if err := sess.Complete(now); err != nil {
			return err
		}
		if err := repos.Sessions.UpdateState(ctx, sess, expected); err != nil {
			return err
		}

		record := &domainreport.Record{
			ID:        idgen.New(),
			SessionID: sess.ID,
			RunID:     run.ID,
			Status:    "completed",
			Report:    doc,
			Markdown:  markdown,
			CreatedAt: now,
			UpdatedAt: now,
		}
		created, _, err := repos.Reports.Create(ctx, record)
		if err != nil {
			return fmt.Errorf("persist report: %w", err)
		}
		if !created {
			return markRunSucceeded(ctx, repos, run, now)
		}

		// M11: weak points become review cards in the same transaction that
		// persists the report. Card creation is deterministic and model-free.
		if cards := review.BuildCards(sess.ID, &doc, now); len(cards) > 0 {
			for _, card := range cards {
				card.UserID = ownerOrLegacy(sess.OwnerID)
			}
			if err := repos.Reviews.SaveMany(ctx, cards); err != nil {
				return fmt.Errorf("persist review cards: %w", err)
			}
		}

		for _, section := range doc.Sections {
			payload := map[string]any{
				"index":      section.Index,
				"title":      section.Title,
				"markdown":   section.Markdown,
				"tool_calls": section.ToolCalls,
				"degraded":   doc.Degraded,
			}
			if len(section.TurnRefs) > 0 {
				payload["turn_refs"] = section.TurnRefs
			}
			if err := appendEvent(ctx, r.logger(ctx), repos.Events, sess.ID, run.ID, session.EventReportSection, payload); err != nil {
				return err
			}
		}
		if err := appendEvent(ctx, r.logger(ctx), repos.Events, sess.ID, run.ID, session.EventReportToolCalls, map[string]any{
			"count":     doc.ToolCalls,
			"breakdown": doc.ToolBreakdown,
		}); err != nil {
			return err
		}
		completed := map[string]any{
			"report_id":            record.ID,
			"overall_score":        doc.OverallScore,
			"degraded":             doc.Degraded,
			"error_code":           doc.ErrorCode,
			"tool_calls":           doc.ToolCalls,
			"model_name":           doc.ModelName,
			"model_calls":          doc.ModelCalls,
			"model_call_breakdown": doc.ModelCallBreakdown,
			"prompt_tokens":        doc.PromptTokens,
			"completion_tokens":    doc.CompletionTokens,
			"total_tokens":         doc.TotalTokens,
		}
		if doc.ErrorDetail != "" {
			completed["error_detail"] = doc.ErrorDetail
		}
		if err := appendEvent(ctx, r.logger(ctx), repos.Events, sess.ID, run.ID, session.EventReportCompleted, completed); err != nil {
			return err
		}
		if err := appendEvent(ctx, r.logger(ctx), repos.Events, sess.ID, run.ID, session.EventSessionPhase, map[string]any{
			"status":        string(sess.Status),
			"phase":         "",
			"state_version": sess.StateVersion,
		}); err != nil {
			return err
		}
		if err := appendEvent(ctx, r.logger(ctx), repos.Events, sess.ID, run.ID, session.EventSessionCompleted, map[string]any{
			"report_id":     record.ID,
			"overall_score": doc.OverallScore,
			"degraded":      doc.Degraded,
		}); err != nil {
			return err
		}
		return markRunSucceeded(ctx, repos, run, now)
	})
}
