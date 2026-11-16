package alert

import (
	"testing"
	"time"
)

func sample(dead, budget, stale uint64) Snapshot {
	return Snapshot{DeadLetters: dead, BudgetRejections: budget, StaleRuns: stale}
}

func TestFirstObservationOnlySeeds(t *testing.T) {
	m := New(DefaultRules())
	if alerts := m.Observe(sample(9, 9, 9), time.Now()); len(alerts) != 0 {
		t.Fatalf("restart must not replay history: %+v", alerts)
	}
}

func TestDeadLetterFiresOncePerCooldown(t *testing.T) {
	m := New(DefaultRules())
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	m.Observe(sample(0, 0, 0), now)

	alerts := m.Observe(sample(1, 0, 0), now.Add(time.Minute))
	if len(alerts) != 1 || alerts[0].Rule != "run_dead_letter" || alerts[0].Value != 1 {
		t.Fatalf("alerts = %+v", alerts)
	}

	// Same rule inside the 10 minute cooldown: suppressed.
	if alerts := m.Observe(sample(3, 0, 0), now.Add(5*time.Minute)); len(alerts) != 0 {
		t.Fatalf("cooldown not applied: %+v", alerts)
	}

	// After the cooldown a new dead letter fires again.
	alerts = m.Observe(sample(4, 0, 0), now.Add(11*time.Minute))
	if len(alerts) != 1 || alerts[0].Value != 1 {
		t.Fatalf("alerts after cooldown = %+v", alerts)
	}
}

func TestBudgetRejectionNeedsThreshold(t *testing.T) {
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

	below := New(DefaultRules())
	below.Observe(sample(0, 0, 0), now)
	if alerts := below.Observe(sample(0, 4, 0), now.Add(time.Minute)); len(alerts) != 0 {
		t.Fatalf("below threshold fired: %+v", alerts)
	}

	atThreshold := New(DefaultRules())
	atThreshold.Observe(sample(0, 0, 0), now)
	alerts := atThreshold.Observe(sample(0, 5, 0), now.Add(time.Minute))
	if len(alerts) != 1 || alerts[0].Rule != "model_budget_rejection" || alerts[0].Value != 5 {
		t.Fatalf("alerts = %+v", alerts)
	}
}

func TestStaleRunFires(t *testing.T) {
	m := New(DefaultRules())
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	m.Observe(sample(0, 0, 2), now)

	alerts := m.Observe(sample(0, 0, 3), now.Add(time.Minute))
	if len(alerts) != 1 || alerts[0].Signal != SignalStaleRuns || alerts[0].Value != 1 {
		t.Fatalf("alerts = %+v", alerts)
	}
}

func TestCounterResetDoesNotFire(t *testing.T) {
	m := New(DefaultRules())
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	m.Observe(sample(10, 10, 10), now)

	// Process restart zeroes the counters: the delta must be zero, not a huge
	// underflow.
	if alerts := m.Observe(sample(0, 0, 0), now.Add(time.Minute)); len(alerts) != 0 {
		t.Fatalf("reset produced alerts: %+v", alerts)
	}
}
