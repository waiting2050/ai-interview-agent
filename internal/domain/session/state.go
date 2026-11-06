// Package session defines the interview session entity and its lifecycle.
// The domain layer owns transition rules and validation; it deliberately has
// no dependency on eino, SQL, Redis or HTTP.
package session

import (
	"fmt"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
)

type Status string

const (
	StatusCreated   Status = "created"
	StatusActive    Status = "active"
	StatusReporting Status = "reporting"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusAborted   Status = "aborted"
)

// Phase is only meaningful while Status is active. Transient execution stages
// (evaluating, deciding, asking) belong to run_steps, not to this field.
type Phase string

const (
	PhaseAwaitingAnswer  Phase = "awaiting_answer"
	PhaseReadyToAsk      Phase = "ready_to_ask"
	PhaseReadyToFinalize Phase = "ready_to_finalize"
)

// Command is the external mutation that drives a transition. It is used in
// errors and logs; the concrete transition methods are the source of truth.
type Command string

const (
	CommandStart  Command = "start"
	CommandAnswer Command = "answer"
	CommandFinish Command = "finish"
	CommandReport Command = "report"
	CommandAbort  Command = "abort"
)

type Config struct {
	MaxPrimaryQuestions     int    `json:"max_primary_questions"`
	MaxFollowupsPerQuestion int    `json:"max_followups_per_question"`
	MaxDepth                int    `json:"max_depth"`
	MaxTotalFollowups       int    `json:"max_total_followups"`
	DurationMinutes         int    `json:"duration_minutes"`
	Difficulty              string `json:"difficulty"`
	Locale                  string `json:"locale"`
}

const (
	defaultMaxPrimaryQuestions     = 8
	defaultMaxFollowupsPerQuestion = 2
	defaultMaxDepth                = 3
	defaultMaxTotalFollowups       = 6
	defaultDurationMinutes         = 45
	defaultDifficulty              = "mid"
	defaultLocale                  = "zh-CN"
)

// DefaultConfig returns the baseline interview configuration used when the
// client omits fields.
func DefaultConfig() Config {
	return Config{
		MaxPrimaryQuestions:     defaultMaxPrimaryQuestions,
		MaxFollowupsPerQuestion: defaultMaxFollowupsPerQuestion,
		MaxDepth:                defaultMaxDepth,
		MaxTotalFollowups:       defaultMaxTotalFollowups,
		DurationMinutes:         defaultDurationMinutes,
		Difficulty:              defaultDifficulty,
		Locale:                  defaultLocale,
	}
}

// Normalize fills zero values with defaults. Ranges are validated separately
// so that callers can surface a precise error.
func (c *Config) Normalize() {
	if c.MaxPrimaryQuestions == 0 {
		c.MaxPrimaryQuestions = defaultMaxPrimaryQuestions
	}
	if c.MaxFollowupsPerQuestion == 0 {
		c.MaxFollowupsPerQuestion = defaultMaxFollowupsPerQuestion
	}
	if c.MaxDepth == 0 {
		c.MaxDepth = defaultMaxDepth
	}
	if c.MaxTotalFollowups == 0 {
		c.MaxTotalFollowups = defaultMaxTotalFollowups
	}
	if c.DurationMinutes == 0 {
		c.DurationMinutes = defaultDurationMinutes
	}
	if c.Difficulty == "" {
		c.Difficulty = defaultDifficulty
	}
	if c.Locale == "" {
		c.Locale = defaultLocale
	}
}

// Validate enforces the hard limits documented in the plan. Zero values must
// be normalized first.
func (c Config) Validate() error {
	if c.MaxPrimaryQuestions < 1 || c.MaxPrimaryQuestions > 12 {
		return fmt.Errorf("max_primary_questions must be in [1,12], got %d", c.MaxPrimaryQuestions)
	}
	if c.MaxFollowupsPerQuestion < 0 || c.MaxFollowupsPerQuestion > 3 {
		return fmt.Errorf("max_followups_per_question must be in [0,3], got %d", c.MaxFollowupsPerQuestion)
	}
	if c.MaxDepth < 1 || c.MaxDepth > 4 {
		return fmt.Errorf("max_depth must be in [1,4], got %d", c.MaxDepth)
	}
	if c.MaxTotalFollowups < 0 || c.MaxTotalFollowups > 10 {
		return fmt.Errorf("max_total_followups must be in [0,10], got %d", c.MaxTotalFollowups)
	}
	if c.DurationMinutes < 1 || c.DurationMinutes > 60 {
		return fmt.Errorf("duration_minutes must be in [1,60], got %d", c.DurationMinutes)
	}
	switch c.Difficulty {
	case "junior", "mid", "senior":
	default:
		return fmt.Errorf("difficulty must be junior/mid/senior, got %q", c.Difficulty)
	}
	if c.Locale == "" {
		return fmt.Errorf("locale must not be empty")
	}
	return nil
}

type Session struct {
	ID            string
	CandidateName string
	TargetRole    string
	ResumeText    string
	ResumeProfile resume.Profile
	JDText        string
	Domain        string
	Config        Config

	Status        Status
	Phase         Phase
	StateVersion  uint64
	PendingTurnID string
	PlanCursor    int
	FollowupsUsed int

	StartedAt *time.Time
	EndedAt   *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TransitionError describes a rejected state transition. The HTTP layer maps
// it to 409 state_conflict.
type TransitionError struct {
	SessionID string
	Status    Status
	Phase     Phase
	Command   Command
	Reason    string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("session %s: cannot %s in status=%s phase=%s: %s",
		e.SessionID, e.Command, e.Status, e.Phase, e.Reason)
}

func (s *Session) transitionError(cmd Command, reason string) error {
	return &TransitionError{
		SessionID: s.ID,
		Status:    s.Status,
		Phase:     s.Phase,
		Command:   cmd,
		Reason:    reason,
	}
}

// IsTerminal reports whether no further write commands are allowed.
func (s *Session) IsTerminal() bool {
	return s.Status == StatusCompleted || s.Status == StatusFailed || s.Status == StatusAborted
}

// Start moves created -> active/awaiting_answer after the first question has
// been produced.
func (s *Session) Start(now time.Time) error {
	if s.Status != StatusCreated {
		return s.transitionError(CommandStart, "session already started")
	}
	s.Status = StatusActive
	s.Phase = PhaseAwaitingAnswer
	s.StartedAt = &now
	return nil
}

// Finish moves an active session to ready_to_finalize (early finish).
func (s *Session) Finish(now time.Time) error {
	if s.Status != StatusActive {
		return s.transitionError(CommandFinish, "session is not active")
	}
	if s.Phase != PhaseAwaitingAnswer && s.Phase != PhaseReadyToAsk {
		return s.transitionError(CommandFinish, "session is not accepting answers")
	}
	s.Phase = PhaseReadyToFinalize
	s.PendingTurnID = ""
	s.UpdatedAt = now
	return nil
}

// AskFollowup records a follow-up question for the current topic. It keeps the
// session in awaiting_answer and increments the global follow-up counter.
func (s *Session) AskFollowup(turnID string, now time.Time) error {
	if s.Status != StatusActive || s.Phase != PhaseAwaitingAnswer {
		return s.transitionError(CommandAnswer, "session is not awaiting an answer")
	}
	s.PendingTurnID = turnID
	s.FollowupsUsed++
	s.UpdatedAt = now
	return nil
}

// AskNextQuestion advances the outline cursor and records the next primary
// question. The caller must ensure a plan item exists for the new cursor.
func (s *Session) AskNextQuestion(turnID string, now time.Time) error {
	if s.Status != StatusActive || s.Phase != PhaseAwaitingAnswer {
		return s.transitionError(CommandAnswer, "session is not awaiting an answer")
	}
	s.PlanCursor++
	s.PendingTurnID = turnID
	s.UpdatedAt = now
	return nil
}

// BeginReport moves ready_to_finalize -> reporting.
func (s *Session) BeginReport() error {
	if s.Status != StatusActive || s.Phase != PhaseReadyToFinalize {
		return s.transitionError(CommandReport, "session is not ready to generate a report")
	}
	s.Status = StatusReporting
	return nil
}

// Complete finalizes reporting -> completed.
func (s *Session) Complete(now time.Time) error {
	if s.Status != StatusReporting {
		return s.transitionError(CommandReport, "session is not reporting")
	}
	s.Status = StatusCompleted
	s.EndedAt = &now
	s.UpdatedAt = now
	return nil
}

// Fail marks a non-terminal session as failed.
func (s *Session) Fail(now time.Time) error {
	if s.IsTerminal() {
		return s.transitionError(CommandAbort, "session already terminal")
	}
	s.Status = StatusFailed
	s.EndedAt = &now
	s.UpdatedAt = now
	return nil
}

// Abort is idempotent: aborting an already aborted session is a no-op success.
func (s *Session) Abort(now time.Time) error {
	switch s.Status {
	case StatusAborted:
		return nil
	case StatusCompleted, StatusFailed:
		return s.transitionError(CommandAbort, "session already terminal")
	}
	s.Status = StatusAborted
	s.EndedAt = &now
	s.UpdatedAt = now
	return nil
}

// Event is the append-only SSE source of truth. ID is a global monotonic
// cursor; per-session order is preserved after filtering.
type Event struct {
	ID        int64
	SessionID string
	RunID     string
	Type      string
	Payload   []byte
	CreatedAt time.Time
}

// Event type names. They are shared with the SSE catalog in the plan.
const (
	EventSessionCreated   = "session.created"
	EventSessionStarted   = "session.started"
	EventSessionPhase     = "session.phase"
	EventSessionAborted   = "session.aborted"
	EventSessionCompleted = "session.completed"
	EventSessionFailed    = "session.failed"
	EventQuestionAsked    = "question.asked"
	EventAnswerReceived   = "answer.received"
	EventEvaluationDone   = "evaluation.completed"
	EventEvaluationFailed = "evaluation.degraded"
	EventDecisionMade     = "decision.made"
	EventRunStarted       = "run.started"
	EventRunSucceeded     = "run.succeeded"
	EventRunFailed        = "run.failed"
	EventRunInterrupted   = "run.interrupted"
	EventReportStarted    = "report.started"
	EventReportSection    = "report.section"
	EventReportToolCalls  = "report.tool_calls"
	EventReportCompleted  = "report.completed"
)
