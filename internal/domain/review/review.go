// Package review turns post-interview weak points into spaced-repetition
// cards. Card creation and scheduling are deterministic rule code: no model is
// called on this path.
package review

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	domainreport "github.com/waiting2050/ai-interview-agent/internal/domain/report"
	"github.com/waiting2050/ai-interview-agent/internal/platform/idgen"
)

const (
	// SchemaVersion is written into card payloads so consumers can evolve.
	SchemaVersion = 1
	// DefaultEase is the SM-2 starting ease factor.
	DefaultEase = 2.5
	// MinEase is the SM-2 floor.
	MinEase = 1.3
	// LowQuestionScore marks a question that earns a card even when the
	// evaluator did not list concrete missing points.
	LowQuestionScore = 60
)

var (
	// ErrInvalidGrade marks a recall quality outside 0-5.
	ErrInvalidGrade = errors.New("review grade must be between 0 and 5")
	// ErrInvalidArgument marks unusable input.
	ErrInvalidArgument = errors.New("invalid review input")
	// ErrNotFound marks a missing review card.
	ErrNotFound = errors.New("review card not found")
)

// Calculation records one SM-2 update so the demo can explain the schedule.
type Calculation struct {
	Grade                int       `json:"grade"`
	PreviousEase         float64   `json:"previous_ease"`
	NewEase              float64   `json:"new_ease"`
	PreviousIntervalDays int       `json:"previous_interval_days"`
	NewIntervalDays      int       `json:"new_interval_days"`
	PreviousRepetitions  int       `json:"previous_repetitions"`
	NewRepetitions       int       `json:"new_repetitions"`
	DueAt                time.Time `json:"due_at"`
	Formula              string    `json:"formula"`
}

// Card is one persisted review unit. EaseFactor / IntervalDays / Repetitions /
// DueAt are the SM-2 state.
type Card struct {
	ID              string       `json:"id"`
	UserID          string       `json:"user_id,omitempty"`
	SessionID       string       `json:"session_id"`
	TurnID          string       `json:"source_turn_id,omitempty"`
	Topic           string       `json:"topic"`
	Question        string       `json:"question"`
	AnswerDigest    string       `json:"answer_digest,omitempty"`
	KeyPoints       []string     `json:"key_points"`
	Dimension       string       `json:"dimension,omitempty"`
	EaseFactor      float64      `json:"ease_factor"`
	IntervalDays    int          `json:"interval_days"`
	Repetitions     int          `json:"repetitions"`
	DueAt           time.Time    `json:"due_at"`
	LastGrade       int          `json:"last_grade"`
	LastReviewedAt  *time.Time   `json:"last_reviewed_at,omitempty"`
	LastCalculation *Calculation `json:"last_calculation,omitempty"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

// BuildCards derives cards from a finished report. A question earns a card
// when it has missing points or when it scored below LowQuestionScore; the
// lowest dimension of that answer becomes the card's focus.
func BuildCards(sessionID string, rep *domainreport.Report, now time.Time) []*Card {
	if rep == nil {
		return nil
	}
	cards := make([]*Card, 0, len(rep.Questions))
	for _, q := range rep.Questions {
		if q.Degraded {
			continue
		}
		dimension, weakest := weakestDimension(q.Scores)
		if len(q.MissingPoints) == 0 && q.Score >= LowQuestionScore && weakest > 2 {
			continue
		}
		topic := strings.TrimSpace(q.Topic)
		if topic == "" {
			topic = fmt.Sprintf("第 %d 题", q.Seq)
		}
		keyPoints := normalizePoints(q.MissingPoints)
		if len(keyPoints) == 0 {
			keyPoints = []string{fmt.Sprintf("重讲「%s」：先给结论，再覆盖机制、边界与取舍", topic)}
		}
		question := strings.TrimSpace(q.Question)
		if question == "" {
			question = fmt.Sprintf("请重新讲解「%s」", topic)
		}
		cards = append(cards, &Card{
			ID:           idgen.New(),
			SessionID:    sessionID,
			TurnID:       q.TurnID,
			Topic:        topic,
			Question:     question,
			AnswerDigest: answerDigest(q, dimension),
			KeyPoints:    keyPoints,
			Dimension:    dimension,
			EaseFactor:   DefaultEase,
			IntervalDays: 0,
			Repetitions:  0,
			DueAt:        now.UTC(),
			LastGrade:    -1,
			CreatedAt:    now.UTC(),
			UpdatedAt:    now.UTC(),
		})
	}
	return cards
}

// GradeCard applies one SM-2 update and returns a copy plus the calculation
// trace. Grades below 3 lapse the card: repetitions reset and the next review
// is scheduled for tomorrow.
func GradeCard(card *Card, grade int, at time.Time) (*Card, Calculation, error) {
	if grade < 0 || grade > 5 {
		return nil, Calculation{}, fmt.Errorf("%w: %d", ErrInvalidGrade, grade)
	}
	at = at.UTC()

	previousEase := card.EaseFactor
	if previousEase <= 0 {
		previousEase = DefaultEase
	}
	delta := 0.1 - float64(5-grade)*(0.08+float64(5-grade)*0.02)
	newEase := math.Max(MinEase, previousEase+delta)
	newEase = round2(newEase)

	newRepetitions := card.Repetitions
	var newInterval int
	var branch string
	switch {
	case grade < 3:
		newRepetitions = 0
		newInterval = 1
		branch = "回忆失败（grade<3）：重复次数归零，间隔重置为 1 天"
	default:
		newRepetitions = card.Repetitions + 1
		switch newRepetitions {
		case 1:
			newInterval = 1
			branch = "第 1 次成功回忆：间隔 1 天"
		case 2:
			newInterval = 6
			branch = "第 2 次成功回忆：间隔 6 天"
		default:
			previousInterval := card.IntervalDays
			if previousInterval < 1 {
				previousInterval = 1
			}
			newInterval = int(math.Round(float64(previousInterval) * newEase))
			if newInterval < 1 {
				newInterval = 1
			}
			branch = fmt.Sprintf("间隔 = 上次 %d 天 × EF %.2f", previousInterval, newEase)
		}
	}
	dueAt := at.Add(time.Duration(newInterval) * 24 * time.Hour)

	calc := Calculation{
		Grade:                grade,
		PreviousEase:         round2(previousEase),
		NewEase:              newEase,
		PreviousIntervalDays: card.IntervalDays,
		NewIntervalDays:      newInterval,
		PreviousRepetitions:  card.Repetitions,
		NewRepetitions:       newRepetitions,
		DueAt:                dueAt,
		Formula: fmt.Sprintf("EF %s + (0.1 - (5-%d)×(0.08+(5-%d)×0.02)) = %.2f；%s；下次 %s",
			formatEase(previousEase), grade, grade, newEase, branch, dueAt.Format("2006-01-02")),
	}

	updated := *card
	updated.KeyPoints = append([]string(nil), card.KeyPoints...)
	updated.EaseFactor = newEase
	updated.IntervalDays = newInterval
	updated.Repetitions = newRepetitions
	updated.DueAt = dueAt
	updated.LastGrade = grade
	reviewedAt := at
	updated.LastReviewedAt = &reviewedAt
	updated.LastCalculation = &calc
	updated.UpdatedAt = at

	return &updated, calc, nil
}

// Retest is a deterministic rephrasing of a due card. Template picks a stable
// variant from Repetitions so repeated sessions do not feel identical.
type Retest struct {
	CardID      string   `json:"card_id"`
	Topic       string   `json:"topic"`
	Question    string   `json:"question"`
	KeyPoints   []string `json:"key_points"`
	Template    string   `json:"template"`
	ModelCalled bool     `json:"model_called"`
}

// BuildRetest returns one rule-generated rephrasing of the original question.
func BuildRetest(card *Card) Retest {
	templates := []struct {
		name string
		text string
	}{
		{"rephrase", "换一种问法：请重新回答下面的问题，先给结论，再说明关键取舍。\n\n" + card.Question},
		{"scenario", fmt.Sprintf("假设面试官追问：「%s」里最容易出错的地方是什么？请结合你的项目经历说明。", card.Topic)},
		{"summary", fmt.Sprintf("请用三句话讲清「%s」的核心机制，再补一个你踩过的坑或做过的取舍。", card.Topic)},
	}
	index := 0
	if card.Repetitions > 0 {
		index = card.Repetitions % len(templates)
	}
	t := templates[index]
	return Retest{
		CardID:      card.ID,
		Topic:       card.Topic,
		Question:    t.text,
		KeyPoints:   append([]string(nil), card.KeyPoints...),
		Template:    t.name,
		ModelCalled: false,
	}
}

func weakestDimension(scores map[string]int) (string, int) {
	if len(scores) == 0 {
		return "", 0
	}
	keys := make([]string, 0, len(scores))
	for k := range scores {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	dimension, lowest := "", 6
	for _, k := range keys {
		if scores[k] < lowest {
			dimension, lowest = k, scores[k]
		}
	}
	return dimension, lowest
}

func answerDigest(q domainreport.QuestionReview, dimension string) string {
	for _, e := range q.Evidence {
		if dimension != "" && e.Dimension == dimension && strings.TrimSpace(e.Quote) != "" {
			return truncateRunes(strings.TrimSpace(e.Quote), 120)
		}
	}
	for _, e := range q.Evidence {
		if strings.TrimSpace(e.Quote) != "" {
			return truncateRunes(strings.TrimSpace(e.Quote), 120)
		}
	}
	if comment := strings.TrimSpace(q.Comment); comment != "" {
		return truncateRunes(comment, 120)
	}
	return ""
}

func normalizePoints(points []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(points))
	for _, p := range points {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "…"
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func formatEase(v float64) string {
	return fmt.Sprintf("%.2f", v)
}
