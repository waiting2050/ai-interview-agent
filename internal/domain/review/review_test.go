package review_test

import (
	"context"
	"errors"
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

	result, err := svc.Due(context.Background(), 480, 10)
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

func (f *fakeStore) ListDue(_ context.Context, before time.Time, limit int) ([]*review.Card, error) {
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
