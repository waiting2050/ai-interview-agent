package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	"github.com/waiting2050/ai-interview-agent/internal/domain/followup"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

// decideAnswer maps the snapshot plus model output into the rule gates.
func decideAnswer(snap AnswerSnapshot, out interview.Outcome) (followup.Decision, error) {
	suggestion := out.Result.Followup

	return followup.Decide(followup.Input{
		Elapsed:    snap.Elapsed,
		MaxElapsed: time.Duration(snap.Config.DurationMinutes) * time.Minute,

		PlanCursor:         snap.PlanCursor,
		RemainingPrimaries: len(snap.PlanItems) - snap.PlanCursor,

		Depth:                   snap.Depth,
		MaxDepth:                snap.Config.MaxDepth,
		FollowupsThisQuestion:   snap.FollowupsThisQuestion,
		MaxFollowupsPerQuestion: snap.Config.MaxFollowupsPerQuestion,
		FollowupsUsed:           snap.FollowupsUsed,
		MaxTotalFollowups:       snap.Config.MaxTotalFollowups,

		AnswerUnknown:         followup.IsUnknownAnswer(snap.Answer),
		CoveredAllPoints:      coveredAllPoints(snap.ExpectedPoints, out.Result.Evidence),
		EvaluationDegraded:    out.Degraded,
		SuggestFollowup:       suggestion.Needed,
		SuggestTarget:         suggestion.Target,
		SuggestionHasEvidence: len(out.Result.Evidence) > 0,
	}), nil
}

// coveredAllPoints is intentionally conservative: without expected points a
// question cannot be considered covered, and evidence reasons must explicitly
// mention each expected point. Matching ignores whitespace and punctuation so
// "goroutine 调度与 G-M-P" still matches "goroutine调度与GMP".
func coveredAllPoints(expected []string, evidence []evaluation.Evidence) bool {
	if len(expected) == 0 {
		return false
	}
	for _, point := range expected {
		normalizedPoint := normalizeCoverage(point)
		if normalizedPoint == "" {
			return false
		}
		covered := false
		for _, e := range evidence {
			if strings.Contains(normalizeCoverage(e.Reason), normalizedPoint) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

func normalizeCoverage(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func nextItem(snap AnswerSnapshot) (interview.PlanItemView, bool) {
	if snap.PlanCursor < 0 || snap.PlanCursor >= len(snap.PlanItems) {
		return interview.PlanItemView{}, false
	}
	return snap.PlanItems[snap.PlanCursor], true
}

func (r *Runner) commitAnswer(
	ctx context.Context,
	run *session.Run,
	snap AnswerSnapshot,
	outcome interview.Outcome,
	decision followup.Decision,
	ask AskResult,
	asked bool,
) error {
	now := r.now().UTC()

	return r.store.WithTx(ctx, func(ctx context.Context, repos ports.Repositories) error {
		sess, err := repos.Sessions.Get(ctx, run.SessionID)
		if err != nil {
			return fmt.Errorf("load session for commit: %w", err)
		}
		turn, err := repos.Turns.Get(ctx, snap.TurnID)
		if err != nil {
			return fmt.Errorf("load turn for commit: %w", err)
		}

		// The transaction below atomically answers the turn and advances the
		// session. If the turn is already answered, a previous attempt reached
		// the end and only the run status is missing.
		if turn.Status == session.TurnStatusAnswered {
			return repos.Runs.MarkFinished(ctx, run.ID, session.RunStatusSucceeded, "", now)
		}
		if sess.Status != session.StatusActive || sess.Phase != session.PhaseAwaitingAnswer ||
			sess.PendingTurnID != turn.ID {
			return fmt.Errorf("%w: session %s/%s pending=%s",
				ports.ErrStateConflict, sess.Status, sess.Phase, sess.PendingTurnID)
		}
		expected := sess.StateVersion

		turn.Status = session.TurnStatusAnswered
		turn.AnswerText = snap.Answer
		turn.AnswerHash = answerHash(snap.Answer)
		turn.AnsweredAt = &now
		turn.UpdatedAt = now
		if err := repos.Turns.UpdateAnswer(ctx, turn); err != nil {
			return fmt.Errorf("update turn answer: %w", err)
		}

		rec := buildEvaluationRecord(snap, outcome, now)
		if err := repos.Evaluations.Upsert(ctx, rec); err != nil {
			return err
		}

		if err := appendEvent(ctx, r.logger(ctx), repos.Events, sess.ID, run.ID, session.EventAnswerReceived, map[string]any{
			"turn_id":   snap.TurnID,
			"chars":     len([]rune(snap.Answer)),
			"truncated": snap.Truncated,
		}); err != nil {
			return err
		}
		if err := appendEvaluationEvent(ctx, r.logger(ctx), repos.Events, sess.ID, run.ID, snap.TurnID, rec, outcome); err != nil {
			return err
		}

		switch decision.Action {
		case followup.ActionFollowUp:
			if err := sess.AskFollowup(ask.TurnID, now); err != nil {
				return err
			}
			if err := createTurn(ctx, repos, ask.TurnID, sess.ID, snap.NextSeq, session.TurnKindFollowup, snap.TurnID,
				ask.Topic, snap.Depth+1, ask.Text, ask.ExpectedPoints, now); err != nil {
				return err
			}
		case followup.ActionNextQuestion:
			item, ok := nextItem(snap)
			if !ok {
				return fmt.Errorf("no plan item for cursor %d", snap.PlanCursor)
			}
			if err := sess.AskNextQuestion(ask.TurnID, now); err != nil {
				return err
			}
			if err := createTurn(ctx, repos, ask.TurnID, sess.ID, snap.NextSeq, session.TurnKindPrimary, "",
				item.Topic, 1, ask.Text, ask.ExpectedPoints, now); err != nil {
				return err
			}
		case followup.ActionFinish:
			if err := sess.Finish(now); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported decision action %q", decision.Action)
		}

		if err := appendEvent(ctx, r.logger(ctx), repos.Events, sess.ID, run.ID, session.EventDecisionMade, map[string]any{
			"turn_id":             snap.TurnID,
			"action":              string(decision.Action),
			"reason":              decision.Reason,
			"depth":               snap.Depth,
			"followups_used":      sess.FollowupsUsed,
			"plan_cursor":         sess.PlanCursor,
			"next_turn_id":        ask.TurnID,
			"next_question_asked": asked,
		}); err != nil {
			return err
		}

		// Emit the follow-up / next question as its own event so SSE clients can
		// render it without polling the session. The start run uses the same
		// payload shape.
		if asked && ask.TurnID != "" {
			isFollowup := decision.Action == followup.ActionFollowUp
			if err := appendEvent(ctx, r.logger(ctx), repos.Events, sess.ID, run.ID, session.EventQuestionAsked, map[string]any{
				"turn_id":     ask.TurnID,
				"seq":         snap.NextSeq,
				"topic":       ask.Topic,
				"text":        ask.Text,
				"is_followup": isFollowup,
				"depth":       ask.Depth,
			}); err != nil {
				return err
			}
		}

		if err := repos.Sessions.UpdateState(ctx, sess, expected); err != nil {
			return fmt.Errorf("update session state: %w", err)
		}

		if err := appendEvent(ctx, r.logger(ctx), repos.Events, sess.ID, run.ID, session.EventSessionPhase, map[string]any{
			"status":          string(sess.Status),
			"phase":           string(sess.Phase),
			"state_version":   sess.StateVersion,
			"pending_turn_id": sess.PendingTurnID,
		}); err != nil {
			return err
		}

		return repos.Runs.MarkFinished(ctx, run.ID, session.RunStatusSucceeded, "", now)
	})
}

func createTurn(
	ctx context.Context,
	repos ports.Repositories,
	id string,
	sessionID string,
	seq int,
	kind session.TurnKind,
	parent string,
	topic string,
	depth int,
	question string,
	expectedPoints []string,
	now time.Time,
) error {
	turn := &session.Turn{
		ID:             id,
		SessionID:      sessionID,
		Seq:            seq,
		Kind:           kind,
		ParentTurnID:   parent,
		Topic:          topic,
		Depth:          depth,
		Question:       question,
		ExpectedPoints: expectedPoints,
		Status:         session.TurnStatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := repos.Turns.Create(ctx, turn); err != nil {
		return fmt.Errorf("create next turn: %w", err)
	}
	return nil
}

func buildEvaluationRecord(snap AnswerSnapshot, outcome interview.Outcome, now time.Time) *evaluation.Record {
	rec := &evaluation.Record{
		TurnID:        snap.TurnID,
		SessionID:     snap.SessionID,
		SchemaVersion: outcome.Result.SchemaVersion,
		Scores:        outcome.Result.Scores,
		Bonus:         outcome.Result.Bonus,
		Penalty:       outcome.Result.Penalty,
		Evidence:      outcome.Result.Evidence,
		MissingPoints: outcome.Result.MissingPoints,
		Confidence:    outcome.Result.Confidence,
		ModelName:     outcome.ModelName,
		PromptVersion: outcome.PromptVersion,
		RawOutput:     truncateRaw(outcome.RawOutput, 2000),
		Degraded:      outcome.Degraded,
		ErrorCode:     outcome.ErrorCode,
		CreatedAt:     now,
	}
	if outcome.Degraded || len(rec.Scores) == 0 {
		rec.Degraded = true
		rec.SchemaVersion = evaluation.SchemaVersion
		rec.Scores = map[evaluation.Dimension]int{}
		if rec.ErrorCode == "" {
			rec.ErrorCode = interview.ErrorCodeModelOutputInvalid
		}
		return rec
	}
	score := evaluation.ComputeScore(rec.Scores, rec.Bonus, rec.Penalty)
	rec.BaseScore = score.Base
	rec.WeightedScore = score.Total
	return rec
}

func appendEvaluationEvent(
	ctx context.Context,
	log *slog.Logger,
	events ports.EventRepository,
	sessionID, runID, turnID string,
	rec *evaluation.Record,
	outcome interview.Outcome,
) error {
	if rec.Degraded {
		return appendEvent(ctx, log, events, sessionID, runID, session.EventEvaluationFailed, map[string]any{
			"turn_id":    turnID,
			"code":       rec.ErrorCode,
			"attempts":   outcome.Attempts,
			"raw_output": truncateRaw(outcome.RawOutput, 800),
		})
	}
	return appendEvent(ctx, log, events, sessionID, runID, session.EventEvaluationDone, map[string]any{
		"turn_id":        turnID,
		"base_score":     rec.BaseScore,
		"total_score":    rec.WeightedScore,
		"scores":         rec.Scores,
		"confidence":     rec.Confidence,
		"missing_points": rec.MissingPoints,
		"model":          rec.ModelName,
		"prompt_version": rec.PromptVersion,
	})
}

func answerHash(answer string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(answer)))
	return hex.EncodeToString(sum[:])
}

func truncateRaw(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
