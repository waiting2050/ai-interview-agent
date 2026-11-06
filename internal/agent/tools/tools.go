// Package tools exposes the read-only conversation queries used by report
// generation. Every tool is scoped to one session, validates its arguments,
// enforces a timeout and a result-size cap, and records each call so the caller
// can persist a cost breakdown.
package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

var (
	ErrInvalidArgument = errors.New("tool invalid argument")
	ErrForbidden       = errors.New("tool access forbidden")
	ErrBudgetExceeded  = errors.New("tool budget exceeded")
	ErrTimeout         = errors.New("tool timeout")
)

const (
	defaultTimeout         = 2 * time.Second
	defaultMaxResultRunes  = 6000
	defaultMaxCalls        = 12
	defaultMaxSectionCalls = 4
	maxSearchLimit         = 8
	maxWeakLimit           = 10
)

type Options struct {
	Timeout         time.Duration
	MaxResultRunes  int
	MaxCalls        int
	MaxSectionCalls int
}

func (o *Options) normalize() {
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	if o.MaxResultRunes <= 0 {
		o.MaxResultRunes = defaultMaxResultRunes
	}
	if o.MaxCalls <= 0 {
		o.MaxCalls = defaultMaxCalls
	}
	if o.MaxSectionCalls <= 0 {
		o.MaxSectionCalls = defaultMaxSectionCalls
	}
}

// CallRecord is one tool invocation. The report run persists the aggregate in
// events so cost accounting does not depend on logs.
type CallRecord struct {
	Tool         string `json:"tool"`
	Args         string `json:"args,omitempty"`
	DurationMS   int64  `json:"duration_ms"`
	ResultRunes  int    `json:"result_runes"`
	Truncated    bool   `json:"truncated"`
	SectionLabel string `json:"section,omitempty"`
}

// Query is bound to one session. Callers cannot override the session id, which
// is the primary defence against cross-session reads.
type Query struct {
	sessionID string
	repos     ports.Repositories
	opts      Options

	calls        int
	sectionOpen  bool
	sectionLabel string
	sectionCalls int
	records      []CallRecord
}

func NewQuery(sessionID string, repos ports.Repositories, opts Options) *Query {
	opts.normalize()
	return &Query{sessionID: sessionID, repos: repos, opts: opts}
}

// BeginSection opens a new tool-call budget window. Report generation uses
// one window for evidence collection so every section stays within the per
// section limit.
func (q *Query) BeginSection(label string) {
	q.sectionOpen = true
	q.sectionLabel = label
	q.sectionCalls = 0
}

func (q *Query) Records() []CallRecord { return append([]CallRecord(nil), q.records...) }

func (q *Query) Breakdown() map[string]int {
	out := map[string]int{}
	for _, r := range q.records {
		out[r.Tool]++
	}
	return out
}

func (q *Query) Calls() int { return q.calls }

// TurnBrief is the compact per-question row. It never carries the full answer.
// The score decomposition is included so the report can show its arithmetic.
type TurnBrief struct {
	TurnID      string                  `json:"turn_id"`
	Seq         int                     `json:"seq"`
	Topic       string                  `json:"topic"`
	Kind        string                  `json:"kind"`
	Depth       int                     `json:"depth"`
	Question    string                  `json:"question"`
	Score       float64                 `json:"score"`
	Scores      map[string]int          `json:"scores,omitempty"`
	BaseScore   float64                 `json:"base_score,omitempty"`
	Bonus       []evaluation.Adjustment `json:"bonus,omitempty"`
	Penalty     []evaluation.Adjustment `json:"penalty,omitempty"`
	Evidence    []evaluation.Evidence   `json:"evidence,omitempty"`
	Degraded    bool                    `json:"degraded"`
	Missing     int                     `json:"missing_points"`
	MissingList []string                `json:"-"`
}

// TurnSummary adds a bounded answer excerpt for prompt evidence.
type TurnSummary struct {
	TurnBrief
	AnswerExcerpt string `json:"answer_excerpt"`
}

// DimensionSummary is the interview-wide average for one dimension.
type DimensionSummary struct {
	Dimension string  `json:"dimension"`
	Score     float64 `json:"score"`
}

type Overview struct {
	SessionID     string             `json:"session_id"`
	CandidateName string             `json:"candidate_name"`
	TargetRole    string             `json:"target_role"`
	Status        string             `json:"status"`
	Phase         string             `json:"phase"`
	Difficulty    string             `json:"difficulty"`
	PlanItems     int                `json:"plan_items"`
	AnsweredTurns int                `json:"answered_turns"`
	Followups     int                `json:"followup_turns"`
	DegradedTurns int                `json:"degraded_turns"`
	OverallScore  float64            `json:"overall_score"`
	Dimensions    []DimensionSummary `json:"dimensions"`
	WeakTopics    []string           `json:"weak_topics,omitempty"`
	Turns         []TurnBrief        `json:"turns"`
	Truncated     bool               `json:"truncated"`
}

type SearchInput struct {
	Topic    string   `json:"topic,omitempty"`
	Query    string   `json:"query,omitempty"`
	MinScore *float64 `json:"min_score,omitempty"`
	MaxScore *float64 `json:"max_score,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}

type SearchResult struct {
	Turns     []TurnSummary `json:"turns"`
	Total     int           `json:"total"`
	Truncated bool          `json:"truncated"`
}

type TurnDetail struct {
	TurnBrief
	Question      string                `json:"question"`
	Answer        string                `json:"answer"`
	Evidence      []evaluation.Evidence `json:"evidence,omitempty"`
	MissingPoints []string              `json:"missing_points,omitempty"`
	Truncated     bool                  `json:"truncated"`
}

// Bundle is everything report generation is allowed to see. The full
// transcript never enters the prompt.
type Bundle struct {
	Overview      Overview       `json:"overview"`
	WeakTurns     []TurnSummary  `json:"weak_turns"`
	Turns         []TurnSummary  `json:"turns"`
	ToolCalls     []CallRecord   `json:"tool_calls"`
	ToolBreakdown map[string]int `json:"tool_breakdown"`
	Calls         int            `json:"calls"`
}

// Collect runs the fixed evidence-retrieval plan: session overview, weak turns,
// then a bounded turn search. Three calls fit both the per-section and total
// budgets.
func (q *Query) Collect(ctx context.Context) (Bundle, error) {
	q.BeginSection("evidence")

	overview, err := q.SessionOverview(ctx)
	if err != nil {
		return Bundle{}, err
	}
	weak, err := q.ListWeakTurns(ctx, 5)
	if err != nil {
		return Bundle{}, err
	}
	search, err := q.SearchTurns(ctx, SearchInput{Limit: maxSearchLimit})
	if err != nil {
		return Bundle{}, err
	}

	return Bundle{
		Overview:      overview,
		WeakTurns:     weak.Turns,
		Turns:         search.Turns,
		ToolCalls:     q.Records(),
		ToolBreakdown: q.Breakdown(),
		Calls:         q.Calls(),
	}, nil
}

func (q *Query) beginCall(ctx context.Context, tool, args string) (context.Context, context.CancelFunc, time.Time, error) {
	if strings.TrimSpace(q.sessionID) == "" {
		return nil, nil, time.Time{}, fmt.Errorf("%w: session id is empty", ErrInvalidArgument)
	}
	if q.calls >= q.opts.MaxCalls {
		return nil, nil, time.Time{}, fmt.Errorf("%w: total calls reached %d", ErrBudgetExceeded, q.opts.MaxCalls)
	}
	if q.sectionOpen && q.sectionCalls >= q.opts.MaxSectionCalls {
		return nil, nil, time.Time{}, fmt.Errorf("%w: section %q reached %d calls", ErrBudgetExceeded, q.sectionLabel, q.opts.MaxSectionCalls)
	}
	callCtx, cancel := context.WithTimeout(ctx, q.opts.Timeout)
	q.calls++
	if q.sectionOpen {
		q.sectionCalls++
	}
	return callCtx, cancel, time.Now(), nil
}

func (q *Query) endCall(tool, args string, start time.Time, resultRunes int, truncated bool) {
	record := CallRecord{
		Tool:         tool,
		Args:         args,
		DurationMS:   time.Since(start).Milliseconds(),
		ResultRunes:  resultRunes,
		Truncated:    truncated,
		SectionLabel: q.sectionLabel,
	}
	q.records = append(q.records, record)
}

func (q *Query) callError(tool string, ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s exceeded %s", ErrTimeout, tool, q.opts.Timeout)
	}
	return err
}

// SessionOverview loads the session, plan and turn/evaluation aggregates. It
// returns compact per-question rows and no answer text.
func (q *Query) SessionOverview(ctx context.Context) (Overview, error) {
	callCtx, cancel, start, err := q.beginCall(ctx, "get_session_overview", q.sessionID)
	if err != nil {
		return Overview{}, err
	}
	defer cancel()

	sess, err := q.repos.Sessions.Get(callCtx, q.sessionID)
	if err != nil {
		return Overview{}, fmt.Errorf("overview session: %w", q.callError("get_session_overview", callCtx, err))
	}
	plan, err := q.repos.Plans.ListBySession(callCtx, q.sessionID)
	if err != nil {
		return Overview{}, fmt.Errorf("overview plan: %w", q.callError("get_session_overview", callCtx, err))
	}
	turns, err := q.repos.Turns.ListBySession(callCtx, q.sessionID)
	if err != nil {
		return Overview{}, fmt.Errorf("overview turns: %w", q.callError("get_session_overview", callCtx, err))
	}

	evals, err := q.evaluations(callCtx)
	if err != nil {
		return Overview{}, err
	}

	overview := Overview{
		SessionID:     sess.ID,
		CandidateName: sess.CandidateName,
		TargetRole:    sess.TargetRole,
		Status:        string(sess.Status),
		Phase:         string(sess.Phase),
		Difficulty:    sess.Config.Difficulty,
		PlanItems:     len(plan),
	}

	var (
		scoreSum   float64
		scoreCount int
		weak       = map[string]float64{}
		missing    = map[string]int{}
	)
	for _, turn := range turns {
		if !answered(&turn) {
			continue
		}
		overview.AnsweredTurns++
		if turn.Kind == session.TurnKindFollowup {
			overview.Followups++
		}
		rec := evals[turn.ID]
		brief := TurnBrief{
			TurnID:   turn.ID,
			Seq:      turn.Seq,
			Topic:    truncateRunes(turn.Topic, 40),
			Kind:     string(turn.Kind),
			Depth:    turn.Depth,
			Question: truncateRunes(turn.Question, 120),
		}
		if rec != nil {
			brief.Degraded = rec.Degraded
			brief.Missing = len(rec.MissingPoints)
			brief.MissingList = clean(rec.MissingPoints)
			brief.Scores = dimensionScores(rec.Scores)
			brief.BaseScore = rec.BaseScore
			brief.Bonus = rec.Bonus
			brief.Penalty = rec.Penalty
			brief.Evidence = rec.Evidence
			if rec.Degraded {
				overview.DegradedTurns++
			} else {
				brief.Score = rec.WeightedScore
				scoreSum += rec.WeightedScore
				scoreCount++
				addWeakTopic(weak, turn.Topic, rec.WeightedScore)
			}
			for _, point := range rec.MissingPoints {
				normalized := strings.TrimSpace(point)
				if normalized != "" {
					missing[normalized]++
				}
			}
		}
		overview.Turns = append(overview.Turns, brief)
	}
	if scoreCount > 0 {
		overview.OverallScore = round1(scoreSum / float64(scoreCount))
	}

	for _, dimension := range evaluation.DimensionOrder {
		var sum float64
		var count int
		for _, rec := range evals {
			if rec.Degraded {
				continue
			}
			if score, ok := rec.Scores[dimension]; ok {
				sum += float64(score)
				count++
			}
		}
		value := 0.0
		if count > 0 {
			value = round1(sum / float64(count) * 20)
		}
		overview.Dimensions = append(overview.Dimensions, DimensionSummary{
			Dimension: string(dimension),
			Score:     value,
		})
	}
	overview.WeakTopics = weakTopics(weak, missing, 3)

	resultRunes := estimateRunes(overview)
	truncated := resultRunes > q.opts.MaxResultRunes
	if truncated {
		overview.Turns = limitTurns(overview.Turns, 20)
	}
	q.endCall("get_session_overview", q.sessionID, start, minInt(resultRunes, q.opts.MaxResultRunes), truncated)
	return overview, nil
}

// ListWeakTurns returns the lowest-scoring answered turns. Degraded records are
// excluded because they carry no comparable score.
func (q *Query) ListWeakTurns(ctx context.Context, limit int) (SearchResult, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > maxWeakLimit {
		return SearchResult{}, fmt.Errorf("%w: limit must be <= %d", ErrInvalidArgument, maxWeakLimit)
	}
	callCtx, cancel, start, err := q.beginCall(ctx, "list_weak_turns", fmt.Sprintf("limit=%d", limit))
	if err != nil {
		return SearchResult{}, err
	}
	defer cancel()

	turns, evals, err := q.turnData(callCtx)
	if err != nil {
		return SearchResult{}, fmt.Errorf("list weak turns: %w", q.callError("list_weak_turns", callCtx, err))
	}

	var out []TurnSummary
	for _, turn := range turns {
		if !answered(&turn) {
			continue
		}
		rec := evals[turn.ID]
		if rec == nil || rec.Degraded {
			continue
		}
		if rec.WeightedScore > 70 {
			continue
		}
		out = append(out, summary(turn, rec, q.opts.MaxResultRunes))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Seq < out[j].Seq
		}
		return out[i].Score < out[j].Score
	})
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}

	runes := estimateRunes(out)
	capped := runes > q.opts.MaxResultRunes
	if capped {
		truncated = true
	}
	q.endCall("list_weak_turns", fmt.Sprintf("limit=%d", limit), start, minInt(runes, q.opts.MaxResultRunes), truncated)
	return SearchResult{Turns: out, Total: len(out), Truncated: truncated}, nil
}

// SearchTurns filters answered turns by topic, text or score. The result set
// is capped at maxSearchLimit rows.
func (q *Query) SearchTurns(ctx context.Context, in SearchInput) (SearchResult, error) {
	if in.Limit <= 0 {
		in.Limit = 6
	}
	if in.Limit > maxSearchLimit {
		return SearchResult{}, fmt.Errorf("%w: limit must be <= %d", ErrInvalidArgument, maxSearchLimit)
	}
	if in.MinScore != nil && (*in.MinScore < 0 || *in.MinScore > 100) {
		return SearchResult{}, fmt.Errorf("%w: min_score must be in [0,100]", ErrInvalidArgument)
	}
	if in.MaxScore != nil && (*in.MaxScore < 0 || *in.MaxScore > 100) {
		return SearchResult{}, fmt.Errorf("%w: max_score must be in [0,100]", ErrInvalidArgument)
	}
	if in.MinScore != nil && in.MaxScore != nil && *in.MinScore > *in.MaxScore {
		return SearchResult{}, fmt.Errorf("%w: min_score must not exceed max_score", ErrInvalidArgument)
	}

	callCtx, cancel, start, err := q.beginCall(ctx, "search_turns", fmt.Sprintf("topic=%s query=%s limit=%d", in.Topic, in.Query, in.Limit))
	if err != nil {
		return SearchResult{}, err
	}
	defer cancel()

	turns, evals, err := q.turnData(callCtx)
	if err != nil {
		return SearchResult{}, fmt.Errorf("search turns: %w", q.callError("search_turns", callCtx, err))
	}

	topicFilter := strings.TrimSpace(in.Topic)
	queryFilter := strings.TrimSpace(in.Query)
	var out []TurnSummary
	for _, turn := range turns {
		if !answered(&turn) {
			continue
		}
		if topicFilter != "" && !strings.Contains(turn.Topic, topicFilter) {
			continue
		}
		if queryFilter != "" && !strings.Contains(turn.Question, queryFilter) && !strings.Contains(turn.AnswerText, queryFilter) {
			continue
		}
		rec := evals[turn.ID]
		if rec == nil {
			continue
		}
		if in.MinScore != nil && rec.WeightedScore < *in.MinScore {
			continue
		}
		if in.MaxScore != nil && rec.WeightedScore > *in.MaxScore {
			continue
		}
		out = append(out, summary(turn, rec, q.opts.MaxResultRunes))
	}
	truncated := len(out) > in.Limit
	if truncated {
		out = out[:in.Limit]
	}
	runes := estimateRunes(out)
	capped := runes > q.opts.MaxResultRunes
	if capped {
		truncated = true
	}
	q.endCall("search_turns", fmt.Sprintf("topic=%s query=%s limit=%d", in.Topic, in.Query, in.Limit), start, minInt(runes, q.opts.MaxResultRunes), truncated)
	return SearchResult{Turns: out, Total: len(out), Truncated: truncated}, nil
}

// Turn returns one turn with a bounded answer excerpt. The session-ownership
// check is deliberately strict: a turn from another session is rejected instead
// of being silently filtered.
func (q *Query) Turn(ctx context.Context, turnID string) (TurnDetail, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return TurnDetail{}, fmt.Errorf("%w: turn_id is required", ErrInvalidArgument)
	}

	callCtx, cancel, start, err := q.beginCall(ctx, "get_turn", turnID)
	if err != nil {
		return TurnDetail{}, err
	}
	defer cancel()

	turn, err := q.repos.Turns.Get(callCtx, turnID)
	if err != nil {
		return TurnDetail{}, fmt.Errorf("get turn: %w", q.callError("get_turn", callCtx, err))
	}
	if turn.SessionID != q.sessionID {
		return TurnDetail{}, fmt.Errorf("%w: turn %s does not belong to this session", ErrForbidden, turnID)
	}

	detail := TurnDetail{
		TurnBrief: TurnBrief{
			TurnID:   turn.ID,
			Seq:      turn.Seq,
			Topic:    truncateRunes(turn.Topic, 40),
			Kind:     string(turn.Kind),
			Depth:    turn.Depth,
			Question: truncateRunes(turn.Question, 200),
		},
	}
	maxAnswer := q.opts.MaxResultRunes / 3
	if maxAnswer < 400 {
		maxAnswer = 400
	}
	detail.Answer = truncateRunes(turn.AnswerText, maxAnswer)
	detail.Truncated = len([]rune(turn.AnswerText)) > maxAnswer

	if rec, err := q.repos.Evaluations.GetByTurn(callCtx, turnID); err == nil {
		detail.Score = rec.WeightedScore
		detail.Degraded = rec.Degraded
		detail.MissingPoints = clean(rec.MissingPoints)
		detail.Missing = len(detail.MissingPoints)
		detail.Evidence = limitEvidence(rec.Evidence, 5)
	}

	runes := estimateRunes(detail)
	q.endCall("get_turn", turnID, start, minInt(runes, q.opts.MaxResultRunes), detail.Truncated)
	return detail, nil
}

func (q *Query) evaluations(ctx context.Context) (map[string]*evaluation.Record, error) {
	turns, err := q.repos.Turns.ListBySession(ctx, q.sessionID)
	if err != nil {
		return nil, fmt.Errorf("list turns for evaluations: %w", err)
	}
	out := map[string]*evaluation.Record{}
	for _, turn := range turns {
		rec, err := q.repos.Evaluations.GetByTurn(ctx, turn.ID)
		if err != nil {
			if errors.Is(err, ports.ErrNotFound) {
				continue
			}
			return nil, fmt.Errorf("get evaluation %s: %w", turn.ID, err)
		}
		out[turn.ID] = rec
	}
	return out, nil
}

func (q *Query) turnData(ctx context.Context) ([]session.Turn, map[string]*evaluation.Record, error) {
	turns, err := q.repos.Turns.ListBySession(ctx, q.sessionID)
	if err != nil {
		return nil, nil, err
	}
	evals, err := q.evaluations(ctx)
	if err != nil {
		return nil, nil, err
	}
	return turns, evals, nil
}

func answered(turn *session.Turn) bool {
	return turn.Status == session.TurnStatusAnswered && strings.TrimSpace(turn.AnswerText) != ""
}

func summary(turn session.Turn, rec *evaluation.Record, maxResultRunes int) TurnSummary {
	answerCap := maxResultRunes / 8
	if answerCap < 120 {
		answerCap = 120
	}
	return TurnSummary{
		TurnBrief: TurnBrief{
			TurnID:      turn.ID,
			Seq:         turn.Seq,
			Topic:       truncateRunes(turn.Topic, 40),
			Kind:        string(turn.Kind),
			Depth:       turn.Depth,
			Question:    truncateRunes(turn.Question, 120),
			Score:       rec.WeightedScore,
			Scores:      dimensionScores(rec.Scores),
			BaseScore:   rec.BaseScore,
			Bonus:       rec.Bonus,
			Penalty:     rec.Penalty,
			Evidence:    rec.Evidence,
			Degraded:    rec.Degraded,
			Missing:     len(rec.MissingPoints),
			MissingList: clean(rec.MissingPoints),
		},
		AnswerExcerpt: truncateRunes(turn.AnswerText, answerCap),
	}
}

func dimensionScores(in map[evaluation.Dimension]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for dimension, score := range in {
		out[string(dimension)] = score
	}
	return out
}

func addWeakTopic(out map[string]float64, topic string, score float64) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return
	}
	if current, ok := out[topic]; !ok || score < current {
		out[topic] = score
	}
}

func weakTopics(scores map[string]float64, missing map[string]int, limit int) []string {
	type row struct {
		topic string
		score float64
	}
	var rows []row
	for topic, score := range scores {
		if score < 70 {
			rows = append(rows, row{topic: topic, score: score})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].score == rows[j].score {
			return rows[i].topic < rows[j].topic
		}
		return rows[i].score < rows[j].score
	})
	var out []string
	for _, r := range rows {
		out = append(out, r.topic)
		if len(out) == limit {
			break
		}
	}
	if len(out) < limit {
		type point struct {
			text  string
			count int
		}
		var points []point
		for text, count := range missing {
			points = append(points, point{text: text, count: count})
		}
		sort.Slice(points, func(i, j int) bool {
			if points[i].count == points[j].count {
				return points[i].text < points[j].text
			}
			return points[i].count > points[j].count
		})
		for _, p := range points {
			if len(out) == limit {
				break
			}
			out = append(out, p.text)
		}
	}
	return out
}

func clean(in []string) []string {
	out := make([]string, 0, len(in))
	for _, value := range in {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func limitEvidence(in []evaluation.Evidence, limit int) []evaluation.Evidence {
	if len(in) <= limit {
		return in
	}
	return in[:limit]
}

func limitTurns(in []TurnBrief, limit int) []TurnBrief {
	if len(in) <= limit {
		return in
	}
	return in[len(in)-limit:]
}

func truncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

func estimateRunes(v any) int {
	// A cheap upper bound is enough for the size cap; the prompt builder
	// independently truncates each field.
	switch value := v.(type) {
	case Overview:
		n := len(value.Turns) * 220
		return n + len(value.Dimensions)*40 + 300
	case []TurnSummary:
		return len(value) * 460
	case TurnDetail:
		return len([]rune(value.Answer)) + len([]rune(value.Question)) + 200
	default:
		return 0
	}
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
