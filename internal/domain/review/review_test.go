package review_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	domainreport "github.com/waiting2050/ai-interview-agent/internal/domain/report"
	"github.com/waiting2050/ai-interview-agent/internal/domain/review"
)

func newCard() *review.Card {
	return &review.Card{
		ID:           "card-1",
		SessionID:    "sess-1",
		Topic:        "订单幂等",
		Question:     "如何保证订单不重复扣减？",
		KeyPoints:    []string{"幂等键", "本地消息表"},
		EaseFactor:   review.DefaultEase,
		IntervalDays: 0,
		Repetitions:  0,
		DueAt:        time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC),
		LastGrade:    -1,
	}
}

func TestGradeCardSuccessPath(t *testing.T) {
	at := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	card := newCard()

	first, calc, err := review.GradeCard(card, 3, at)
	if err != nil {
		t.Fatal(err)
	}
	if first.EaseFactor != 2.36 {
		t.Fatalf("ease after grade 3 = %v, want 2.36", first.EaseFactor)
	}
	if first.Repetitions != 1 || first.IntervalDays != 1 {
		t.Fatalf("first success = reps %d interval %d, want 1/1", first.Repetitions, first.IntervalDays)
	}
	if !first.DueAt.Equal(at.Add(24 * time.Hour)) {
		t.Fatalf("due = %v, want %v", first.DueAt, at.Add(24*time.Hour))
	}
	if calc.PreviousEase != 2.5 || calc.NewEase != 2.36 || calc.NewIntervalDays != 1 {
		t.Fatalf("calculation = %+v", calc)
	}
	if card.EaseFactor != review.DefaultEase {
		t.Fatal("original card must not be mutated")
	}

	second, _, _ := review.GradeCard(first, 5, at.Add(24*time.Hour))
	if second.EaseFactor != 2.46 || second.Repetitions != 2 || second.IntervalDays != 6 {
		t.Fatalf("second success = %+v", second)
	}

	third, _, _ := review.GradeCard(second, 5, at.Add(7*24*time.Hour))
	if third.Repetitions != 3 || third.IntervalDays != 15 {
		t.Fatalf("third success = reps %d interval %d, want 3/15", third.Repetitions, third.IntervalDays)
	}
	if third.EaseFactor != 2.56 {
		t.Fatalf("third ease = %v, want 2.56", third.EaseFactor)
	}
}

func TestGradeCardLapseAndFloor(t *testing.T) {
	at := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	card := newCard()
	card.EaseFactor = 2.5
	card.Repetitions = 3
	card.IntervalDays = 15

	lapsed, calc, err := review.GradeCard(card, 0, at)
	if err != nil {
		t.Fatal(err)
	}
	if lapsed.Repetitions != 0 || lapsed.IntervalDays != 1 {
		t.Fatalf("lapse = reps %d interval %d, want 0/1", lapsed.Repetitions, lapsed.IntervalDays)
	}
	if lapsed.EaseFactor != 1.7 {
		t.Fatalf("lapse ease = %v, want 1.7", lapsed.EaseFactor)
	}
	if calc.NewRepetitions != 0 || calc.PreviousIntervalDays != 15 {
		t.Fatalf("calculation = %+v", calc)
	}

	floored, _, err := review.GradeCard(&review.Card{EaseFactor: 1.4}, 0, at)
	if err != nil {
		t.Fatal(err)
	}
	if floored.EaseFactor != review.MinEase {
		t.Fatalf("ease floor = %v, want %v", floored.EaseFactor, review.MinEase)
	}

	// A min-grade success still advances the repetition count.
	recovered, _, _ := review.GradeCard(lapsed, 3, at.Add(24*time.Hour))
	if recovered.Repetitions != 1 || recovered.IntervalDays != 1 {
		t.Fatalf("recovered = %+v", recovered)
	}
}

func TestGradeCardRejectsInvalidGrade(t *testing.T) {
	at := time.Now()
	for _, grade := range []int{-1, 6} {
		if _, _, err := review.GradeCard(newCard(), grade, at); !errors.Is(err, review.ErrInvalidGrade) {
			t.Fatalf("grade %d err = %v", grade, err)
		}
	}
}

func TestBuildCardsFromReport(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	rep := &domainreport.Report{
		SessionID: "sess-1",
		Questions: []domainreport.QuestionReview{
			{
				TurnID:        "turn-1",
				Seq:           1,
				Topic:         "订单幂等",
				Question:      "如何保证订单不重复扣减？",
				Score:         72,
				Scores:        map[string]int{"correctness": 4, "depth": 3, "structure": 2},
				Comment:       "链路完整",
				MissingPoints: []string{"补偿失败的兜底", "补偿失败的兜底", "消息堆积限流"},
				Evidence: []domainreport.Evidence{
					{TurnID: "turn-1", Dimension: "correctness", Quote: "用订单号做幂等键"},
				},
			},
			{
				TurnID:   "turn-2",
				Seq:      2,
				Topic:    "缓存一致性",
				Question: "缓存与数据库如何保持一致？",
				Score:    45,
				Scores:   map[string]int{"correctness": 2, "depth": 2},
			},
			{
				TurnID:   "turn-3",
				Seq:      3,
				Topic:    "降级题",
				Question: "降级题",
				Score:    0,
				Degraded: true,
			},
			{
				TurnID:   "turn-4",
				Seq:      4,
				Topic:    "高分题",
				Question: "高分题",
				Score:    90,
				Scores:   map[string]int{"correctness": 5, "depth": 5},
			},
		},
	}

	cards := review.BuildCards("sess-1", rep, now)
	if len(cards) != 2 {
		t.Fatalf("cards = %d, want 2", len(cards))
	}
	first := cards[0]
	if first.SessionID != "sess-1" || first.TurnID != "turn-1" {
		t.Fatalf("card = %+v", first)
	}
	if len(first.KeyPoints) != 2 {
		t.Fatalf("key points must be deduped: %v", first.KeyPoints)
	}
	if first.Dimension != "structure" {
		t.Fatalf("weakest dimension = %q, want structure", first.Dimension)
	}
	if first.AnswerDigest != "用订单号做幂等键" {
		t.Fatalf("digest = %q", first.AnswerDigest)
	}
	if !first.DueAt.Equal(now) || first.EaseFactor != review.DefaultEase || first.LastGrade != -1 {
		t.Fatalf("initial schedule = %+v", first)
	}

	second := cards[1]
	if len(second.KeyPoints) != 1 || second.KeyPoints[0] == "" {
		t.Fatalf("low-score card needs a deterministic key point: %v", second.KeyPoints)
	}
	if second.Dimension != "correctness" && second.Dimension != "depth" {
		t.Fatalf("dimension = %q", second.Dimension)
	}
}

func TestBuildRetestRotatesWithRepetitions(t *testing.T) {
	card := newCard()
	first := review.BuildRetest(card)
	if first.ModelCalled || first.CardID != card.ID || first.Question == "" {
		t.Fatalf("retest = %+v", first)
	}
	card.Repetitions = 1
	if review.BuildRetest(card).Template != "scenario" {
		t.Fatalf("reps 1 template = %q", review.BuildRetest(card).Template)
	}
	card.Repetitions = 2
	if review.BuildRetest(card).Template != "summary" {
		t.Fatalf("reps 2 template = %q", review.BuildRetest(card).Template)
	}
	card.Repetitions = 3
	if review.BuildRetest(card).Template != "rephrase" {
		t.Fatalf("reps 3 template = %q", review.BuildRetest(card).Template)
	}
}

func TestBuildVariantUsesSyllabusPointsDeterministically(t *testing.T) {
	card := &review.Card{
		ID:           "card-go",
		Topic:        "Go 并发模型",
		KeyPoints:    []string{"自定义要点"},
		Repetitions:  1,
		EaseFactor:   review.DefaultEase,
		IntervalDays: 0,
		LastGrade:    -1,
	}
	first := review.BuildVariant(card)
	second := review.BuildVariant(card)
	if first.ModelCalled {
		t.Fatal("variant generation must not call a model")
	}
	if first.Question != second.Question || !reflect.DeepEqual(first.ExpectedPoints, second.ExpectedPoints) {
		t.Fatalf("variant generation is not deterministic:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if first.Template != "scenario" {
		t.Fatalf("reps 1 template = %q", first.Template)
	}
	want := map[string]bool{
		"goroutine 调度与 G-M-P 模型": true,
		"channel 阻塞与缓冲区设计":       true,
		"context 取消与超时传递":        true,
	}
	if len(first.ExpectedPoints) != len(want) {
		t.Fatalf("expected points = %v", first.ExpectedPoints)
	}
	for _, point := range first.ExpectedPoints {
		if !want[point] {
			t.Fatalf("unexpected syllabus point %q", point)
		}
	}
	if !strings.Contains(first.Question, "Go 并发模型") {
		t.Fatalf("question = %q", first.Question)
	}
}

func TestBuildVariantFallsBackToCardKeyPoints(t *testing.T) {
	card := &review.Card{
		ID:          "card-fallback",
		Topic:       "订单幂等",
		KeyPoints:   []string{"幂等键", "本地消息表", "重试语义"},
		Repetitions: 2,
		EaseFactor:  review.DefaultEase,
		LastGrade:   -1,
	}
	first := review.BuildVariant(card)
	if len(first.ExpectedPoints) != 3 {
		t.Fatalf("points = %v", first.ExpectedPoints)
	}
	if !reflect.DeepEqual(first, review.BuildVariant(card)) {
		t.Fatalf("fallback variant is not deterministic: %+v", first)
	}
}

func TestBuildCardsMarksKeyTopicAndSourceScore(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	rep := &domainreport.Report{Questions: []domainreport.QuestionReview{
		{
			TurnID:        "turn-key",
			Topic:         "Go 并发模型",
			Question:      "讲讲 Go 并发",
			Score:         61,
			Scores:        map[string]int{"correctness": 3, "depth": 3},
			MissingPoints: []string{"context 取消"},
		},
		{
			TurnID:        "turn-normal",
			Topic:         "自拟考点",
			Question:      "自拟问题",
			Score:         70,
			Scores:        map[string]int{"correctness": 3, "depth": 3},
			MissingPoints: []string{"自拟缺口"},
		},
	}}
	cards := review.BuildCards("sess-1", rep, now)
	if len(cards) != 2 {
		t.Fatalf("cards = %d", len(cards))
	}
	if !cards[0].KeyTopic {
		t.Fatal("syllabus focus topic must be marked")
	}
	if cards[0].SourceScore != 61 {
		t.Fatalf("source score = %v", cards[0].SourceScore)
	}
	if cards[1].KeyTopic {
		t.Fatal("unknown topic must not be marked as key")
	}
}

func TestObjectiveGradeMapping(t *testing.T) {
	cases := []struct {
		score float64
		want  int
	}{
		{-1, 1}, {0, 1}, {39.9, 1}, {40, 2}, {59.9, 2}, {60, 3},
		{79.9, 3}, {80, 4}, {89.9, 4}, {90, 5}, {100, 5}, {120, 5},
	}
	for _, tc := range cases {
		if got := review.ObjectiveGrade(tc.score); got != tc.want {
			t.Fatalf("ObjectiveGrade(%v) = %d, want %d", tc.score, got, tc.want)
		}
	}
}

func TestAnswerVariantUsesObjectiveGrade(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	card := newCard()
	card.UserID = "u1"
	evaluator := &fakeEvaluator{result: review.VariantEvaluation{
		Score:         45,
		MissingPoints: []string{"没讲补偿"},
		ModelName:     "fake-heuristic",
		PromptVersion: "eval-test",
	}}
	store := &fakeStore{cards: []*review.Card{card}}
	svc := review.New(store, evaluator)
	svc.SetClock(func() time.Time { return now })

	self := 5
	result, err := svc.AnswerVariant(context.Background(), "u1", card.ID, review.VariantAnswerInput{
		Answer:    "我的回答",
		SelfGrade: &self,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Objective.Score != 45 || result.Objective.Band != 1 || result.Objective.Grade != 2 {
		t.Fatalf("objective = %+v", result.Objective)
	}
	if result.Card.LastGrade != 2 || result.Card.IntervalDays != 1 || result.Card.Repetitions != 0 {
		t.Fatalf("objective grade must drive SM-2: %+v", result.Card)
	}
	if result.Card.LastObjectiveScore == nil || *result.Card.LastObjectiveScore != 45 ||
		result.Card.LastObjectiveBand == nil || *result.Card.LastObjectiveBand != 1 ||
		result.Card.LastSelfGrade == nil || *result.Card.LastSelfGrade != 5 {
		t.Fatalf("objective/self fields = %+v", result.Card)
	}
	if result.Divergence == nil || result.Divergence.Delta != 3 || !result.Divergence.SuspectedOverestimate {
		t.Fatalf("divergence = %+v", result.Divergence)
	}
	if result.Card.LastDivergence == nil || *result.Card.LastDivergence != 3 {
		t.Fatalf("persisted divergence = %v", result.Card.LastDivergence)
	}
	if !strings.Contains(result.Message, "高估") {
		t.Fatalf("message = %q", result.Message)
	}
	if result.Calculation == nil || result.Calculation.Grade != 2 {
		t.Fatalf("calculation = %+v", result.Calculation)
	}
	if len(evaluator.last.ExpectedPoints) == 0 {
		t.Fatal("variant points must be passed to the evaluator")
	}
}

func TestAnswerVariantRequiresEvaluator(t *testing.T) {
	card := newCard()
	card.UserID = "u1"
	store := &fakeStore{cards: []*review.Card{card}}
	svc := review.New(store)
	_, err := svc.AnswerVariant(context.Background(), "u1", card.ID, review.VariantAnswerInput{Answer: "回答"})
	if !errors.Is(err, review.ErrEvaluatorUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestAnswerVariantDegradedDoesNotReschedule(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	card := newCard()
	card.UserID = "u1"
	evaluator := &fakeEvaluator{result: review.VariantEvaluation{Degraded: true, ErrorCode: "model_output_invalid"}}
	store := &fakeStore{cards: []*review.Card{card}}
	svc := review.New(store, evaluator)
	svc.SetClock(func() time.Time { return now })

	result, err := svc.AnswerVariant(context.Background(), "u1", card.ID, review.VariantAnswerInput{Answer: "回答"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Objective.Degraded || result.Calculation != nil {
		t.Fatalf("degraded result = %+v", result)
	}
	if result.Card.LastGrade != -1 || !result.Card.DueAt.Equal(card.DueAt) {
		t.Fatalf("degraded must not reschedule: %+v", result.Card)
	}
}

func TestAnswerVariantRejectsBadInput(t *testing.T) {
	card := newCard()
	card.UserID = "u1"
	store := &fakeStore{cards: []*review.Card{card}}
	svc := review.New(store, &fakeEvaluator{})
	bad := 6
	if _, err := svc.AnswerVariant(context.Background(), "u1", card.ID, review.VariantAnswerInput{Answer: " ", SelfGrade: &bad}); !errors.Is(err, review.ErrInvalidArgument) {
		t.Fatalf("empty answer err = %v", err)
	}
	if _, err := svc.AnswerVariant(context.Background(), "u1", card.ID, review.VariantAnswerInput{Answer: "回答", SelfGrade: &bad}); !errors.Is(err, review.ErrInvalidGrade) {
		t.Fatalf("bad self grade err = %v", err)
	}
}

func TestDuePriorityAndDailyLimit(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	makeCard := func(id, topic string, source float64, dueAt time.Time) *review.Card {
		c := newCard()
		c.ID = id
		c.UserID = "u1"
		c.Topic = topic
		c.Question = topic
		c.SourceScore = source
		c.DueAt = dueAt
		return c
	}
	cards := []*review.Card{
		makeCard("key", "Go 并发模型", 90, now.Add(2*time.Hour)),
		makeCard("low", "自拟低分题", 45, now.Add(time.Hour)),
		makeCard("normal-late", "普通题 A", 80, now),
		makeCard("normal-early", "普通题 B", 80, now.Add(-time.Hour)),
	}
	store := &fakeStore{cards: cards}
	svc := review.New(store)
	svc.SetClock(func() time.Time { return now })
	svc.SetDailyLimit(2)

	result, err := svc.Due(context.Background(), "u1", 480, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalDue != 4 || result.Count != 2 || result.DeferredCount != 2 || result.DailyLimit != 2 || result.Remaining != 0 {
		t.Fatalf("due counts = %+v", result)
	}
	if result.Cards[0].ID != "key" || result.Cards[1].ID != "low" {
		t.Fatalf("priority order = %v, %v", result.Cards[0].ID, result.Cards[1].ID)
	}
	if result.Cards[0].Priority != "key_topic" || result.Cards[1].Priority != "low_score" {
		t.Fatalf("priority labels = %q, %q", result.Cards[0].Priority, result.Cards[1].Priority)
	}
	if !strings.Contains(result.Cards[0].SortReason, "高频") || !strings.Contains(result.Cards[1].SortReason, "低分") {
		t.Fatalf("sort reasons = %q / %q", result.Cards[0].SortReason, result.Cards[1].SortReason)
	}

	svc.SetDailyLimit(4)
	all, err := svc.Due(context.Background(), "u1", 480, 0)
	if err != nil {
		t.Fatal(err)
	}
	if all.Count != 4 || all.DeferredCount != 0 || all.Remaining != 0 {
		t.Fatalf("all due = %+v", all)
	}
	if all.Cards[2].ID != "normal-early" || all.Cards[3].ID != "normal-late" {
		t.Fatalf("same-tier due order = %v, %v", all.Cards[2].ID, all.Cards[3].ID)
	}

	svc.SetDailyLimit(2)
	one, err := svc.Due(context.Background(), "u1", 480, 1)
	if err != nil {
		t.Fatal(err)
	}
	if one.Count != 1 || one.DeferredCount != 3 || one.Remaining != 1 {
		t.Fatalf("requested limit = %+v", one)
	}
}

type fakeEvaluator struct {
	result review.VariantEvaluation
	err    error
	last   review.VariantEvaluationInput
	calls  int
}

func (f *fakeEvaluator) EvaluateVariant(_ context.Context, in review.VariantEvaluationInput) (review.VariantEvaluation, error) {
	f.calls++
	f.last = in
	return f.result, f.err
}

func TestEndOfTodayRespectsOffset(t *testing.T) {
	// 23:59 in UTC+8 is 15:59 UTC; the local day must still end at 16:00 UTC.
	now := time.Date(2026, 11, 6, 15, 59, 0, 0, time.UTC)
	end := review.EndOfToday(now, 480)
	want := time.Date(2026, 11, 6, 16, 0, 0, 0, time.UTC)
	if !end.Equal(want) {
		t.Fatalf("end = %v, want %v", end, want)
	}

	// One minute later the local date rolled over.
	after := time.Date(2026, 11, 6, 16, 1, 0, 0, time.UTC)
	endAfter := review.EndOfToday(after, 480)
	wantAfter := time.Date(2026, 11, 7, 16, 0, 0, 0, time.UTC)
	if !endAfter.Equal(wantAfter) {
		t.Fatalf("end after = %v, want %v", endAfter, wantAfter)
	}

	// A zero offset falls back to Asia/Shanghai rather than UTC.
	if got := review.EndOfToday(now, 0); !got.Equal(want) {
		t.Fatalf("default offset end = %v, want %v", got, want)
	}
}

func TestDueFiltersByLocalDay(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	dueToday := newCard()
	dueToday.ID = "due-today"
	dueToday.DueAt = time.Date(2026, 11, 6, 15, 59, 0, 0, time.UTC)
	dueTomorrow := newCard()
	dueTomorrow.ID = "due-tomorrow"
	dueTomorrow.DueAt = time.Date(2026, 11, 6, 16, 1, 0, 0, time.UTC)

	store := &fakeStore{cards: []*review.Card{dueToday, dueTomorrow}}
	svc := review.New(store)
	svc.SetClock(func() time.Time { return now })

	result, err := svc.Due(context.Background(), "", 480, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || result.Cards[0].ID != "due-today" {
		t.Fatalf("due = %+v", result)
	}
	if !result.TodayEnd.Equal(time.Date(2026, 11, 6, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("today_end = %v", result.TodayEnd)
	}
}

type fakeStore struct {
	cards []*review.Card
}

func (f *fakeStore) SaveMany(_ context.Context, cards []*review.Card) error {
	f.cards = append(f.cards, cards...)
	return nil
}

func (f *fakeStore) Get(_ context.Context, id string) (*review.Card, error) {
	for _, c := range f.cards {
		if c.ID == id {
			cp := *c
			return &cp, nil
		}
	}
	return nil, review.ErrNotFound
}

func (f *fakeStore) ListBySession(_ context.Context, sessionID string) ([]*review.Card, error) {
	out := []*review.Card{}
	for _, c := range f.cards {
		if c.SessionID == sessionID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStore) ListDue(_ context.Context, _ string, before time.Time, limit int) ([]*review.Card, error) {
	out := []*review.Card{}
	for _, c := range f.cards {
		if c.DueAt.Before(before) {
			out = append(out, c)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) Update(_ context.Context, card *review.Card) error {
	for i, c := range f.cards {
		if c.ID == card.ID {
			f.cards[i] = card
			return nil
		}
	}
	return review.ErrNotFound
}
