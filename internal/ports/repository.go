// Package ports defines the interfaces that the application and agent layers
// depend on. Implementations live in internal/store and are wired in
// cmd/server. PostgreSQL is the only runtime implementation; the memory store
// is a test double injected directly by unit tests.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	"github.com/waiting2050/ai-interview-agent/internal/domain/jd"
	domainreport "github.com/waiting2050/ai-interview-agent/internal/domain/report"
	"github.com/waiting2050/ai-interview-agent/internal/domain/review"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/domain/user"
)

var (
	ErrNotFound          = errors.New("not found")
	ErrStateConflict     = errors.New("state conflict")
	ErrIdempotencyReused = errors.New("idempotency key reused with a different request")
)

// UserRepository persists minimal accounts for the auth layer.
type UserRepository interface {
	Create(ctx context.Context, u *user.User) error
	GetByID(ctx context.Context, id string) (*user.User, error)
	GetByEmail(ctx context.Context, email string) (*user.User, error)
}

type SessionRepository interface {
	Create(ctx context.Context, s *session.Session) error
	Get(ctx context.Context, id string) (*session.Session, error)
	// UpdateState must compare StateVersion and increment it atomically.
	// On success it writes the new version back into s.StateVersion.
	UpdateState(ctx context.Context, s *session.Session, expectedVersion uint64) error
}

type EventRepository interface {
	// Append allocates a monotonic event_id and fills it back into event.
	Append(ctx context.Context, event *session.Event) error
	ListAfter(ctx context.Context, sessionID string, afterID int64, limit int) ([]session.Event, error)
}

type RunRepository interface {
	// Create inserts a run unless (session_id, kind, idempotency_key) already
	// exists. When a row exists, created is false and existing is returned so
	// the caller can compare request hashes.
	Create(ctx context.Context, run *session.Run) (created bool, existing *session.Run, err error)
	Get(ctx context.Context, id string) (*session.Run, error)
	GetByIdempotencyKey(ctx context.Context, sessionID string, kind session.RunKind, key string) (*session.Run, error)
	// GetRunningBySession enforces the invariant that a session has at most
	// one running run at a time.
	GetRunningBySession(ctx context.Context, sessionID string) (*session.Run, error)
	SetLastStep(ctx context.Context, id, step string) error
	MarkRunning(ctx context.Context, id string, attempt int, at time.Time) error
	MarkFinished(ctx context.Context, id string, status session.RunStatus, errorCode string, at time.Time) error
	// MarkFinishedOwned is the lease-fenced variant used by workers. A stale
	// owner whose lease was reclaimed cannot overwrite the terminal state.
	MarkFinishedOwned(ctx context.Context, id, leaseID string, status session.RunStatus, errorCode string, at time.Time) error
	MarkInterrupted(ctx context.Context, id, lastStep string, at time.Time) error
	// Claim atomically leases up to limit queued (running/interrupted) runs
	// with an expired or empty lease. It uses SELECT ... FOR UPDATE SKIP
	// LOCKED, so multiple instances never claim the same run.
	Claim(ctx context.Context, workerID, leaseID string, now, leaseUntil time.Time, limit int) ([]session.Run, error)
	// RenewLease extends the lease and reports false when the caller no longer
	// owns the run (for example because the lease expired and was reclaimed).
	RenewLease(ctx context.Context, id, leaseID string, until time.Time) (bool, error)
	ReleaseLease(ctx context.Context, id, leaseID string) error
	// RecordFailure increments the consecutive-failure counter, stores the
	// error class/message and moves the run to dead when the threshold is hit.
	RecordFailure(ctx context.Context, id, leaseID, errorCode, errorClass, lastError string, at time.Time, maxFailures int) (dead bool, failureCount int, err error)
	// Requeue puts a failed/interrupted/dead run back in the claim queue.
	// resetFailures is used when an operator explicitly resurrects a dead run.
	Requeue(ctx context.Context, id string, resetFailures bool, at time.Time) error
	// ListUnhealthy returns owner-scoped failed/interrupted/dead runs.
	ListUnhealthy(ctx context.Context, userID string, statuses []session.RunStatus, limit int) ([]session.Run, error)
	// ListStaleRunning returns running runs whose updated_at is older than
	// staleBefore and whose lease is empty or expired. Startup reconciliation
	// uses it to detect crashed runs.
	ListStaleRunning(ctx context.Context, staleBefore, now time.Time, limit int) ([]session.Run, error)
}

type StepRepository interface {
	Get(ctx context.Context, runID, stepName string) (*session.RunStep, error)
	ListByRun(ctx context.Context, runID string) ([]session.RunStep, error)
	Start(ctx context.Context, step *session.RunStep) error
	Complete(ctx context.Context, runID, stepName string, output []byte, at time.Time) error
	Fail(ctx context.Context, runID, stepName, errorCode string, at time.Time) error
}

type TurnRepository interface {
	Create(ctx context.Context, turn *session.Turn) error
	Get(ctx context.Context, id string) (*session.Turn, error)
	// UpdateAnswer persists the candidate answer and marks the turn answered.
	UpdateAnswer(ctx context.Context, turn *session.Turn) error
	ListBySession(ctx context.Context, sessionID string) ([]session.Turn, error)
}

type PlanRepository interface {
	CreateItems(ctx context.Context, items []session.PlanItem) error
	ListBySession(ctx context.Context, sessionID string) ([]session.PlanItem, error)
}

type EvaluationRepository interface {
	// Upsert is keyed by turn_id so a retried run cannot create two records
	// for the same answer.
	Upsert(ctx context.Context, record *evaluation.Record) error
	GetByTurn(ctx context.Context, turnID string) (*evaluation.Record, error)
	ListBySession(ctx context.Context, sessionID string) ([]evaluation.Record, error)
}

// ReportRepository enforces the exactly-one-report-per-session invariant
// through the unique session_id.
type ReportRepository interface {
	// Create inserts the report unless one already exists for the session.
	// When it exists, created is false and existing is returned.
	Create(ctx context.Context, record *domainreport.Record) (created bool, existing *domainreport.Record, err error)
	GetBySession(ctx context.Context, sessionID string) (*domainreport.Record, error)
}

// JDRepository caches parsed job descriptions together with their provenance.
type JDRepository interface {
	Save(ctx context.Context, listing *jd.Listing) error
	Get(ctx context.Context, id string) (*jd.Listing, error)
}

// JDAnalyzer is the M15 rule-based JD pipeline. It parses a URL or pasted text,
// matches it against a resume and plans an outline without calling a model.
type JDAnalyzer interface {
	Analyze(ctx context.Context, userID string, in jd.AnalyzeInput) (jd.Analysis, error)
	Get(ctx context.Context, userID, id string) (*jd.Listing, error)
}

// ReviewRepository persists M11 spaced-repetition cards and their SM-2 state.
type ReviewRepository interface {
	SaveMany(ctx context.Context, cards []*review.Card) error
	Get(ctx context.Context, id string) (*review.Card, error)
	ListBySession(ctx context.Context, sessionID string) ([]*review.Card, error)
	ListDue(ctx context.Context, userID string, before time.Time, limit int) ([]*review.Card, error)
	Update(ctx context.Context, card *review.Card) error
}

// Reviewer is the M11 review workflow used by the HTTP layer. Card generation
// and scheduling are deterministic rule code and never call a model.
type Reviewer interface {
	Due(ctx context.Context, userID string, tzOffsetMinutes, limit int) (review.DueResult, error)
	ListBySession(ctx context.Context, userID, sessionID string) ([]*review.Card, error)
	Get(ctx context.Context, userID, id string) (*review.Card, error)
	Grade(ctx context.Context, userID, id string, grade int) (*review.GradeResult, error)
	Retest(ctx context.Context, userID, id string) (*review.Retest, error)
}

// Repositories is the set of repositories bound to either the pool or an
// active transaction.
type Repositories struct {
	Users       UserRepository
	Sessions    SessionRepository
	Events      EventRepository
	Runs        RunRepository
	Steps       StepRepository
	Turns       TurnRepository
	Plans       PlanRepository
	Evaluations EvaluationRepository
	Reports     ReportRepository
	JDs         JDRepository
	Reviews     ReviewRepository
}

// Store is the runtime persistence boundary. Repos returns repositories bound
// to the pool; WithTx gives the callback a set bound to one transaction, which
// is how business state and events are committed atomically.
type Store interface {
	Repos() Repositories
	WithTx(ctx context.Context, fn func(ctx context.Context, repos Repositories) error) error
	Ping(ctx context.Context) error
	// SubscribeEvents returns a channel that receives a coalesced wake-up
	// signal whenever an event may have been committed for sessionID. It is a
	// hint only: readers must still query ListAfter to get the authoritative
	// order and to support Last-Event-ID replay. The channel closes when ctx
	// is cancelled.
	SubscribeEvents(ctx context.Context, sessionID string) (<-chan struct{}, error)
	Close()
}
