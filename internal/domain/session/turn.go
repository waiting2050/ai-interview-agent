package session

import "time"

// TurnKind distinguishes a topic's primary question from its follow-ups.
type TurnKind string

const (
	TurnKindPrimary  TurnKind = "primary"
	TurnKindFollowup TurnKind = "followup"
)

// TurnStatus is pending while the question waits for an answer and answered
// once the candidate has replied.
type TurnStatus string

const (
	TurnStatusPending  TurnStatus = "pending"
	TurnStatusAnswered TurnStatus = "answered"
)

// Turn is one question-answer unit. Follow-ups point at their primary
// question through ParentTurnID and Depth keeps the chain bounded.
type Turn struct {
	ID             string
	SessionID      string
	Seq            int
	Kind           TurnKind
	ParentTurnID   string
	Topic          string
	Depth          int
	Question       string
	ExpectedPoints []string
	Status         TurnStatus
	AnswerText     string
	AnswerHash     string
	AnsweredAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// IsPending reports whether the turn still waits for an answer.
func (t *Turn) IsPending() bool { return t.Status == TurnStatusPending }

// PlanItem is one topic in the session outline. PlanCursor on the session is
// the number of primary questions already asked.
type PlanItem struct {
	ID             string
	SessionID      string
	Seq            int
	Topic          string
	Objective      string
	ExpectedPoints []string
	Difficulty     string
	Status         string
}
