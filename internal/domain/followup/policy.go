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
