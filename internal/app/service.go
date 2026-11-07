package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/agent/report"
	"github.com/waiting2050/ai-interview-agent/internal/domain/domainpack"
	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/domain/user"
	"github.com/waiting2050/ai-interview-agent/internal/platform/idgen"
	platformlock "github.com/waiting2050/ai-interview-agent/internal/platform/lock"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

var (
	// ErrInvalidArgument marks input that fails validation (HTTP 400).
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrRunInProgress marks a session that already has a running run.
	ErrRunInProgress = errors.New("run in progress")
	// ErrIdempotencyReused marks a key reused with a different request body.
	ErrIdempotencyReused = ports.ErrIdempotencyReused
)

type RunInProgressError struct {
	RunID string
}

// ownerOrLegacy keeps historical rows and direct internal calls on the
// pre-auth placeholder account instead of silently writing an empty owner.
func ownerOrLegacy(ownerID string) string {
	if strings.TrimSpace(ownerID) == "" {
		return user.LegacyID
	}
	return ownerID
}

func (e *RunInProgressError) Error() string {
	return fmt.Sprintf("%s: run %s", ErrRunInProgress, e.RunID)
}

func (e *RunInProgressError) Unwrap() error { return ErrRunInProgress }

type Options struct {
	Logger        *slog.Logger
	Now           func() time.Time
	RunStaleAfter time.Duration
	Steps         StartSteps
	// Evaluator drives the evaluate node of an answer run.
	Evaluator Evaluator
	// QuestionGenerator drives both the start question and answer follow-ups /
	// next questions. When nil a heuristic generator is used.
	QuestionGenerator *interview.QuestionGenerator
	// AnswerSteps replaces individual answer nodes; zero values fall back to
	// the defaults built from Evaluator and QuestionGenerator.
	AnswerSteps AnswerSteps
	// AnswerGraph is the compiled answer graph. When nil it is compiled here;
	// the server compiles it at startup so wiring errors fail fast.
	AnswerGraph *interview.AnswerGraph
	// ReportGenerator produces the end-of-interview report. When nil a
	// heuristic generator is used so demos and tests need no API key.
	ReportGenerator report.Generator
	// Locker serialises session commands. The server injects the Redis
	// implementation; nil falls back to the process-local test double.
	Locker ports.Locker
	// Metrics is the process-wide registry behind /metrics.
	Metrics *obs.Metrics
	// Usage aggregates model calls and tokens per session for the report.
	Usage *obs.UsageTracker
	// Synchronous executes the run inline. Used by unit tests; the server
	// keeps the default asynchronous behaviour.
	Synchronous bool
}

type Service struct {
	store       ports.Store
	runner      *Runner
	log         *slog.Logger
	now         func() time.Time
	staleAfter  time.Duration
	synchronous bool
	metrics     *obs.Metrics

	locks ports.Locker
	wg    sync.WaitGroup
}

func New(store ports.Store, opts Options) *Service {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.RunStaleAfter <= 0 {
		opts.RunStaleAfter = 60 * time.Second
	}
	if opts.QuestionGenerator == nil {
		opts.QuestionGenerator = interview.NewQuestionGenerator(interview.NewHeuristicClient(), "fake-heuristic")
	}
	if opts.Steps.Plan == nil || opts.Steps.Ask == nil {
		opts.Steps = DefaultStartSteps(opts.QuestionGenerator)
	}
	if opts.ReportGenerator == nil {
		opts.ReportGenerator = report.NewBuilder(interview.NewHeuristicClient(), "fake-heuristic")
	}
	if opts.Locker == nil {
		opts.Locker = platformlock.NewProcessLocker()
	}
	if opts.Metrics == nil {
		opts.Metrics = obs.NewMetrics()
	}
	if opts.Usage == nil {
		opts.Usage = obs.NewUsageTracker()
	}

	answerSteps := opts.AnswerSteps
	defaults := DefaultAnswerSteps(opts.Evaluator, opts.QuestionGenerator)
	if answerSteps.Evaluate == nil {
		answerSteps.Evaluate = defaults.Evaluate
	}
	if answerSteps.AskFollowup == nil {
		answerSteps.AskFollowup = defaults.AskFollowup
	}
	if answerSteps.AskNext == nil {
		answerSteps.AskNext = defaults.AskNext
	}

	answerGraph := opts.AnswerGraph
	if answerGraph == nil {
		answerGraph = interview.NewAnswerGraph(context.Background())
	}
	if err := answerGraph.Err(); err != nil {
		opts.Logger.Error("answer graph compilation failed", "error", err)
	}

	return &Service{
		store: store,
		runner: NewRunner(store, opts.Steps, answerSteps, answerGraph, opts.ReportGenerator,
			opts.Now, opts.Logger, opts.Metrics, opts.Usage),
		log:         opts.Logger,
		now:         opts.Now,
		staleAfter:  opts.RunStaleAfter,
		synchronous: opts.Synchronous,
		metrics:     opts.Metrics,
		locks:       opts.Locker,
	}
}

type CreateSessionInput struct {
	// OwnerID is the account that will own the session. Empty is treated as
	// the pre-auth legacy owner so internal callers and old tests keep working.
	OwnerID       string
	CandidateName string
	TargetRole    string
	ResumeText    string
	ResumeProfile resume.Profile
	JDText        string
	// Domain selects the interview domain pack. Empty means go-backend.
	Domain string
	Config session.Config
}

func (s *Service) CreateSession(ctx context.Context, in CreateSessionInput) (*session.Session, error) {
	if in.TargetRole == "" {
		return nil, fmt.Errorf("%w: target_role is required", ErrInvalidArgument)
	}
	if in.ResumeText == "" && in.ResumeProfile.IsZero() {
		return nil, fmt.Errorf("%w: resume_text or resume_profile is required", ErrInvalidArgument)
	}
	if in.JDText == "" {
		return nil, fmt.Errorf("%w: jd_text is required", ErrInvalidArgument)
	}

	domain := strings.TrimSpace(in.Domain)
	if domain == "" {
		domain = domainpack.LegacyGoBackendID
	}
	if _, ok := domainpack.Default().Get(domain); !ok {
		return nil, fmt.Errorf("%w: unknown domain %q", ErrInvalidArgument, domain)
	}

	in.Config.Normalize()
	if err := in.Config.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidArgument, err)
	}

	now := s.now().UTC()
	profile := in.ResumeProfile
	if profile.IsZero() {
		profile = resume.Parse(in.ResumeText)
	}
	profile.Normalize()

	sess := &session.Session{
		ID:            idgen.New(),
		OwnerID:       ownerOrLegacy(in.OwnerID),
		CandidateName: in.CandidateName,
		TargetRole:    in.TargetRole,
		ResumeText:    in.ResumeText,
		ResumeProfile: profile,
		JDText:        in.JDText,
		Domain:        domain,
		Config:        in.Config,
		Status:        session.StatusCreated,
		StateVersion:  0,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	err := s.store.WithTx(ctx, func(ctx context.Context, repos ports.Repositories) error {
		if err := repos.Sessions.Create(ctx, sess); err != nil {
			return err
		}
		return appendEvent(ctx, s.logger(ctx), repos.Events, sess.ID, "", session.EventSessionCreated, map[string]any{
			"status": string(sess.Status),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	s.logger(ctx).Info("session created", "session_id", sess.ID, "target_role", sess.TargetRole)
	return sess, nil
}

func (s *Service) GetSession(ctx context.Context, id string) (*session.Session, error) {
	return s.store.Repos().Sessions.Get(ctx, id)
}

type StartCommand struct {
	SessionID      string
	IdempotencyKey string
	// RequestBody is hashed to detect an idempotency key reused for a
	// different payload.
	RequestBody []byte
}

type StartResult struct {
	RunID    string
	Status   session.RunStatus
	Replayed bool
}

func (s *Service) Start(ctx context.Context, cmd StartCommand) (StartResult, error) {
	ctx = reqctx.WithSession(ctx, cmd.SessionID)
	unlock, err := s.acquireSession(ctx, cmd.SessionID)
	if err != nil {
		return StartResult{}, err
	}
	defer unlock()

	key := cmd.IdempotencyKey
	if key == "" {
		key = deriveIdempotencyKey(session.RunKindStart, cmd.SessionID, cmd.RequestBody)
	}
	requestHash := requestHash(session.RunKindStart, cmd.SessionID, cmd.RequestBody)

	// Idempotency is checked before the session status guard: a retried
	// command must return the original run even after the session advanced.
	if existing, err := s.store.Repos().Runs.GetByIdempotencyKey(ctx, cmd.SessionID, session.RunKindStart, key); err == nil {
		return replayStartResult(existing, requestHash)
	} else if !errors.Is(err, ports.ErrNotFound) {
		return StartResult{}, fmt.Errorf("lookup idempotency key: %w", err)
	}

	sess, err := s.store.Repos().Sessions.Get(ctx, cmd.SessionID)
	if err != nil {
		return StartResult{}, err
	}
	if sess.Status != session.StatusCreated {
		return StartResult{}, fmt.Errorf("%w: session is %s, start requires created",
			ports.ErrStateConflict, sess.Status)
	}
	if sess.ResumeText == "" || sess.JDText == "" {
		return StartResult{}, fmt.Errorf("%w: resume_text and jd_text are required before start", ErrInvalidArgument)
	}

	if running, err := s.store.Repos().Runs.GetRunningBySession(ctx, cmd.SessionID); err == nil {
		return StartResult{}, &RunInProgressError{RunID: running.ID}
	} else if !errors.Is(err, ports.ErrNotFound) {
		return StartResult{}, fmt.Errorf("check running run: %w", err)
	}

	now := s.now().UTC()
	run := &session.Run{
		ID:             idgen.New(),
		SessionID:      cmd.SessionID,
		Kind:           session.RunKindStart,
		Status:         session.RunStatusRunning,
		IdempotencyKey: key,
		RequestHash:    requestHash,
		Attempt:        0,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	var (
		created  bool
		existing *session.Run
	)
	err = s.store.WithTx(ctx, func(ctx context.Context, repos ports.Repositories) error {
		created, existing, err = repos.Runs.Create(ctx, run)
		if err != nil {
			return err
		}
		if !created {
			return nil
		}
		return appendEvent(ctx, s.logger(ctx), repos.Events, cmd.SessionID, run.ID, session.EventRunStarted, map[string]any{
			"run_id": run.ID,
			"kind":   string(run.Kind),
		})
	})
	if err != nil {
		return StartResult{}, fmt.Errorf("create start run: %w", err)
	}

	if !created {
		return replayStartResult(existing, requestHash)
	}

	s.logger(ctx).Info("start run accepted", "run_id", run.ID, "kind", string(run.Kind))
	s.launch(ctx, run.ID)

	return StartResult{RunID: run.ID, Status: session.RunStatusRunning}, nil
}

type AnswerCommand struct {
	SessionID      string
	TurnID         string
	Answer         string
	IdempotencyKey string
}

// Answer accepts one candidate reply and enqueues an answer run. The turn
// must be the pending one; answers longer than maxAnswerRunes are truncated
// and the truncation is recorded in the answer.received event.
func (s *Service) Answer(ctx context.Context, cmd AnswerCommand) (StartResult, error) {
	ctx = reqctx.WithSession(ctx, cmd.SessionID)
	unlock, err := s.acquireSession(ctx, cmd.SessionID)
	if err != nil {
		return StartResult{}, err
	}
	defer unlock()

	turnID := strings.TrimSpace(cmd.TurnID)
	answer := strings.TrimSpace(cmd.Answer)
	if turnID == "" {
		return StartResult{}, fmt.Errorf("%w: turn_id is required", ErrInvalidArgument)
	}
	if answer == "" {
		return StartResult{}, fmt.Errorf("%w: answer is required", ErrInvalidArgument)
	}
	ctx = reqctx.WithTurn(ctx, turnID)

	truncated := false
	if r := []rune(answer); len(r) > maxAnswerRunes {
		answer = string(r[:maxAnswerRunes])
		truncated = true
	}
	body, err := json.Marshal(AnswerInput{TurnID: turnID, Answer: answer, Truncated: truncated})
	if err != nil {
		return StartResult{}, fmt.Errorf("encode answer input: %w", err)
	}

	key := cmd.IdempotencyKey
	if key == "" {
		key = deriveIdempotencyKey(session.RunKindAnswer, cmd.SessionID, body)
	}
	requestHash := requestHash(session.RunKindAnswer, cmd.SessionID, body)

	if existing, err := s.store.Repos().Runs.GetByIdempotencyKey(ctx, cmd.SessionID, session.RunKindAnswer, key); err == nil {
		return replayStartResult(existing, requestHash)
	} else if !errors.Is(err, ports.ErrNotFound) {
		return StartResult{}, fmt.Errorf("lookup idempotency key: %w", err)
	}

	sess, err := s.store.Repos().Sessions.Get(ctx, cmd.SessionID)
	if err != nil {
		return StartResult{}, err
	}
	if sess.Status != session.StatusActive || sess.Phase != session.PhaseAwaitingAnswer {
		return StartResult{}, fmt.Errorf("%w: session is %s/%s, answers require active/awaiting_answer",
			ports.ErrStateConflict, sess.Status, sess.Phase)
	}
	if sess.PendingTurnID != turnID {
		return StartResult{}, fmt.Errorf("%w: turn %s is not the pending turn", ports.ErrStateConflict, turnID)
	}

	turn, err := s.store.Repos().Turns.Get(ctx, turnID)
	if err != nil {
		return StartResult{}, err
	}
	if turn.SessionID != cmd.SessionID {
		return StartResult{}, ports.ErrNotFound
	}
	if !turn.IsPending() {
		return StartResult{}, fmt.Errorf("%w: turn %s is already answered", ports.ErrStateConflict, turnID)
	}

	if running, err := s.store.Repos().Runs.GetRunningBySession(ctx, cmd.SessionID); err == nil {
		return StartResult{}, &RunInProgressError{RunID: running.ID}
	} else if !errors.Is(err, ports.ErrNotFound) {
		return StartResult{}, fmt.Errorf("check running run: %w", err)
	}

	now := s.now().UTC()
	run := &session.Run{
		ID:             idgen.New(),
		SessionID:      cmd.SessionID,
		Kind:           session.RunKindAnswer,
		Status:         session.RunStatusRunning,
		IdempotencyKey: key,
		RequestHash:    requestHash,
		Input:          body,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	var (
		created  bool
		existing *session.Run
	)
	err = s.store.WithTx(ctx, func(ctx context.Context, repos ports.Repositories) error {
		created, existing, err = repos.Runs.Create(ctx, run)
		if err != nil {
			return err
		}
		if !created {
			return nil
		}
		return appendEvent(ctx, s.logger(ctx), repos.Events, cmd.SessionID, run.ID, session.EventRunStarted, map[string]any{
			"run_id":  run.ID,
			"kind":    string(run.Kind),
			"turn_id": turnID,
		})
	})
	if err != nil {
		return StartResult{}, fmt.Errorf("create answer run: %w", err)
	}

	if !created {
		return replayStartResult(existing, requestHash)
	}

	s.logger(ctx).Info("answer run accepted", "run_id", run.ID, "kind", string(run.Kind),
		"chars", len([]rune(answer)), "truncated", truncated)
	s.launch(ctx, run.ID)

	return StartResult{RunID: run.ID, Status: session.RunStatusRunning}, nil
}

func (s *Service) Abort(ctx context.Context, sessionID string) error {
	ctx = reqctx.WithSession(ctx, sessionID)
	unlock, err := s.acquireSession(ctx, sessionID)
	if err != nil {
		return err
	}
	defer unlock()

	now := s.now().UTC()
	return s.store.WithTx(ctx, func(ctx context.Context, repos ports.Repositories) error {
		sess, err := repos.Sessions.Get(ctx, sessionID)
		if err != nil {
			return err
		}
		if sess.Status == session.StatusAborted {
			return nil
		}

		expected := sess.StateVersion
		if err := sess.Abort(now); err != nil {
			return err
		}
		if err := repos.Sessions.UpdateState(ctx, sess, expected); err != nil {
			return err
		}
		if err := appendEvent(ctx, s.logger(ctx), repos.Events, sess.ID, "", session.EventSessionAborted, map[string]any{
			"status": string(sess.Status),
		}); err != nil {
			return err
		}
		return appendEvent(ctx, s.logger(ctx), repos.Events, sess.ID, "", session.EventSessionPhase, map[string]any{
			"status":        string(sess.Status),
			"phase":         string(sess.Phase),
			"state_version": sess.StateVersion,
		})
	})
}

func (s *Service) ListEvents(ctx context.Context, sessionID string, afterID int64, limit int) ([]session.Event, error) {
	return s.store.Repos().Events.ListAfter(ctx, sessionID, afterID, limit)
}

// SubscribeEvents exposes the store's change signal to the SSE handler. The
// signal carries no payload: the handler always re-reads session_events so
// Last-Event-ID replay stays authoritative and duplicate-free.
func (s *Service) SubscribeEvents(ctx context.Context, sessionID string) (<-chan struct{}, error) {
	return s.store.SubscribeEvents(ctx, sessionID)
}

// Retry explicitly re-executes a failed or interrupted run. Succeeded steps
// are replayed from run_steps, so no duplicate business writes occur.
func (s *Service) Retry(ctx context.Context, sessionID, runID string) error {
	ctx = reqctx.WithSession(ctx, sessionID)
	unlock, err := s.acquireSession(ctx, sessionID)
	if err != nil {
		return err
	}
	defer unlock()

	run, err := s.store.Repos().Runs.Get(ctx, runID)
	if err != nil {
		return err
	}
	if run.SessionID != sessionID {
		return ports.ErrNotFound
	}
	switch run.Status {
	case session.RunStatusRunning:
		return &RunInProgressError{RunID: run.ID}
	case session.RunStatusSucceeded:
		return fmt.Errorf("%w: run already succeeded", ports.ErrStateConflict)
	}

	s.launch(ctx, run.ID)
	return nil
}

// Reconcile detects runs left running by a crashed process, marks them
// interrupted, emits an event and resumes them from the last succeeded step.
func (s *Service) Reconcile(ctx context.Context) (int, error) {
	cutoff := s.now().UTC().Add(-s.staleAfter)
	runs, err := s.store.Repos().Runs.ListStaleRunning(ctx, cutoff, 50)
	if err != nil {
		return 0, fmt.Errorf("list stale runs: %w", err)
	}

	resumed := 0
	for _, run := range runs {
		if err := s.store.Repos().Runs.MarkInterrupted(ctx, run.ID, run.LastStep, s.now().UTC()); err != nil {
			if errors.Is(err, ports.ErrStateConflict) {
				continue
			}
			return resumed, fmt.Errorf("mark run %s interrupted: %w", run.ID, err)
		}
		runCtx := reqctx.WithRun(reqctx.WithSession(ctx, run.SessionID), run.ID)
		if err := appendEvent(runCtx, s.logger(runCtx), s.store.Repos().Events, run.SessionID, run.ID, session.EventRunInterrupted, map[string]any{
			"run_id":    run.ID,
			"last_step": run.LastStep,
		}); err != nil {
			s.logger(runCtx).Error("append run interrupted event", "error", err)
		}

		s.logger(runCtx).Warn("resuming interrupted run", "last_step", run.LastStep)
		if err := s.runner.Execute(runCtx, run.ID); err != nil {
			s.logger(runCtx).Error("resume interrupted run", "error", err)
			continue
		}
		resumed++
	}
	return resumed, nil
}

func (s *Service) Wait() {
	s.wg.Wait()
}

// launch runs a queued run. The request-scoped identifiers survive into the
// background execution so command, step and model logs share one trace.
func (s *Service) launch(ctx context.Context, runID string) {
	execute := func(ctx context.Context) {
		if err := s.runner.Execute(ctx, runID); err != nil {
			s.logger(ctx).Error("execute run", "run_id", runID, "error", err)
		}
	}

	if s.synchronous {
		execute(ctx)
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		base := context.WithoutCancel(ctx)
		ctx, cancel := context.WithTimeout(base, 5*time.Minute)
		defer cancel()
		execute(ctx)
	}()
}

func (s *Service) logger(ctx context.Context) *slog.Logger {
	if attrs := reqctx.Attrs(ctx); len(attrs) > 0 {
		return s.log.With(attrs...)
	}
	return s.log
}

func replayStartResult(existing *session.Run, requestHash string) (StartResult, error) {
	if existing.RequestHash != requestHash {
		return StartResult{}, ErrIdempotencyReused
	}
	if existing.Status == session.RunStatusRunning {
		return StartResult{}, &RunInProgressError{RunID: existing.ID}
	}
	return StartResult{RunID: existing.ID, Status: existing.Status, Replayed: true}, nil
}

func deriveIdempotencyKey(kind session.RunKind, sessionID string, body []byte) string {
	sum := sha256.Sum256(append([]byte(string(kind)+"\n"+sessionID+"\n"), body...))
	return "auto-" + hex.EncodeToString(sum[:])
}

func requestHash(kind session.RunKind, sessionID string, body []byte) string {
	sum := sha256.Sum256(append([]byte(string(kind)+"\n"+sessionID+"\n"), body...))
	return hex.EncodeToString(sum[:])
}

// acquireSession takes the cross-process session lock and returns a best-effort
// release. A busy lock is surfaced as ports.ErrLockBusy so the API can answer
// 409 + retryable instead of dropping the command.
func (s *Service) acquireSession(ctx context.Context, sessionID string) (func(), error) {
	token := idgen.New()
	key := "lock:session:" + sessionID
	if err := s.locks.Acquire(ctx, key, token); err != nil {
		if errors.Is(err, ports.ErrLockBusy) {
			s.metrics.IncLockConflict()
		}
		s.logger(ctx).Warn("session lock acquire failed", "error_class", lockErrorClass(err))
		return nil, err
	}
	return func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := s.locks.Release(releaseCtx, key, token); err != nil {
			s.logger(ctx).Warn("release session lock", "error", err)
		}
	}, nil
}
