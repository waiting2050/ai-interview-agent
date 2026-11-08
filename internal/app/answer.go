package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/domain/domainpack"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	"github.com/waiting2050/ai-interview-agent/internal/domain/followup"
	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/platform/idgen"
	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

const (
	stepSnapshot = "snapshot"
	stepEvaluate = "evaluate"
	stepDecide   = "decide"
	stepAskNext  = "ask"
)

// maxAnswerRunes caps a single answer so one turn cannot blow up the model
// context. Longer answers are truncated and the event records it.
const maxAnswerRunes = 6000

// memorySummaryTimeout bounds the best-effort summary call after a committed
// answer. It never runs on every turn, only when the builder threshold is hit.
const memorySummaryTimeout = 20 * time.Second

// AnswerInput is the persisted command payload of an answer run.
type AnswerInput struct {
	TurnID string `json:"turn_id"`
	Answer string `json:"answer"`
	// Truncated is true when the original answer exceeded maxAnswerRunes.
	Truncated bool `json:"truncated"`
}

// AnswerSnapshot is the read-only product of the graph snapshot node.
type AnswerSnapshot = interview.TurnSnapshot

// Evaluator is the model-facing evaluation step. *interview.Evaluator satisfies
// it; tests inject a fake.
type Evaluator interface {
	Evaluate(ctx context.Context, in interview.EvaluateInput) (interview.Outcome, error)
}

// AnswerSteps makes every answer-graph node replaceable in tests. The graph
// itself lives in the interview package; these are the node bodies.
type AnswerSteps struct {
	Evaluate    func(ctx context.Context, snap AnswerSnapshot) (interview.Outcome, error)
	AskFollowup func(ctx context.Context, snap AnswerSnapshot, out interview.Outcome, decision followup.Decision) (interview.Question, error)
	AskNext     func(ctx context.Context, snap AnswerSnapshot, item interview.PlanItemView) (interview.Question, error)
}

func DefaultAnswerSteps(evaluator Evaluator, generator *interview.QuestionGenerator) AnswerSteps {
	if generator == nil {
		generator = interview.NewQuestionGenerator(interview.NewHeuristicClient(), "fake-heuristic")
	}
	return AnswerSteps{
		Evaluate: func(ctx context.Context, snap AnswerSnapshot) (interview.Outcome, error) {
			if evaluator == nil {
				return interview.Outcome{}, fmt.Errorf("evaluator is not configured")
			}
			return evaluator.Evaluate(ctx, interview.EvaluateInput{
				SessionID:      snap.SessionID,
				TurnID:         snap.TurnID,
				Topic:          snap.Topic,
				Question:       snap.Question,
				Answer:         snap.Answer,
				TargetRole:     snap.TargetRole,
				Difficulty:     snap.Difficulty,
				ResumeText:     snap.ResumeText,
				JDText:         snap.JDText,
				ExpectedPoints: snap.ExpectedPoints,
				Rubric:         domainpack.Default().MustGet(snap.Domain).EvaluationRubric(),
			})
		},
		AskFollowup: func(ctx context.Context, snap AnswerSnapshot, out interview.Outcome, decision followup.Decision) (interview.Question, error) {
			points := cleanPoints(out.Result.MissingPoints)
			if len(points) == 0 {
				points = snap.ExpectedPoints
			}
			objective := strings.TrimSpace(decision.Target)
			if objective == "" {
				objective = "回答中未覆盖的关键点"
			}
			outcome, err := generator.Generate(ctx, interview.QuestionInput{
				Kind:              "followup",
				TargetRole:        snap.TargetRole,
				Difficulty:        snap.Difficulty,
				JDText:            snap.JDText,
				Profile:           snap.Profile,
				Topic:             snap.Topic,
				Objective:         objective,
				ExpectedPoints:    points,
				History:           snap.AskedQuestions,
				FollowupTarget:    objective,
				LastQuestion:      snap.Question,
				Answer:            snap.Answer,
				MemorySummary:     snap.MemorySummary,
				MemoryRecentTurns: snap.MemoryRecentTurns,
			})
			if err != nil {
				return interview.Question{}, err
			}
			return interview.Question{
				TurnID:         idgen.New(),
				Topic:          snap.Topic,
				Text:           outcome.Text,
				Depth:          snap.Depth + 1,
				Objective:      objective,
				ExpectedPoints: points,
			}, nil
		},
		AskNext: func(ctx context.Context, snap AnswerSnapshot, item interview.PlanItemView) (interview.Question, error) {
			outcome, err := generator.Generate(ctx, interview.QuestionInput{
				Kind:              "primary",
				TargetRole:        snap.TargetRole,
				Difficulty:        snap.Difficulty,
				JDText:            snap.JDText,
				Profile:           snap.Profile,
				Topic:             item.Topic,
				Objective:         item.Objective,
				ExpectedPoints:    item.ExpectedPoints,
				History:           snap.AskedQuestions,
				MemorySummary:     snap.MemorySummary,
				MemoryRecentTurns: snap.MemoryRecentTurns,
			})
			if err != nil {
				return interview.Question{}, err
			}
			return interview.Question{
				TurnID:         idgen.New(),
				Topic:          item.Topic,
				Text:           outcome.Text,
				Depth:          1,
				Objective:      item.Objective,
				ExpectedPoints: item.ExpectedPoints,
			}, nil
		},
	}
}

func cleanPoints(points []string) []string {
	out := make([]string, 0, len(points))
	for _, point := range points {
		if point = strings.TrimSpace(point); point != "" {
			out = append(out, point)
		}
	}
	return out
}

// executeAnswer runs the compiled answer graph and commits the projection
// atomically. Node products are still persisted by run_steps through the
// dependencies built below, so recovery semantics are unchanged.
func (r *Runner) executeAnswer(ctx context.Context, run *session.Run) error {
	var in AnswerInput
	if err := json.Unmarshal(run.Input, &in); err != nil {
		r.failRun(ctx, run, stepSnapshot, fmt.Errorf("decode answer input: %w", err))
		return fmt.Errorf("decode answer input: %w", err)
	}
	ctx = reqctx.WithTurn(ctx, in.TurnID)

	state, err := r.answerGraph.Run(ctx, interview.TurnInput{
		SessionID: run.SessionID,
		TurnID:    in.TurnID,
		Answer:    in.Answer,
	}, r.answerDeps(run))
	if err != nil {
		step := interview.StepName(err)
		r.failRun(ctx, run, step, err)
		return err
	}

	if err := r.commitAnswer(ctx, run, state.Snapshot, state.Outcome, state.Decision, state.Question, state.Asked); err != nil {
		r.failRun(ctx, run, "commit", err)
		return err
	}

	r.updateMemory(ctx, run.SessionID)

	r.logger(ctx).Info("run succeeded", "action", string(state.Decision.Action),
		"reason", state.Decision.Reason)
	return nil
}

// answerDeps binds the graph nodes to this run, wrapping every node in the
// same run_steps recovery used before the graph rewrite.
func (r *Runner) answerDeps(run *session.Run) interview.AnswerDeps {
	return interview.AnswerDeps{
		Snapshot: func(ctx context.Context) (AnswerSnapshot, error) {
			return runRecordStep(r, ctx, run.ID, stepSnapshot, func(ctx context.Context) (AnswerSnapshot, error) {
				return r.buildSnapshot(ctx, run)
			})
		},
		Evaluate: func(ctx context.Context, snap AnswerSnapshot) (interview.Outcome, error) {
			return runRecordStep(r, ctx, run.ID, stepEvaluate, func(ctx context.Context) (interview.Outcome, error) {
				out, err := r.answerSteps.Evaluate(ctx, snap)
				if err == nil && out.Degraded {
					r.metrics.IncEvaluationDegraded(out.ErrorCode)
				}
				return out, err
			})
		},
		Decide: func(ctx context.Context, snap AnswerSnapshot, out interview.Outcome) (followup.Decision, error) {
			return runRecordStep(r, ctx, run.ID, stepDecide, func(context.Context) (followup.Decision, error) {
				return decideAnswer(snap, out)
			})
		},
		AskFollowup: func(ctx context.Context, snap AnswerSnapshot, out interview.Outcome, decision followup.Decision) (interview.Question, error) {
			question, err := runRecordStep(r, ctx, run.ID, stepAskNext, func(ctx context.Context) (interview.Question, error) {
				return r.answerSteps.AskFollowup(ctx, snap, out, decision)
			})
			if err != nil {
				return interview.Question{}, err
			}
			return question, nil
		},
		AskNext: func(ctx context.Context, snap AnswerSnapshot, item interview.PlanItemView) (interview.Question, error) {
			question, err := runRecordStep(r, ctx, run.ID, stepAskNext, func(ctx context.Context) (interview.Question, error) {
				return r.answerSteps.AskNext(ctx, snap, item)
			})
			if err != nil {
				return interview.Question{}, err
			}
			return question, nil
		},
	}
}

// buildSnapshot validates that the turn is still the pending one and loads
// everything the evaluation prompt needs.
func (r *Runner) buildSnapshot(ctx context.Context, run *session.Run) (AnswerSnapshot, error) {
	var in AnswerInput
	if err := json.Unmarshal(run.Input, &in); err != nil {
		return AnswerSnapshot{}, fmt.Errorf("decode answer input: %w", err)
	}
	if in.TurnID == "" {
		return AnswerSnapshot{}, fmt.Errorf("answer input has no turn_id")
	}

	repos := r.store.Repos()
	sess, err := repos.Sessions.Get(ctx, run.SessionID)
	if err != nil {
		return AnswerSnapshot{}, err
	}
	turn, err := repos.Turns.Get(ctx, in.TurnID)
	if err != nil {
		return AnswerSnapshot{}, err
	}
	if turn.SessionID != sess.ID {
		return AnswerSnapshot{}, ports.ErrNotFound
	}
	if sess.Status != session.StatusActive || sess.Phase != session.PhaseAwaitingAnswer {
		return AnswerSnapshot{}, fmt.Errorf("%w: session is %s/%s", ports.ErrStateConflict, sess.Status, sess.Phase)
	}
	if sess.PendingTurnID != turn.ID || !turn.IsPending() {
		return AnswerSnapshot{}, fmt.Errorf("%w: turn %s is not pending", ports.ErrStateConflict, turn.ID)
	}

	plan, err := repos.Plans.ListBySession(ctx, sess.ID)
	if err != nil {
		return AnswerSnapshot{}, err
	}
	items := make([]interview.PlanItemView, 0, len(plan))
	for _, item := range plan {
		items = append(items, interview.PlanItemView{
			Seq:            item.Seq,
			Topic:          item.Topic,
			Objective:      item.Objective,
			ExpectedPoints: item.ExpectedPoints,
			Difficulty:     item.Difficulty,
		})
	}

	turns, err := repos.Turns.ListBySession(ctx, sess.ID)
	if err != nil {
		return AnswerSnapshot{}, err
	}
	nextSeq := 1
	asked := make([]string, 0, len(turns))
	for _, t := range turns {
		if t.Seq >= nextSeq {
			nextSeq = t.Seq + 1
		}
		if question := strings.TrimSpace(t.Question); question != "" {
			asked = append(asked, question)
		}
	}

	// The layered memory slice is resolved per run and frozen into the snapshot
	// (and therefore into run_steps), so a replay uses exactly the same context
	// the model saw. A store failure degrades to recent-turns-only or nothing.
	var memorySummary string
	var memoryRecentTurns []string
	if r.memory != nil {
		if prompt := r.memory.Context(ctx, sess.ID, turns); !prompt.Empty() {
			memorySummary = prompt.Summary
			memoryRecentTurns = prompt.RecentTurns
		}
	}

	evals, err := repos.Evaluations.ListBySession(ctx, sess.ID)
	if err != nil {
		return AnswerSnapshot{}, err
	}
	history := buildTopicHistory(turn.Topic, turn.ID, turns, evals)

	now := r.now().UTC()
	var startedAt time.Time
	var elapsed time.Duration
	if sess.StartedAt != nil {
		startedAt = *sess.StartedAt
		elapsed = now.Sub(startedAt)
	}

	profile := sess.ResumeProfile
	if profile.IsZero() {
		profile = resume.Parse(sess.ResumeText)
	}
	resumeText := profile.Text()
	if resumeText == "" {
		resumeText = sess.ResumeText
	}

	return AnswerSnapshot{
		SessionID:             sess.ID,
		TurnID:                turn.ID,
		Answer:                in.Answer,
		Truncated:             in.Truncated,
		Topic:                 turn.Topic,
		Question:              turn.Question,
		ExpectedPoints:        turn.ExpectedPoints,
		Depth:                 turn.Depth,
		FollowupsThisQuestion: turn.Depth - 1,
		FollowupsUsed:         sess.FollowupsUsed,
		PlanCursor:            sess.PlanCursor,
		PlanItems:             items,
		NextSeq:               nextSeq,
		AskedQuestions:        asked,
		TopicHistory:          history,
		MemorySummary:         memorySummary,
		MemoryRecentTurns:     memoryRecentTurns,
		StartedAt:             startedAt,
		Elapsed:               elapsed,
		Now:                   now,
		Domain:                sess.Domain,
		TargetRole:            sess.TargetRole,
		Difficulty:            sess.Config.Difficulty,
		Profile:               profile,
		ResumeText:            resumeText,
		JDText:                sess.JDText,
		Config:                sess.Config,
	}, nil
}

// updateMemory compresses older turns after a committed answer. The trigger
// lives in the memory builder (every N answered turns), so most answers are a
// cheap no-op. It is best-effort by design: a failed summary is logged and
// never fails the answer run.
func (r *Runner) updateMemory(ctx context.Context, sessionID string) {
	if r.memory == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), memorySummaryTimeout)
	defer cancel()

	turns, err := r.store.Repos().Turns.ListBySession(ctx, sessionID)
	if err != nil {
		r.logger(ctx).Warn("session memory skipped: list turns failed", "session_id", sessionID, "error", err)
		return
	}
	state, fired, err := r.memory.Update(ctx, sessionID, turns)
	if err != nil {
		r.logger(ctx).Warn("session memory update failed, answer kept", "session_id", sessionID, "error", err)
		return
	}
	if fired {
		r.logger(ctx).Info("session memory summarized", "session_id", sessionID,
			"covered_turns", state.CoveredTurns, "degraded", state.Degraded)
	}
}

type scoredTurn struct {
	seq   int
	score float64
}

// buildTopicHistory aggregates the already-scored answers of one topic. It
// reads persisted turns and evaluations only, and returns nil when the topic
// has no usable history so Decide keeps its original behaviour.
//
// Streaks are counted from the most recent answer backwards, using the same
// band cut points as the scorer: band 1 is low, band 5 is high, anything else
// ends the streak.
func buildTopicHistory(topic, currentTurnID string, turns []session.Turn, records []evaluation.Record) *followup.TopicHistory {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return nil
	}

	byTurn := make(map[string]evaluation.Record, len(records))
	for _, rec := range records {
		byTurn[rec.TurnID] = rec
	}

	scored := make([]scoredTurn, 0, len(turns))
	dimSums := make(map[evaluation.Dimension]float64, len(evaluation.DimensionOrder))
	dimCounts := make(map[evaluation.Dimension]int, len(evaluation.DimensionOrder))
	var total float64
	for _, turn := range turns {
		if turn.ID == currentTurnID || turn.Topic != topic || turn.Status != session.TurnStatusAnswered {
			continue
		}
		rec, ok := byTurn[turn.ID]
		if !ok || rec.Degraded || len(rec.Scores) == 0 {
			continue
		}
		scored = append(scored, scoredTurn{seq: turn.Seq, score: rec.WeightedScore})
		total += rec.WeightedScore
		for dimension, value := range rec.Scores {
			dimSums[dimension] += float64(value)
			dimCounts[dimension]++
		}
	}
	if len(scored) == 0 {
		return nil
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].seq < scored[j].seq })

	low, high := 0, 0
	for i := len(scored) - 1; i >= 0; i-- {
		band := evaluation.Band(scored[i].score)
		if band == 1 && high == 0 {
			low++
			continue
		}
		if band == 5 && low == 0 {
			high++
			continue
		}
		break
	}

	weakest := ""
	weakestAvg := 0.0
	for _, dimension := range evaluation.DimensionOrder {
		count := dimCounts[dimension]
		if count == 0 {
			continue
		}
		avg := dimSums[dimension] / float64(count)
		if weakest == "" || avg < weakestAvg {
			weakest = string(dimension)
			weakestAvg = avg
		}
	}

	return &followup.TopicHistory{
		Topic:            topic,
		Answered:         len(scored),
		AvgScore:         total / float64(len(scored)),
		LowStreak:        low,
		HighStreak:       high,
		WeakestDimension: weakest,
	}
}
