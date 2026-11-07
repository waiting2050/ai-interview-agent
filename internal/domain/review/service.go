package review

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	"github.com/waiting2050/ai-interview-agent/internal/domain/user"
)

// DefaultTZOffsetMinutes is Asia/Shanghai (UTC+8). The API accepts an explicit
// offset so "today" is decided by the caller's clock without a tz database.
const DefaultTZOffsetMinutes = 480

// DefaultDailyLimit is the number of cards the daily queue shows unless
// REVIEW_DAILY_LIMIT configures another value.
const DefaultDailyLimit = 20

// Scoring modes reported by the variant-answer endpoint.
const (
	// ScoringModeObjective means a real model produced the score, and the
	// objective grade drives SM-2.
	ScoringModeObjective = "objective"
	// ScoringModeLengthHeuristic means the heuristic fallback produced a
	// length-based reference score. It never drives SM-2.
	ScoringModeLengthHeuristic = "length_heuristic"
	// ScoringModeSelfOnly means no evaluation chain is wired; only the self
	// grade can drive SM-2.
	ScoringModeSelfOnly = "self_only"
)

// Human-readable notes surfaced by the API and the demo panel.
const (
	ScoringNoteObjective       = "真实模型客观评分，客观分决定本次 SM-2 间隔"
	ScoringNoteLengthHeuristic = "当前未配置模型，本题评分仅按答案长度估算，仅供参考；本次排程按你的自评"
	ScoringNoteSelfOnly        = "当前未启用评估链路，本次仅按自评排程"
)

// Store persists review cards. Implementations live in internal/store.
type Store interface {
	SaveMany(ctx context.Context, cards []*Card) error
	Get(ctx context.Context, id string) (*Card, error)
	ListBySession(ctx context.Context, sessionID string) ([]*Card, error)
	ListDue(ctx context.Context, userID string, before time.Time, limit int) ([]*Card, error)
	Update(ctx context.Context, card *Card) error
}

// VariantEvaluationInput is the neutral input handed to the injected evaluator.
// The review package does not import the agent layer; main wires an adapter
// that runs the existing evaluation chain, which degrades to the heuristic
// model when no API key is configured.
type VariantEvaluationInput struct {
	CardID         string
	Topic          string
	Question       string
	Answer         string
	ExpectedPoints []string
	Rubric         evaluation.Rubric
}

// VariantEvaluation is the result of one variant answer.
type VariantEvaluation struct {
	Score         float64
	MissingPoints []string
	Degraded      bool
	ErrorCode     string
	ModelName     string
	PromptVersion string
	// ScoringMode tells the service whether a real model or the heuristic
	// fallback produced this score.
	ScoringMode string
}

// VariantEvaluator evaluates one variant answer. Production injects the
// existing interview evaluator (real model when configured, heuristic otherwise).
type VariantEvaluator interface {
	EvaluateVariant(ctx context.Context, in VariantEvaluationInput) (VariantEvaluation, error)
}

// DueCard adds an explainable ranking reason on top of the persisted card.
type DueCard struct {
	Card
	// Priority is one of key_topic / low_score / normal.
	Priority string `json:"priority"`
	// SortReason is a one-sentence human explanation shown by the demo.
	SortReason string `json:"sort_reason"`
}

// DueResult is the response of GET /v1/review/due. TotalDue counts every card
// due today; DeferredCount counts the ones hidden by the daily limit.
type DueResult struct {
	TZOffsetMinutes int       `json:"tz_offset_minutes"`
	TodayEnd        time.Time `json:"today_end"`
	Count           int       `json:"count"`
	TotalDue        int       `json:"total_due"`
	DailyLimit      int       `json:"daily_limit"`
	DeferredCount   int       `json:"deferred_count"`
	Remaining       int       `json:"remaining"`
	Cards           []DueCard `json:"cards"`
}

// GradeResult is the response of POST /v1/review/{id}/grade.
type GradeResult struct {
	Card        *Card       `json:"card"`
	Calculation Calculation `json:"calculation"`
}

// VariantAnswerInput is the body of POST /v1/review/{id}/variant/answer.
type VariantAnswerInput struct {
	Answer    string `json:"answer"`
	SelfGrade *int   `json:"self_grade"`
}

// ObjectiveResult exposes the deterministic conversion used for SM-2.
type ObjectiveResult struct {
	Score         float64  `json:"score"`
	Band          int      `json:"band"`
	Grade         int      `json:"grade"`
	MissingPoints []string `json:"missing_points,omitempty"`
	Degraded      bool     `json:"degraded"`
	ErrorCode     string   `json:"error_code,omitempty"`
	ModelName     string   `json:"model_name,omitempty"`
	PromptVersion string   `json:"prompt_version,omitempty"`
}

// Divergence records the self-versus-objective gap. It is persisted on the
// card so it can be aggregated later.
type Divergence struct {
	Delta                  int    `json:"delta"`
	SuspectedOverestimate  bool   `json:"suspected_overestimate"`
	SuspectedUnderestimate bool   `json:"suspected_underestimate"`
	Message                string `json:"message"`
}

// VariantAnswerResult is the response of POST /v1/review/{id}/variant/answer.
type VariantAnswerResult struct {
	Card        *Card           `json:"card"`
	Calculation *Calculation    `json:"calculation,omitempty"`
	Variant     Variant         `json:"variant"`
	Objective   ObjectiveResult `json:"objective"`
	SelfGrade   *int            `json:"self_grade,omitempty"`
	Divergence  *Divergence     `json:"divergence,omitempty"`
	// ScoringMode is objective | length_heuristic | self_only.
	ScoringMode string `json:"scoring_mode"`
	// ScoringNote is the user-facing sentence shown by the review panel.
	ScoringNote string `json:"scoring_note"`
	Message     string `json:"message"`
}

// Service exposes the deterministic review workflow.
type Service struct {
	store      Store
	now        func() time.Time
	evaluator  VariantEvaluator
	dailyLimit int
}

// New wires the service. An optional evaluator enables variant scoring; when
// it is absent the endpoint still works in self_only mode.
func New(store Store, evaluators ...VariantEvaluator) *Service {
	s := &Service{
		store:      store,
		now:        func() time.Time { return time.Now().UTC() },
		dailyLimit: DefaultDailyLimit,
	}
	if len(evaluators) > 0 {
		s.evaluator = evaluators[0]
	}
	return s
}

// SetClock overrides the clock in tests.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// SetDailyLimit configures the daily queue size. Non-positive values keep the
// current limit.
func (s *Service) SetDailyLimit(limit int) {
	if limit > 0 {
		s.dailyLimit = limit
	}
}

// Due lists cards due before the end of the caller's local day, ranked by
// priority and then due time. The daily limit only trims the display queue;
// it never touches the persisted SM-2 due dates.
func (s *Service) Due(ctx context.Context, userID string, tzOffsetMinutes, limit int) (DueResult, error) {
	offset := clampOffset(tzOffsetMinutes)
	end := EndOfToday(s.now().UTC(), offset)
	dailyLimit := s.dailyLimit
	if dailyLimit <= 0 {
		dailyLimit = DefaultDailyLimit
	}
	result := DueResult{
		TZOffsetMinutes: offset,
		TodayEnd:        end,
		DailyLimit:      dailyLimit,
		Cards:           []DueCard{},
	}
	if s.store == nil {
		return result, nil
	}
	cards, err := s.store.ListDue(ctx, ownerOrLegacy(userID), end, 0)
	if err != nil {
		return DueResult{}, fmt.Errorf("list due review cards: %w", err)
	}
	ranked := rankDueCards(cards)
	result.TotalDue = len(ranked)
	shown := dailyLimit
	if limit > 0 && limit < shown {
		shown = limit
	}
	if shown > len(ranked) {
		shown = len(ranked)
	}
	result.Count = shown
	result.DeferredCount = len(ranked) - shown
	result.Remaining = dailyLimit - shown
	if result.Remaining < 0 {
		result.Remaining = 0
	}
	result.Cards = ranked[:shown]
	return result, nil
}

// ListBySession returns every card produced by one interview session, but only
// when the session belongs to the caller.
func (s *Service) ListBySession(ctx context.Context, userID, sessionID string) ([]*Card, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("%w: session_id is required", ErrInvalidArgument)
	}
	if s.store == nil {
		return []*Card{}, nil
	}
	cards, err := s.store.ListBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list review cards: %w", err)
	}
	owned := make([]*Card, 0, len(cards))
	for _, card := range cards {
		if card.UserID == ownerOrLegacy(userID) {
			owned = append(owned, card)
		}
	}
	return owned, nil
}

// Get returns one card if it belongs to the caller.
func (s *Service) Get(ctx context.Context, userID, id string) (*Card, error) {
	if s.store == nil {
		return nil, ErrNotFound
	}
	card, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if card.UserID != ownerOrLegacy(userID) {
		return nil, ErrNotFound
	}
	return card, nil
}

// Grade applies one manually supplied SM-2 grade and persists the schedule.
func (s *Service) Grade(ctx context.Context, userID, id string, grade int) (*GradeResult, error) {
	card, err := s.Get(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	updated, calc, err := GradeCard(card, grade, s.now().UTC())
	if err != nil {
		return nil, err
	}
	if s.store != nil {
		if err := s.store.Update(ctx, updated); err != nil {
			return nil, fmt.Errorf("update review card: %w", err)
		}
	}
	return &GradeResult{Card: updated, Calculation: calc}, nil
}

// Retest returns the deterministic variant of a card's question.
func (s *Service) Retest(ctx context.Context, userID, id string) (*Retest, error) {
	card, err := s.Get(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	result := BuildRetest(card)
	return &result, nil
}

// Variant returns the deterministic variant question for a card.
func (s *Service) Variant(ctx context.Context, userID, id string) (*Variant, error) {
	card, err := s.Get(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	result := BuildVariant(card)
	return &result, nil
}

// AnswerVariant scores one variant answer and applies the appropriate schedule.
//
// Only objective mode (a real model) lets the score drive SM-2. Length
// heuristic mode returns a reference score for comparison only and schedules
// from the self grade. Self-only mode does the same without any score.
func (s *Service) AnswerVariant(ctx context.Context, userID, id string, in VariantAnswerInput) (*VariantAnswerResult, error) {
	card, err := s.Get(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	answer := strings.TrimSpace(in.Answer)
	if answer == "" {
		return nil, fmt.Errorf("%w: answer 不能为空", ErrInvalidArgument)
	}
	if in.SelfGrade != nil && (*in.SelfGrade < 0 || *in.SelfGrade > 5) {
		return nil, fmt.Errorf("%w: %d", ErrInvalidGrade, *in.SelfGrade)
	}

	variant := BuildVariant(card)
	result := &VariantAnswerResult{
		Card:      card,
		Variant:   variant,
		SelfGrade: in.SelfGrade,
	}

	// No evaluation chain at all: self_only.
	if s.evaluator == nil {
		result.ScoringMode = ScoringModeSelfOnly
		result.ScoringNote = ScoringNoteSelfOnly
		if in.SelfGrade == nil {
			result.Message = "当前未启用评估链路，且未填写自评；本次不更新排程"
			return result, nil
		}
		return s.applySelfGrade(ctx, result, card, *in.SelfGrade, ScoringModeSelfOnly)
	}

	out, err := s.evaluator.EvaluateVariant(ctx, VariantEvaluationInput{
		CardID:         card.ID,
		Topic:          card.Topic,
		Question:       variant.Question,
		Answer:         answer,
		ExpectedPoints: variant.ExpectedPoints,
		Rubric:         rubricForTopic(card.Topic),
	})
	if err != nil {
		return nil, fmt.Errorf("evaluate variant answer: %w", err)
	}

	mode := normalizeScoringMode(out.ScoringMode)
	result.ScoringMode = mode
	result.ScoringNote = scoringNote(mode)
	score := clampScore(out.Score)
	result.Objective = ObjectiveResult{
		Score:         score,
		Band:          evaluation.Band(score),
		Grade:         ObjectiveGrade(score),
		MissingPoints: append([]string(nil), out.MissingPoints...),
		Degraded:      out.Degraded,
		ErrorCode:     out.ErrorCode,
		ModelName:     out.ModelName,
		PromptVersion: out.PromptVersion,
	}
	if out.Degraded {
		result.Message = "评估降级，本次不更新排程；请稍后重试"
		return result, nil
	}

	// length_heuristic: the score is a reference signal only.
	if mode != ScoringModeObjective {
		if in.SelfGrade == nil {
			result.Message = "长度启发式评分仅供参考；未填写自评，本次不更新排程"
			return result, nil
		}
		result.Divergence = buildHeuristicDivergence(*in.SelfGrade, result.Objective)
		result.Message = result.Divergence.Message
		return s.applySelfGrade(ctx, result, card, *in.SelfGrade, mode)
	}

	// objective: the objective grade drives SM-2.
	updated, calc, err := GradeCard(card, result.Objective.Grade, s.now().UTC())
	if err != nil {
		return nil, err
	}
	scoreInt := int(math.Round(score))
	band := result.Objective.Band
	updated.LastObjectiveScore = &scoreInt
	updated.LastObjectiveBand = &band
	updated.LastScoringMode = ScoringModeObjective
	if in.SelfGrade != nil {
		self := *in.SelfGrade
		delta := self - result.Objective.Grade
		updated.LastSelfGrade = &self
		updated.LastDivergence = &delta
		result.Divergence = buildDivergence(self, result.Objective)
		result.Message = result.Divergence.Message
	} else {
		updated.LastSelfGrade = nil
		updated.LastDivergence = nil
		result.Message = "未填写自评；本次由客观分决定排程"
	}
	if err := s.persist(ctx, updated); err != nil {
		return nil, err
	}
	result.Card = updated
	result.Calculation = &calc
	return result, nil
}

// applySelfGrade schedules from the self grade without touching the objective
// columns, so older real-model data is never relabelled by a heuristic run.
func (s *Service) applySelfGrade(ctx context.Context, result *VariantAnswerResult, card *Card, self int, mode string) (*VariantAnswerResult, error) {
	updated, calc, err := GradeCard(card, self, s.now().UTC())
	if err != nil {
		return nil, err
	}
	updated.LastSelfGrade = &self
	updated.LastScoringMode = mode
	// The heuristic gap is a transient hint, not an objective-vs-self record.
	updated.LastDivergence = nil
	if err := s.persist(ctx, updated); err != nil {
		return nil, err
	}
	result.Card = updated
	result.Calculation = &calc
	if result.Message == "" {
		if mode == ScoringModeSelfOnly {
			result.Message = ScoringNoteSelfOnly
		} else {
			result.Message = "长度启发式仅供参考；本次排程按自评"
		}
	}
	return result, nil
}

func (s *Service) persist(ctx context.Context, card *Card) error {
	if s.store == nil {
		return nil
	}
	if err := s.store.Update(ctx, card); err != nil {
		return fmt.Errorf("update review card: %w", err)
	}
	return nil
}

func normalizeScoringMode(mode string) string {
	switch mode {
	case ScoringModeObjective:
		return ScoringModeObjective
	case ScoringModeLengthHeuristic:
		return ScoringModeLengthHeuristic
	default:
		// Never overclaim objective scoring.
		return ScoringModeLengthHeuristic
	}
}

func scoringNote(mode string) string {
	if mode == ScoringModeObjective {
		return ScoringNoteObjective
	}
	return ScoringNoteLengthHeuristic
}

// buildHeuristicDivergence reports the gap without concluding over/under
// estimation, because length-based scoring cannot support that claim.
func buildHeuristicDivergence(self int, reference ObjectiveResult) *Divergence {
	delta := self - reference.Grade
	div := &Divergence{Delta: delta}
	if delta >= 2 || delta <= -2 {
		div.Message = fmt.Sprintf("自评 %d 与长度估算 %.1f 分（band %d）差异较大；长度估算仅供参考，本次排程按自评",
			self, reference.Score, reference.Band)
	} else {
		div.Message = fmt.Sprintf("自评 %d 与长度估算 %.1f 分（band %d）接近；长度估算仅供参考，本次排程按自评",
			self, reference.Score, reference.Band)
	}
	return div
}

func buildDivergence(self int, objective ObjectiveResult) *Divergence {
	delta := self - objective.Grade
	div := &Divergence{Delta: delta}
	switch {
	case delta >= 2:
		div.SuspectedOverestimate = true
		div.Message = fmt.Sprintf("自评 %d，客观 %.1f 分（band %d，SM-2 grade %d）：可能高估自己；本次按客观分排程",
			self, objective.Score, objective.Band, objective.Grade)
	case delta <= -2:
		div.SuspectedUnderestimate = true
		div.Message = fmt.Sprintf("自评 %d，客观 %.1f 分（band %d，SM-2 grade %d）：可能低估自己；本次按客观分排程",
			self, objective.Score, objective.Band, objective.Grade)
	default:
		div.Message = fmt.Sprintf("自评 %d 与客观 %.1f 分（band %d，SM-2 grade %d）基本一致",
			self, objective.Score, objective.Band, objective.Grade)
	}
	return div
}

func clampScore(score float64) float64 {
	switch {
	case math.IsNaN(score) || score < 0:
		return 0
	case score > 100:
		return 100
	}
	return math.Round(score*100) / 100
}

func rankDueCards(cards []*Card) []DueCard {
	ranked := make([]DueCard, 0, len(cards))
	for _, card := range cards {
		if card == nil {
			continue
		}
		priority, reason := classifyDue(card)
		ranked = append(ranked, DueCard{Card: *card, Priority: priority, SortReason: reason})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		ti, tj := priorityTier(ranked[i].Priority), priorityTier(ranked[j].Priority)
		if ti != tj {
			return ti < tj
		}
		if !ranked[i].DueAt.Equal(ranked[j].DueAt) {
			return ranked[i].DueAt.Before(ranked[j].DueAt)
		}
		return ranked[i].ID < ranked[j].ID
	})
	return ranked
}

func priorityTier(priority string) int {
	switch priority {
	case "key_topic":
		return 0
	case "low_score":
		return 1
	default:
		return 2
	}
}

func classifyDue(card *Card) (string, string) {
	key := card.KeyTopic || isKeyTopic(card.Topic)
	low, lowReason := isLowScore(card)
	due := card.DueAt.UTC().Format("2006-01-02")
	switch {
	case key && low:
		return "key_topic", fmt.Sprintf("面试高频考点，且历史低分（%s），优先补强；到期 %s", lowReason, due)
	case key:
		return "key_topic", fmt.Sprintf("面试高频考点，优先安排；到期 %s", due)
	case low:
		return "low_score", fmt.Sprintf("历史低分（%s），优先补强；到期 %s", lowReason, due)
	default:
		return "normal", fmt.Sprintf("普通题目，按到期时间排序；到期 %s", due)
	}
}

func isLowScore(card *Card) (bool, string) {
	if card.SourceScore > 0 && card.SourceScore < LowQuestionScore {
		return true, fmt.Sprintf("原题 %.0f 分", card.SourceScore)
	}
	if card.LastObjectiveScore != nil && *card.LastObjectiveScore < LowQuestionScore {
		return true, fmt.Sprintf("上次客观分 %d 分", *card.LastObjectiveScore)
	}
	if card.LastGrade >= 0 && card.LastGrade < 3 {
		return true, fmt.Sprintf("上次回忆 grade %d", card.LastGrade)
	}
	return false, ""
}

func ownerOrLegacy(userID string) string {
	if userID == "" {
		return user.LegacyID
	}
	return userID
}

// EndOfToday returns the first instant of tomorrow in the given fixed offset.
func EndOfToday(now time.Time, offsetMinutes int) time.Time {
	zone := time.FixedZone("review", clampOffset(offsetMinutes)*60)
	local := now.In(zone)
	year, month, day := local.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, zone).AddDate(0, 0, 1).UTC()
}

func clampOffset(minutes int) int {
	if minutes == 0 {
		return DefaultTZOffsetMinutes
	}
	if minutes < -720 {
		return -720
	}
	if minutes > 840 {
		return 840
	}
	return minutes
}
