// Package followup defines the upper bounds and decision vocabulary for
// follow-up questions. Gate rules (hard budget -> coverage -> model suggestion
// -> final arbitration) land here in M4.
package followup

type Action string

const (
	ActionFollowUp     Action = "follow_up"
	ActionNextQuestion Action = "next_question"
	ActionFinish       Action = "finish"
)

type Limits struct {
	MaxPrimaryQuestions     int
	MaxFollowupsPerQuestion int
	MaxDepth                int
	MaxTotalFollowups       int
	MaxSessionMinutes       int
}

// DefaultLimits mirrors the defaults documented in the development plan.
func DefaultLimits() Limits {
	return Limits{
		MaxPrimaryQuestions:     8,
		MaxFollowupsPerQuestion: 2,
		MaxDepth:                3,
		MaxTotalFollowups:       6,
		MaxSessionMinutes:       45,
	}
}

// Adaptive follow-up thresholds. Streaks are counted over the already-answered
// questions of one topic, using the same band cut points as evaluation.Band:
// band 1 (<50) is "low", band 5 (>=80) is "high". A topic must show two
// consecutive answers in the same extreme before the preference flips.
const (
	WeakStreakThreshold   = 2
	StrongStreakThreshold = 2
)

// TopicHistory is the read-only aggregate of one topic's answered questions
// before the current decision. The zero value / nil pointer means "no history"
// and must leave Decide byte-identical to the original behaviour.
type TopicHistory struct {
	Topic            string  `json:"topic,omitempty"`
	Answered         int     `json:"answered"`
	AvgScore         float64 `json:"avg_score"`
	LowStreak        int     `json:"low_streak"`
	HighStreak       int     `json:"high_streak"`
	WeakestDimension string  `json:"weakest_dimension,omitempty"`
}
