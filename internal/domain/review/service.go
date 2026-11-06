package review

import (
	"context"
	"fmt"
	"time"
)

// DefaultTZOffsetMinutes is Asia/Shanghai (UTC+8). The API accepts an explicit
// offset so "today" is decided by the caller's clock without a tz database.
const DefaultTZOffsetMinutes = 480

// Store persists review cards. Implementations live in internal/store.
type Store interface {
	SaveMany(ctx context.Context, cards []*Card) error
	Get(ctx context.Context, id string) (*Card, error)
	ListBySession(ctx context.Context, sessionID string) ([]*Card, error)
	ListDue(ctx context.Context, before time.Time, limit int) ([]*Card, error)
	Update(ctx context.Context, card *Card) error
}

// DueResult is the response of GET /v1/review/due.
type DueResult struct {
	TZOffsetMinutes int       `json:"tz_offset_minutes"`
	TodayEnd        time.Time `json:"today_end"`
	Count           int       `json:"count"`
	Cards           []*Card   `json:"cards"`
}

// GradeResult is the response of POST /v1/review/{id}/grade.
type GradeResult struct {
	Card        *Card       `json:"card"`
	Calculation Calculation `json:"calculation"`
}

// Service exposes the deterministic review workflow.
type Service struct {
	store Store
	now   func() time.Time
}

// New wires the service. A nil store makes every read return ErrNotFound.
func New(store Store) *Service {
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }}
}

// SetClock overrides the clock in tests.
func (s *Service) SetClock(now func() time.Time) {
	if now != nil {
		s.now = now
	}
}

// Due lists cards due before the end of the caller's local day.
func (s *Service) Due(ctx context.Context, tzOffsetMinutes, limit int) (DueResult, error) {
	offset := clampOffset(tzOffsetMinutes)
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	end := EndOfToday(s.now().UTC(), offset)
	result := DueResult{TZOffsetMinutes: offset, TodayEnd: end, Cards: []*Card{}}
	if s.store == nil {
		return result, nil
	}
	cards, err := s.store.ListDue(ctx, end, limit)
	if err != nil {
		return DueResult{}, fmt.Errorf("list due review cards: %w", err)
	}
	if cards != nil {
		result.Cards = cards
	}
	result.Count = len(result.Cards)
	return result, nil
}

// ListBySession returns every card produced by one interview session.
func (s *Service) ListBySession(ctx context.Context, sessionID string) ([]*Card, error) {
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
	if cards == nil {
		cards = []*Card{}
	}
	return cards, nil
}

// Get returns one card.
func (s *Service) Get(ctx context.Context, id string) (*Card, error) {
	if s.store == nil {
		return nil, ErrNotFound
	}
	return s.store.Get(ctx, id)
}

// Grade applies one SM-2 update and persists the new schedule.
func (s *Service) Grade(ctx context.Context, id string, grade int) (*GradeResult, error) {
	card, err := s.Get(ctx, id)
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

// Retest returns a deterministic rephrasing of a card's question.
func (s *Service) Retest(ctx context.Context, id string) (*Retest, error) {
	card, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	result := BuildRetest(card)
	return &result, nil
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
