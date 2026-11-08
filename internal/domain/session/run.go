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
	// RunStatusDead is terminal: the run failed more than the configured
	// consecutive-failure threshold and must be re-queued explicitly.
	RunStatusDead RunStatus = "dead"
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
	// TraceID is the HTTP request trace that created the run. Workers restore
	// it from storage so the run, its steps, model/tool calls and events keep
	// one trace_id even though the originating request has already returned.
	TraceID string
	// Input is the command payload (turn_id + answer) persisted so an answer
	// run can be resumed after a crash. Start runs keep it empty.
	Input     []byte
	Attempt   int
	ErrorCode string
	LastStep  string
	// Lease fields describe which worker currently owns the run. A queued run
	// has an empty lease; an expired lease may be reclaimed by another worker.
	LeaseID        string
	LeaseWorker    string
	LeaseExpiresAt *time.Time
	// FailureCount counts consecutive failed executions. A success ends the
	// run, so the counter only grows through retries until it hits the threshold.
	FailureCount   int
	LastError      string
	LastErrorClass string
	DeadAt         *time.Time
	// Model usage is aggregated from the run's steps. CostMicros is in
	// micro-CNY; 0 means no price was configured for the provider.
	PromptTokens     int
	CompletionTokens int
	ModelCalls       int
	CostMicros       int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
	FinishedAt       *time.Time
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
	// Usage is the model spend attributed to this node. CostMicros is in
	// micro-CNY and is 0 when no price is configured.
	PromptTokens     int
	CompletionTokens int
	ModelCalls       int
	CostMicros       int64
}
