package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/platform/idgen"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

// RunExecutor executes an already-leased run. Runner implements it; tests can
// substitute a fake.
type RunExecutor interface {
	ExecuteClaimed(ctx context.Context, runID, leaseID string) error
}

// WorkerOptions configures one process-local worker pool.
type WorkerOptions struct {
	// WorkerID identifies this instance; each goroutine appends its slot number.
	WorkerID string
	// Concurrency caps how many runs this instance executes at the same time.
	Concurrency int
	// LeaseTTL is how long a claim stays valid without a heartbeat.
	LeaseTTL time.Duration
	// HeartbeatInterval renews the lease while a run executes. It must be
	// smaller than LeaseTTL; the pool clamps it to half the TTL when it is not.
	HeartbeatInterval time.Duration
	// PollInterval is the idle claim interval. Notify short-circuits it.
	PollInterval time.Duration
	// MaxFailures is used when a panic is converted into a run failure.
	MaxFailures int

	Logger  *slog.Logger
	Metrics *obs.Metrics
	Now     func() time.Time
}

// WorkerPool claims queued runs from PostgreSQL and executes them. Every claim
// is atomic (FOR UPDATE SKIP LOCKED), so several instances can consume the same
// table without duplicating work; an expired lease makes the run claimable
// again and the existing run_steps replay resumes from the last success.
type WorkerPool struct {
	store ports.Store
	exec  RunExecutor
	opts  WorkerOptions

	wakeCh chan struct{}
	stopCh chan struct{}
	stop   sync.Once

	loops sync.WaitGroup
	runs  sync.WaitGroup
}

// NewWorkerPool wires the pool into a Service: queued commands wake the pool
// instead of executing inline.
func NewWorkerPool(svc *Service, opts WorkerOptions) *WorkerPool {
	if opts.WorkerID == "" {
		opts.WorkerID = defaultWorkerID()
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 2
	}
	if opts.LeaseTTL <= 0 {
		opts.LeaseTTL = 30 * time.Second
	}
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = opts.LeaseTTL / 3
	}
	if opts.HeartbeatInterval >= opts.LeaseTTL {
		opts.HeartbeatInterval = opts.LeaseTTL / 2
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = time.Second
	}
	if opts.MaxFailures <= 0 {
		opts.MaxFailures = 3
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Metrics == nil {
		opts.Metrics = obs.NewMetrics()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	pool := &WorkerPool{
		store:  svc.store,
		exec:   svc.runner,
		opts:   opts,
		wakeCh: make(chan struct{}, 1),
		stopCh: make(chan struct{}),
	}
	svc.workerMode = true
	svc.wake = pool.Notify
	return pool
}

// Start launches the configured number of claim loops.
func (p *WorkerPool) Start(ctx context.Context) {
	for i := 0; i < p.opts.Concurrency; i++ {
		workerID := fmt.Sprintf("%s-%d", p.opts.WorkerID, i+1)
		p.loops.Add(1)
		go p.loop(ctx, workerID)
	}
}

// Stop stops idle loops. In-flight runs keep going until Wait returns or the
// caller's grace period expires.
func (p *WorkerPool) Stop() {
	p.stop.Do(func() { close(p.stopCh) })
}

// Wait waits for claim loops and in-flight runs.
func (p *WorkerPool) Wait() {
	p.loops.Wait()
	p.runs.Wait()
}

// Notify wakes one idle loop. It never blocks: a single pending wake-up is
// enough because each loop claims until the queue or its slot is full.
func (p *WorkerPool) Notify() {
	select {
	case p.wakeCh <- struct{}{}:
	default:
	}
}

func (p *WorkerPool) loop(ctx context.Context, workerID string) {
	defer p.loops.Done()

	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		case <-p.wakeCh:
		case <-timer.C:
		}

		p.claimOne(ctx, workerID)
		timer.Reset(p.opts.PollInterval)
	}
}

func (p *WorkerPool) claimOne(ctx context.Context, workerID string) {
	now := p.opts.Now().UTC()
	leaseID := idgen.New()
	runs, err := p.store.Repos().Runs.Claim(ctx, workerID, leaseID, now, now.Add(p.opts.LeaseTTL), 1)
	if err != nil {
		if ctx.Err() == nil {
			p.opts.Logger.Error("claim runs", "worker_id", workerID, "error", err)
		}
		return
	}
	if len(runs) == 0 {
		return
	}

	run := runs[0]
	p.opts.Metrics.IncWorkerClaim()
	p.opts.Logger.Info("run claimed",
		"worker_id", workerID, "lease_id", leaseID, "run_id", run.ID,
		"session_id", run.SessionID, "kind", string(run.Kind), "attempt", run.Attempt)

	p.runs.Add(1)
	go p.runClaimed(ctx, workerID, leaseID, run)
}

func (p *WorkerPool) runClaimed(parent context.Context, workerID, leaseID string, run session.Run) {
	defer p.runs.Done()

	base := context.WithoutCancel(parent)
	runCtx, cancel := context.WithTimeout(base, 5*time.Minute)
	defer cancel()
	runCtx = reqctx.WithWorker(runCtx, workerID, leaseID)
	runCtx = reqctx.WithRun(runCtx, run.ID)
	runCtx = reqctx.WithSession(runCtx, run.SessionID)
	log := p.opts.Logger
	if attrs := reqctx.Attrs(runCtx); len(attrs) > 0 {
		log = log.With(attrs...)
	}

	// Renew the lease until execution finishes. Losing ownership cancels the
	// local execution so a stale worker does not keep writing business state.
	leaseCtx, stopRenew := context.WithCancel(runCtx)
	renewDone := make(chan struct{})
	go p.renewLease(leaseCtx, log, run.ID, leaseID, cancel, renewDone)

	exitCode := 0
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				exitCode = 1
				log.Error("run execution panicked", "panic", fmt.Sprint(rec))
				caught := fmt.Errorf("panic: %v", rec)
				_, _, _ = p.store.Repos().Runs.RecordFailure(
					context.WithoutCancel(runCtx), run.ID, leaseID, runErrorStepFailed, "panic",
					truncateError(caught), p.opts.Now().UTC(), p.opts.MaxFailures)
			}
		}()
		if err := p.exec.ExecuteClaimed(runCtx, run.ID, leaseID); err != nil {
			exitCode = 1
			log.Error("execute claimed run", "error", err)
		}
	}()

	stopRenew()
	<-renewDone

	releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	_ = p.store.Repos().Runs.ReleaseLease(releaseCtx, run.ID, leaseID)
	releaseCancel()

	if exitCode == 0 {
		log.Info("run execution finished")
	}
}

func (p *WorkerPool) renewLease(ctx context.Context, log *slog.Logger, runID, leaseID string, cancel context.CancelFunc, done chan<- struct{}) {
	defer close(done)

	ticker := time.NewTicker(p.opts.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			callCtx, cancelCall := context.WithTimeout(ctx, 5*time.Second)
			ok, err := p.store.Repos().Runs.RenewLease(callCtx, runID, leaseID, p.opts.Now().UTC().Add(p.opts.LeaseTTL))
			cancelCall()
			if err != nil {
				p.opts.Metrics.IncLeaseRenewFailure()
				log.Warn("renew run lease", "error", err)
				continue
			}
			if !ok {
				p.opts.Metrics.IncLeaseRenewFailure()
				log.Warn("run lease lost; cancelling local execution")
				cancel()
				return
			}
		}
	}
}

// defaultWorkerID is stable per process and unique across instances: hostname
// and pid are enough for a single host, the ULID suffix guards container reuse.
func defaultWorkerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), idgen.New())
}
