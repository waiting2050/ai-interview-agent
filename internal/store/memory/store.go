// Package memory provides an in-memory test double for ports.Store.
//
// It is injected by unit tests only and must never be wired into cmd/server:
// PostgreSQL is the single runtime source of truth. This implementation does
// not provide transactional rollback, multi-process safety or durability.
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	"github.com/waiting2050/ai-interview-agent/internal/domain/jd"
	domainreport "github.com/waiting2050/ai-interview-agent/internal/domain/report"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

type Store struct {
	mu sync.RWMutex

	sessions   map[string]*session.Session
	runs       map[string]*session.Run
	runKeys    map[string]string           // sessionID|kind|key -> runID
	steps      map[string]*session.RunStep // runID|stepName
	events     map[string][]session.Event
	turns      map[string]*session.Turn
	plans      map[string][]session.PlanItem
	evals      map[string]*evaluation.Record   // turnID
	reports    map[string]*domainreport.Record // sessionID
	jdListings map[string]*jd.Listing          // listing id
	subs       map[string]map[chan struct{}]struct{}

	eventSeq int64

	repos ports.Repositories
}

func (s *Store) Repos() ports.Repositories { return s.repos }

var _ ports.Store = (*Store)(nil)

func New() *Store {
	s := &Store{
		sessions:   map[string]*session.Session{},
		runs:       map[string]*session.Run{},
		runKeys:    map[string]string{},
		steps:      map[string]*session.RunStep{},
		events:     map[string][]session.Event{},
		turns:      map[string]*session.Turn{},
		plans:      map[string][]session.PlanItem{},
		evals:      map[string]*evaluation.Record{},
		reports:    map[string]*domainreport.Record{},
		jdListings: map[string]*jd.Listing{},
		subs:       map[string]map[chan struct{}]struct{}{},
	}
	s.repos = ports.Repositories{
		Sessions:    &sessionRepo{s: s},
		Events:      &eventRepo{s: s},
		Runs:        &runRepo{s: s},
		Steps:       &stepRepo{s: s},
		Turns:       &turnRepo{s: s},
		Plans:       &planRepo{s: s},
		Evaluations: &evaluationRepo{s: s},
		Reports:     &reportRepo{s: s},
		JDs:         &jdRepo{s: s},
	}
	return s
}

// WithTx runs fn with the same repositories. There is no rollback: this is a
// deliberate limitation of the unit-test double.
func (s *Store) WithTx(ctx context.Context, fn func(ctx context.Context, repos ports.Repositories) error) error {
	return fn(ctx, s.repos)
}

func (s *Store) Ping(context.Context) error { return nil }
func (s *Store) Close()                     {}

// SubscribeEvents mirrors the PostgreSQL LISTEN/NOTIFY wake-up signal. The
// channel is buffered and coalescing: readers still query the event log.
func (s *Store) SubscribeEvents(ctx context.Context, sessionID string) (<-chan struct{}, error) {
	ch := make(chan struct{}, 1)

	s.mu.Lock()
	if s.subs[sessionID] == nil {
		s.subs[sessionID] = map[chan struct{}]struct{}{}
	}
	s.subs[sessionID][ch] = struct{}{}
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		if subs, ok := s.subs[sessionID]; ok {
			if _, exists := subs[ch]; exists {
				delete(subs, ch)
				close(ch)
			}
			if len(subs) == 0 {
				delete(s.subs, sessionID)
			}
		}
	}()

	return ch, nil
}

func (s *Store) broadcast(sessionID string) {
	for ch := range s.subs[sessionID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

type sessionRepo struct{ s *Store }

func (r *sessionRepo) Create(_ context.Context, in *session.Session) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.sessions[in.ID]; ok {
		return ports.ErrStateConflict
	}
	cp := *in
	r.s.sessions[in.ID] = &cp
	return nil
}

func (r *sessionRepo) Get(_ context.Context, id string) (*session.Session, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	s, ok := r.s.sessions[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	cp := *s
	return &cp, nil
}

func (r *sessionRepo) UpdateState(_ context.Context, in *session.Session, expectedVersion uint64) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	s, ok := r.s.sessions[in.ID]
	if !ok {
		return ports.ErrNotFound
	}
	if s.StateVersion != expectedVersion {
		return ports.ErrStateConflict
	}
	cp := *in
	cp.StateVersion = expectedVersion + 1
	r.s.sessions[in.ID] = &cp
	in.StateVersion = cp.StateVersion
	return nil
}

type runRepo struct{ s *Store }

func runKey(sessionID string, kind session.RunKind, key string) string {
	return fmt.Sprintf("%s|%s|%s", sessionID, kind, key)
}

func (r *runRepo) Create(_ context.Context, in *session.Run) (bool, *session.Run, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()

	key := runKey(in.SessionID, in.Kind, in.IdempotencyKey)
	if id, ok := r.s.runKeys[key]; ok {
		cp := *r.s.runs[id]
		return false, &cp, nil
	}
	cp := *in
	r.s.runs[in.ID] = &cp
	r.s.runKeys[key] = in.ID
	return true, nil, nil
}

func (r *runRepo) Get(_ context.Context, id string) (*session.Run, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	run, ok := r.s.runs[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	cp := *run
	return &cp, nil
}

func (r *runRepo) GetByIdempotencyKey(_ context.Context, sessionID string, kind session.RunKind, key string) (*session.Run, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	id, ok := r.s.runKeys[runKey(sessionID, kind, key)]
	if !ok {
		return nil, ports.ErrNotFound
	}
	cp := *r.s.runs[id]
	return &cp, nil
}

func (r *runRepo) GetRunningBySession(_ context.Context, sessionID string) (*session.Run, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	for _, run := range r.s.runs {
		if run.SessionID == sessionID && run.Status == session.RunStatusRunning {
			cp := *run
			return &cp, nil
		}
	}
	return nil, ports.ErrNotFound
}

func (r *runRepo) SetLastStep(_ context.Context, id, step string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	run, ok := r.s.runs[id]
	if !ok {
		return ports.ErrNotFound
	}
	run.LastStep = step
	run.UpdatedAt = time.Now().UTC()
	return nil
}

func (r *runRepo) MarkRunning(_ context.Context, id string, attempt int, at time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	run, ok := r.s.runs[id]
	if !ok {
		return ports.ErrNotFound
	}
	run.Status = session.RunStatusRunning
	run.Attempt = attempt
	run.UpdatedAt = at
	run.FinishedAt = nil
	return nil
}

func (r *runRepo) MarkFinished(_ context.Context, id string, status session.RunStatus, errorCode string, at time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	run, ok := r.s.runs[id]
	if !ok {
		return ports.ErrNotFound
	}
	run.Status = status
	run.ErrorCode = errorCode
	run.UpdatedAt = at
	run.FinishedAt = &at
	return nil
}

func (r *runRepo) MarkInterrupted(_ context.Context, id, lastStep string, at time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	run, ok := r.s.runs[id]
	if !ok {
		return ports.ErrNotFound
	}
	if run.Status != session.RunStatusRunning {
		return ports.ErrStateConflict
	}
	run.Status = session.RunStatusInterrupted
	run.LastStep = lastStep
	run.UpdatedAt = at
	run.FinishedAt = &at
	return nil
}

func (r *runRepo) ListStaleRunning(_ context.Context, staleBefore time.Time, limit int) ([]session.Run, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()

	var runs []session.Run
	for _, run := range r.s.runs {
		if run.Status == session.RunStatusRunning && run.UpdatedAt.Before(staleBefore) {
			runs = append(runs, *run)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].UpdatedAt.Before(runs[j].UpdatedAt) })
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

type stepRepo struct{ s *Store }

func stepKey(runID, stepName string) string { return runID + "|" + stepName }

func (r *stepRepo) Get(_ context.Context, runID, stepName string) (*session.RunStep, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	step, ok := r.s.steps[stepKey(runID, stepName)]
	if !ok {
		return nil, ports.ErrNotFound
	}
	cp := *step
	return &cp, nil
}

func (r *stepRepo) ListByRun(_ context.Context, runID string) ([]session.RunStep, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	var steps []session.RunStep
	for _, step := range r.s.steps {
		if step.RunID == runID {
			steps = append(steps, *step)
		}
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].StartedAt.Before(steps[j].StartedAt) })
	return steps, nil
}

func (r *stepRepo) Start(_ context.Context, in *session.RunStep) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	cp := *in
	cp.Status = session.StepStatusRunning
	cp.Output = nil
	cp.ErrorCode = ""
	cp.FinishedAt = nil
	r.s.steps[stepKey(in.RunID, in.StepName)] = &cp
	return nil
}

func (r *stepRepo) Complete(_ context.Context, runID, stepName string, output []byte, at time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	step, ok := r.s.steps[stepKey(runID, stepName)]
	if !ok {
		return ports.ErrNotFound
	}
	step.Status = session.StepStatusSucceeded
	step.Output = output
	step.ErrorCode = ""
	step.FinishedAt = &at
	return nil
}

func (r *stepRepo) Fail(_ context.Context, runID, stepName, errorCode string, at time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	step, ok := r.s.steps[stepKey(runID, stepName)]
	if !ok {
		return ports.ErrNotFound
	}
	step.Status = session.StepStatusFailed
	step.ErrorCode = errorCode
	step.FinishedAt = &at
	return nil
}

type eventRepo struct{ s *Store }

func (r *eventRepo) Append(_ context.Context, event *session.Event) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.eventSeq++
	cp := *event
	cp.ID = r.s.eventSeq
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	r.s.events[event.SessionID] = append(r.s.events[event.SessionID], cp)
	event.ID = cp.ID
	event.CreatedAt = cp.CreatedAt
	r.s.broadcast(event.SessionID)
	return nil
}

func (r *eventRepo) ListAfter(_ context.Context, sessionID string, afterID int64, limit int) ([]session.Event, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	var out []session.Event
	for _, e := range r.s.events[sessionID] {
		if e.ID > afterID {
			out = append(out, e)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

type turnRepo struct{ s *Store }

func (r *turnRepo) Create(_ context.Context, in *session.Turn) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.turns[in.ID]; ok {
		return ports.ErrStateConflict
	}
	for _, existing := range r.s.turns {
		if existing.SessionID == in.SessionID && existing.Seq == in.Seq {
			return ports.ErrStateConflict
		}
	}
	cp := *in
	r.s.turns[in.ID] = &cp
	return nil
}

func (r *turnRepo) Get(_ context.Context, id string) (*session.Turn, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	turn, ok := r.s.turns[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	cp := *turn
	return &cp, nil
}

func (r *turnRepo) UpdateAnswer(_ context.Context, in *session.Turn) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	turn, ok := r.s.turns[in.ID]
	if !ok {
		return ports.ErrNotFound
	}
	if turn.Status != session.TurnStatusPending {
		return ports.ErrStateConflict
	}
	turn.Status = in.Status
	turn.AnswerText = in.AnswerText
	turn.AnswerHash = in.AnswerHash
	turn.AnsweredAt = in.AnsweredAt
	turn.UpdatedAt = in.UpdatedAt
	return nil
}

func (r *turnRepo) ListBySession(_ context.Context, sessionID string) ([]session.Turn, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	var turns []session.Turn
	for _, turn := range r.s.turns {
		if turn.SessionID == sessionID {
			turns = append(turns, *turn)
		}
	}
	sort.Slice(turns, func(i, j int) bool { return turns[i].Seq < turns[j].Seq })
	return turns, nil
}

type planRepo struct{ s *Store }

func (r *planRepo) CreateItems(_ context.Context, items []session.PlanItem) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if len(items) == 0 {
		return nil
	}
	sessionID := items[0].SessionID
	for _, item := range items {
		for _, existing := range r.s.plans[item.SessionID] {
			if existing.Seq == item.Seq {
				return ports.ErrStateConflict
			}
		}
		r.s.plans[item.SessionID] = append(r.s.plans[item.SessionID], item)
	}
	sort.Slice(r.s.plans[sessionID], func(i, j int) bool {
		return r.s.plans[sessionID][i].Seq < r.s.plans[sessionID][j].Seq
	})
	return nil
}

func (r *planRepo) ListBySession(_ context.Context, sessionID string) ([]session.PlanItem, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	out := append([]session.PlanItem(nil), r.s.plans[sessionID]...)
	return out, nil
}

type evaluationRepo struct{ s *Store }

func (r *evaluationRepo) Upsert(_ context.Context, rec *evaluation.Record) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	cp := *rec
	r.s.evals[rec.TurnID] = &cp
	return nil
}

func (r *evaluationRepo) GetByTurn(_ context.Context, turnID string) (*evaluation.Record, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	rec, ok := r.s.evals[turnID]
	if !ok {
		return nil, ports.ErrNotFound
	}
	cp := *rec
	return &cp, nil
}

func (r *evaluationRepo) ListBySession(_ context.Context, sessionID string) ([]evaluation.Record, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	var out []evaluation.Record
	for _, turn := range r.s.turns {
		if turn.SessionID != sessionID {
			continue
		}
		if rec, ok := r.s.evals[turn.ID]; ok {
			out = append(out, *rec)
		}
	}
	return out, nil
}

type reportRepo struct{ s *Store }

func (r *reportRepo) Create(_ context.Context, in *domainreport.Record) (bool, *domainreport.Record, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if existing, ok := r.s.reports[in.SessionID]; ok {
		cp := *existing
		return false, &cp, nil
	}
	cp := *in
	r.s.reports[in.SessionID] = &cp
	return true, nil, nil
}

func (r *reportRepo) GetBySession(_ context.Context, sessionID string) (*domainreport.Record, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	rec, ok := r.s.reports[sessionID]
	if !ok {
		return nil, ports.ErrNotFound
	}
	cp := *rec
	return &cp, nil
}

type jdRepo struct{ s *Store }

func (r *jdRepo) Save(_ context.Context, listing *jd.Listing) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	cp := *listing
	r.s.jdListings[listing.ID] = &cp
	return nil
}

func (r *jdRepo) Get(_ context.Context, id string) (*jd.Listing, error) {
	r.s.mu.RLock()
	defer r.s.mu.RUnlock()
	listing, ok := r.s.jdListings[id]
	if !ok {
		return nil, jd.ErrNotFound
	}
	cp := *listing
	return &cp, nil
}
