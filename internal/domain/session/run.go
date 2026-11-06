package session

import "time"

type RunKind string

const (
	RunKindStart  RunKind = "start"
	RunKindAnswer RunKind = "answer"
	RunKindReport RunKind = "report"
)

type RunStatus string

const (
	RunStatusRunning     RunStatus = "running"
	RunStatusSucceeded   RunStatus = "succeeded"
	RunStatusFailed      RunStatus = "failed"
	RunStatusInterrupted RunStatus = "interrupted"
)

// Run is one graph execution. (session_id, kind, idempotency_key) is unique;
// the request hash detects a key reused for a different body.
type Run struct {
	ID             string
	SessionID      string
	Kind           RunKind
	Status         RunStatus
	IdempotencyKey string
	RequestHash    string
	// Input is the command payload (turn_id + answer) persisted so an answer
	// run can be resumed after a crash. Start runs keep it empty.
	Input      []byte
	Attempt    int
	ErrorCode  string
	LastStep   string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt *time.Time
}

type StepStatus string

const (
	StepStatusRunning   StepStatus = "running"
	StepStatusSucceeded StepStatus = "succeeded"
	StepStatusFailed    StepStatus = "failed"
)

// RunStep stores the product of one graph node. A succeeded step is replayed
// from Output instead of being executed again after a crash.
type RunStep struct {
	RunID      string
	StepName   string
	Status     StepStatus
	Attempt    int
	InputHash  string
	Output     []byte
	ErrorCode  string
	StartedAt  time.Time
	FinishedAt *time.Time
}
