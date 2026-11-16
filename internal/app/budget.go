package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

var (
	// ErrUserBudgetExceeded is checked before the session scope so an exhausted
	// user cannot keep spending in sessions that still have room.
	ErrUserBudgetExceeded = errors.New("user model budget exceeded")
	// ErrSessionBudgetExceeded means the user still has room but this one
	// session does not.
	ErrSessionBudgetExceeded = errors.New("session model budget exceeded")
)

// BudgetGuard is run-level admission control for model spend. Checks happen
// once per run, before the run row is created, so a run costs at most one
// database round trip and the overshoot is bounded by the spend of the run
// already in flight. Per-step checks would multiply latency without changing
// the hard limits the product promises.
type BudgetGuard struct {
	metrics *obs.Metrics
}

func NewBudgetGuard(metrics *obs.Metrics) *BudgetGuard {
	return &BudgetGuard{metrics: metrics}
}

// Check enforces the documented priority: user total first, session second.
// A user rejection returns immediately, so the session lookup is not even
// attempted. An unconfigured scope means "no limit"; a storage error is
// fail-closed because continuing to spend on a broken ledger is worse than a
// visible failure.
func (g *BudgetGuard) Check(ctx context.Context, repos ports.Repositories, userID, sessionID string) error {
	if repos.Budgets == nil {
		return nil
	}

	if userID != "" {
		limit, ok, err := repos.Budgets.GetLimit(ctx, ports.BudgetScopeUser, userID)
		if err != nil {
			return fmt.Errorf("read user budget limit: %w", err)
		}
		if ok {
			spent, err := repos.Budgets.UserSpendTokens(ctx, userID)
			if err != nil {
				return fmt.Errorf("read user budget spend: %w", err)
			}
			if spent >= limit {
				g.metrics.IncBudgetRejection(ports.BudgetScopeUser)
				return fmt.Errorf("%w: spent %d of %d tokens", ErrUserBudgetExceeded, spent, limit)
			}
		}
	}

	limit, ok, err := repos.Budgets.GetLimit(ctx, ports.BudgetScopeSession, sessionID)
	if err != nil {
		return fmt.Errorf("read session budget limit: %w", err)
	}
	if !ok {
		return nil
	}
	spent, err := repos.Budgets.SessionSpendTokens(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("read session budget spend: %w", err)
	}
	if spent >= limit {
		g.metrics.IncBudgetRejection(ports.BudgetScopeSession)
		return fmt.Errorf("%w: spent %d of %d tokens", ErrSessionBudgetExceeded, spent, limit)
	}
	return nil
}
