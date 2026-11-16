// Package alert turns existing counters into in-process alerts.
//
// There is deliberately no Alertmanager, webhook or extra service: the three
// signals already exist (dead letters, budget rejections, stale runs), so the
// only missing piece is a rule that compares two samples. Everything here is
// pull-based, deterministic and testable without a clock.
package alert

import (
	"context"
	"time"
)

// Signal names the counter a rule reads.
type Signal string

const (
	SignalDeadLetters      Signal = "dead_letters"
	SignalBudgetRejections Signal = "budget_rejections"
	SignalStaleRuns        Signal = "stale_runs"
)

// Rule fires when the counter delta since the previous sample reaches
// Threshold, unless the rule fired within Cooldown.
type Rule struct {
	Name      string
	Severity  string
	Signal    Signal
	Threshold uint64
	Cooldown  time.Duration
	Message   string
}

// Snapshot holds cumulative counters, in the same units Prometheus exposes.
type Snapshot struct {
	DeadLetters      uint64
	BudgetRejections uint64
	StaleRuns        uint64
}

// Alert is one emission. Value is the delta that triggered the rule.
type Alert struct {
	Rule     string
	Severity string
	Signal   Signal
	Value    uint64
	Message  string
	At       time.Time
}

// Monitor keeps the previous sample and the last emission per rule. It is not
// safe for concurrent use: the server runs it in a single goroutine.
type Monitor struct {
	rules     []Rule
	prev      Snapshot
	primed    bool
	lastFired map[string]time.Time
}

func New(rules []Rule) *Monitor {
	return &Monitor{rules: rules, lastFired: map[string]time.Time{}}
}

// DefaultRules is what the server starts with. Thresholds are deliberately
// low: a single dead letter or stale run already means a request was lost or a
// process died, while budget rejections are aggregated because one user
// hitting a limit is normal.
func DefaultRules() []Rule {
	return []Rule{
		{
			Name:      "run_dead_letter",
			Severity:  "error",
			Signal:    SignalDeadLetters,
			Threshold: 1,
			Cooldown:  10 * time.Minute,
			Message:   "有 run 进入死信，检查 last_error 与重试次数",
		},
		{
			Name:      "model_budget_rejection",
			Severity:  "warn",
			Signal:    SignalBudgetRejections,
			Threshold: 5,
			Cooldown:  15 * time.Minute,
			Message:   "预算拒绝在采样窗口内达到 5 次，检查用户/会话额度是否配置过低",
		},
		{
			Name:      "stale_run_detected",
			Severity:  "warn",
			Signal:    SignalStaleRuns,
			Threshold: 1,
			Cooldown:  10 * time.Minute,
			Message:   "崩溃恢复扫描发现 stale run，检查进程是否异常退出",
		},
	}
}

// Observe compares the new sample with the previous one and returns the alerts
// that should be logged. The first call only seeds state: restarting the
// process must not replay historical dead letters as new alerts. Counter
// resets (a restart between samples) are treated as zero delta.
func (m *Monitor) Observe(s Snapshot, now time.Time) []Alert {
	if !m.primed {
		m.prev = s
		m.primed = true
		return nil
	}

	delta := Snapshot{
		DeadLetters:      sub(s.DeadLetters, m.prev.DeadLetters),
		BudgetRejections: sub(s.BudgetRejections, m.prev.BudgetRejections),
		StaleRuns:        sub(s.StaleRuns, m.prev.StaleRuns),
	}
	m.prev = s

	alerts := make([]Alert, 0, len(m.rules))
	for _, rule := range m.rules {
		value := delta.value(rule.Signal)
		if value < rule.Threshold {
			continue
		}
		if last, ok := m.lastFired[rule.Name]; ok && now.Sub(last) < rule.Cooldown {
			continue
		}
		m.lastFired[rule.Name] = now
		alerts = append(alerts, Alert{
			Rule:     rule.Name,
			Severity: rule.Severity,
			Signal:   rule.Signal,
			Value:    value,
			Message:  rule.Message,
			At:       now,
		})
	}
	return alerts
}

func (s Snapshot) value(signal Signal) uint64 {
	switch signal {
	case SignalDeadLetters:
		return s.DeadLetters
	case SignalBudgetRejections:
		return s.BudgetRejections
	case SignalStaleRuns:
		return s.StaleRuns
	default:
		return 0
	}
}

func sub(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

// Run samples on an interval until ctx is cancelled. The first sample happens
// immediately, so the rule set is active as soon as the server starts.
func Run(ctx context.Context, monitor *Monitor, sample func() Snapshot, emit func(Alert), interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		for _, a := range monitor.Observe(sample(), time.Now().UTC()) {
			emit(a)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
