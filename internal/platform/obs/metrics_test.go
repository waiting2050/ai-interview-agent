package obs

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestMetricsCountersAndRendering(t *testing.T) {
	m := NewMetrics()

	m.IncModelCall("openai", "gpt-test", "success")
	m.IncModelCall("openai", "gpt-test", "success")
	m.IncModelCall("openai", "gpt-test", "error")
	m.IncModelCallFailure("openai", "gpt-test", "timeout")
	m.AddModelTokens("openai", "gpt-test", 120, 30)
	m.IncEvaluationDegraded("model_output_invalid")
	m.IncReportDegraded("model_unavailable")
	m.IncLockAttempt()
	m.IncLockAttempt()
	m.IncLockConflict()
	m.IncRunFailure("answer", "timeout")
	m.IncLeaseRenewAttempt()
	m.IncLeaseRenewAttempt()
	m.IncLeaseRenewFailure()
	m.IncSSEReconnect()
	m.ObserveEventDelivery(25 * time.Millisecond)

	if got := m.ModelCallTotal("openai", "gpt-test", "success"); got != 2 {
		t.Fatalf("success calls = %d", got)
	}
	if got := m.ModelCallTotal("openai", "gpt-test", "error"); got != 1 {
		t.Fatalf("error calls = %d", got)
	}
	if got := m.TokenTotal("openai", "gpt-test", "input"); got != 120 {
		t.Fatalf("input tokens = %d", got)
	}
	if got := m.TokenTotal("openai", "gpt-test", "output"); got != 30 {
		t.Fatalf("output tokens = %d", got)
	}
	if got := m.EvaluationDegradedTotal("model_output_invalid"); got != 1 {
		t.Fatalf("evaluation degraded = %d", got)
	}
	if got := m.ReportDegradedTotal("model_unavailable"); got != 1 {
		t.Fatalf("report degraded = %d", got)
	}
	if got := m.LockAttemptTotal(); got != 2 {
		t.Fatalf("lock attempts = %d", got)
	}
	if got := m.LockConflictTotal(); got != 1 {
		t.Fatalf("lock conflicts = %d", got)
	}
	if got := m.ModelCallFailureTotal("openai", "gpt-test", "timeout"); got != 1 {
		t.Fatalf("model failures by class = %d", got)
	}
	if got := m.LeaseRenewAttemptTotal(); got != 2 {
		t.Fatalf("lease renew attempts = %d", got)
	}
	if got := m.LeaseRenewFailureTotal(); got != 1 {
		t.Fatalf("lease renew failures = %d", got)
	}
	if got := m.SSEReconnectTotal(); got != 1 {
		t.Fatalf("sse reconnects = %d", got)
	}
	if got := m.RunFailureTotal("answer", "timeout"); got != 1 {
		t.Fatalf("run failures = %d", got)
	}
	if got := m.EventDeliveryCount(); got != 1 {
		t.Fatalf("delivery observations = %d", got)
	}

	var buf bytes.Buffer
	if err := m.WriteText(&buf); err != nil {
		t.Fatalf("write metrics: %v", err)
	}
	text := buf.String()
	for _, want := range []string{
		"# TYPE ai_interview_model_calls_total counter",
		`ai_interview_model_calls_total{model="gpt-test",provider="openai",result="success"} 2`,
		`ai_interview_model_tokens_total{direction="input",model="gpt-test",provider="openai"} 120`,
		`ai_interview_evaluation_degraded_total{reason="model_output_invalid"} 1`,
		`ai_interview_report_degraded_total{reason="model_unavailable"} 1`,
		`ai_interview_model_call_failures_total{class="timeout",model="gpt-test",provider="openai"} 1`,
		"ai_interview_lock_attempts_total 2",
		"ai_interview_lock_conflicts_total 1",
		"ai_interview_lease_renew_attempts_total 2",
		"ai_interview_lease_renew_failures_total 1",
		"ai_interview_sse_reconnects_total 1",
		"ai_interview_sse_connections 0",
		`ai_interview_run_failures_total{class="timeout",kind="answer"} 1`,
		"# TYPE ai_interview_sse_event_delivery_seconds histogram",
		"ai_interview_sse_event_delivery_seconds_count 1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics output missing %q:\n%s", want, text)
		}
	}
}

func TestSSEConnectionGaugeTracksOpenAndClose(t *testing.T) {
	m := NewMetrics()

	if got := m.SSEConnectionCount(); got != 0 {
		t.Fatalf("initial gauge = %d", got)
	}
	m.SSEConnectionOpened()
	m.SSEConnectionOpened()
	if got := m.SSEConnectionCount(); got != 2 {
		t.Fatalf("after two opens = %d", got)
	}
	m.SSEConnectionClosed()
	if got := m.SSEConnectionCount(); got != 1 {
		t.Fatalf("after one close = %d", got)
	}
	m.SSEConnectionClosed()
	m.SSEConnectionClosed()
	if got := m.SSEConnectionCount(); got != 0 {
		t.Fatalf("gauge must not go negative: %d", got)
	}
}
