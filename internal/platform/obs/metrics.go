// Package obs provides the in-process metrics registry used by the /metrics
// endpoint. It intentionally implements the small subset of the Prometheus
// text format we need instead of pulling in a metrics library.
package obs

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type modelCallKey struct {
	Provider string
	Model    string
	Result   string
}

type modelFailureKey struct {
	Provider string
	Model    string
	Class    string
}

type tokenKey struct {
	Provider  string
	Model     string
	Direction string
}

type runFailureKey struct {
	Kind  string
	Class string
}

type transitionKey struct {
	Status string
	Phase  string
}

// Metrics is a concurrency-safe registry. The zero value is not usable; call
// NewMetrics.
type Metrics struct {
	mu sync.Mutex

	modelCalls         map[modelCallKey]uint64
	modelCallFailures  map[modelFailureKey]uint64
	tokens             map[tokenKey]uint64
	evaluationDegraded map[string]uint64
	reportDegraded     map[string]uint64
	lockAttempts       uint64
	lockConflicts      uint64
	runFailures        map[runFailureKey]uint64
	deadLetters        map[runFailureKey]uint64
	rateLimitRejected  map[string]uint64
	rateLimitDegraded  map[string]uint64
	budgetRejected     map[string]uint64
	transitions        map[transitionKey]uint64
	staleRunsDetected  uint64
	workerClaims       uint64
	leaseRenewAttempts uint64
	leaseRenewFailures uint64
	sseReconnects      uint64
	delivery           histogram
	modelDurations     map[modelCallKey]*histogram

	sseConnections atomic.Int64
}

// NewMetrics returns an empty registry.
func NewMetrics() *Metrics {
	return &Metrics{
		modelCalls:         map[modelCallKey]uint64{},
		modelCallFailures:  map[modelFailureKey]uint64{},
		tokens:             map[tokenKey]uint64{},
		evaluationDegraded: map[string]uint64{},
		reportDegraded:     map[string]uint64{},
		runFailures:        map[runFailureKey]uint64{},
		deadLetters:        map[runFailureKey]uint64{},
		rateLimitRejected:  map[string]uint64{},
		rateLimitDegraded:  map[string]uint64{},
		budgetRejected:     map[string]uint64{},
		transitions:        map[transitionKey]uint64{},
		delivery:           newHistogram(),
		modelDurations:     map[modelCallKey]*histogram{},
	}
}

// IncModelCall counts one model invocation. Result is success or error.
func (m *Metrics) IncModelCall(provider, model, result string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modelCalls[modelCallKey{Provider: provider, Model: model, Result: result}]++
}

// IncModelCallFailure counts one failed model invocation by error class.
// Together with ai_interview_model_calls_total{result="error"} it yields the
// failure rate per error_class without duplicating the success counter.
func (m *Metrics) IncModelCallFailure(provider, model, class string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modelCallFailures[modelFailureKey{Provider: provider, Model: model, Class: nonEmpty(class)}]++
}

// ObserveModelCall records one model invocation latency. result matches the
// label set of IncModelCall so the histogram and the counter join cleanly.
func (m *Metrics) ObserveModelCall(provider, model, result string, d time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := modelCallKey{Provider: provider, Model: model, Result: nonEmpty(result)}
	h, ok := m.modelDurations[key]
	if !ok {
		h = new(histogram)
		*h = newHistogram()
		m.modelDurations[key] = h
	}
	h.observe(d.Seconds())
}

// IncSessionTransition counts one successful session state write, labelled by
// the resulting status and phase. Per-transition detail stays in
// session_events; this counter is the aggregate signal for dashboards.
func (m *Metrics) IncSessionTransition(status, phase string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.transitions[transitionKey{Status: status, Phase: phase}]++
}

// IncLockAttempt counts one session lock acquisition attempt. The contention
// rate is lock_conflicts_total / lock_attempts_total.
func (m *Metrics) IncLockAttempt() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lockAttempts++
}

// IncSSEReconnect counts one SSE subscription carrying Last-Event-ID, i.e. a
// client that reconnected after a dropped stream.
func (m *Metrics) IncSSEReconnect() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sseReconnects++
}

// IncLeaseRenewAttempt counts one worker lease heartbeat. The renewal failure
// rate is lease_renew_failures_total / lease_renew_attempts_total.
func (m *Metrics) IncLeaseRenewAttempt() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leaseRenewAttempts++
}

// AddModelTokens adds input/output token usage reported by a model call.
func (m *Metrics) AddModelTokens(provider, model string, input, output int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[tokenKey{Provider: provider, Model: model, Direction: "input"}] += uint64(max(input, 0))
	m.tokens[tokenKey{Provider: provider, Model: model, Direction: "output"}] += uint64(max(output, 0))
}

// IncEvaluationDegraded counts one degraded answer evaluation.
func (m *Metrics) IncEvaluationDegraded(reason string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evaluationDegraded[nonEmpty(reason)]++
}

// IncReportDegraded counts one degraded report generation.
func (m *Metrics) IncReportDegraded(reason string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reportDegraded[nonEmpty(reason)]++
}

// IncLockConflict counts one failed session lock acquisition.
func (m *Metrics) IncLockConflict() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lockConflicts++
}

// SSEConnectionOpened increments the live SSE connection gauge.
func (m *Metrics) SSEConnectionOpened() {
	if m == nil {
		return
	}
	m.sseConnections.Add(1)
}

// SSEConnectionClosed decrements the live SSE connection gauge.
func (m *Metrics) SSEConnectionClosed() {
	if m == nil {
		return
	}
	if v := m.sseConnections.Add(-1); v < 0 {
		m.sseConnections.Store(0)
	}
}

// ObserveEventDelivery records the time from listing events to writing one to
// the SSE client. Backlog replay is included; it measures delivery, not event
// age.
func (m *Metrics) ObserveEventDelivery(d time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delivery.observe(d.Seconds())
}

// IncRunFailure counts one failed run, labelled by run kind and error class.
func (m *Metrics) IncRunFailure(kind, class string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runFailures[runFailureKey{Kind: nonEmpty(kind), Class: nonEmpty(class)}]++
}

// IncDeadLetter counts one run that crossed the consecutive-failure threshold.
func (m *Metrics) IncDeadLetter(kind, class string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deadLetters[runFailureKey{Kind: nonEmpty(kind), Class: nonEmpty(class)}]++
}

// IncWorkerClaim counts one run claimed by a worker.
func (m *Metrics) IncWorkerClaim() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workerClaims++
}

// IncLeaseRenewFailure counts one failed lease heartbeat (lost ownership or
// storage error).
func (m *Metrics) IncLeaseRenewFailure() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.leaseRenewFailures++
}

// AddStaleRunsDetected counts runs the crash-recovery scan found stale. The
// alert rule uses this counter; it is cumulative so restarts do not lose the
// signal (Observe seeds on the first sample, see package alert).
func (m *Metrics) AddStaleRunsDetected(count int) {
	if m == nil || count <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.staleRunsDetected += uint64(count)
}

// DeadLetterCount sums every kind/class of dead-lettered runs.
func (m *Metrics) DeadLetterCount() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var total uint64
	for _, v := range m.deadLetters {
		total += v
	}
	return total
}

// BudgetRejectionCount sums user- and session-scope rejections.
func (m *Metrics) BudgetRejectionCount() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var total uint64
	for _, v := range m.budgetRejected {
		total += v
	}
	return total
}

// StaleRunDetectedTotal is an accessor used by tests.
func (m *Metrics) StaleRunDetectedTotal() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.staleRunsDetected
}

// IncBudgetRejection counts one run rejected before it could spend tokens.
// scope is "user" or "session"; user is checked first, so a request that
// exceeds both is only counted as a user rejection.
func (m *Metrics) IncBudgetRejection(scope string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.budgetRejected[nonEmpty(scope)]++
}

// IncRateLimitRejection counts one request rejected by the user rate limiter.
func (m *Metrics) IncRateLimitRejection(action string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rateLimitRejected[nonEmpty(action)]++
}

// IncRateLimitDegraded counts one request allowed because the rate-limit
// backend was unavailable (explicit fail-open).
func (m *Metrics) IncRateLimitDegraded(action string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rateLimitDegraded[nonEmpty(action)]++
}

// ModelCallTotal is an accessor used by tests and ad-hoc checks.
func (m *Metrics) ModelCallTotal(provider, model, result string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.modelCalls[modelCallKey{Provider: provider, Model: model, Result: result}]
}

// ModelCallFailureTotal is an accessor used by tests.
func (m *Metrics) ModelCallFailureTotal(provider, model, class string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.modelCallFailures[modelFailureKey{Provider: provider, Model: model, Class: nonEmpty(class)}]
}

// TokenTotal is an accessor used by tests and ad-hoc checks.
func (m *Metrics) TokenTotal(provider, model, direction string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tokens[tokenKey{Provider: provider, Model: model, Direction: direction}]
}

// EvaluationDegradedTotal is an accessor used by tests.
func (m *Metrics) EvaluationDegradedTotal(reason string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.evaluationDegraded[nonEmpty(reason)]
}

// ReportDegradedTotal is an accessor used by tests.
func (m *Metrics) ReportDegradedTotal(reason string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reportDegraded[nonEmpty(reason)]
}

// LockAttemptTotal is an accessor used by tests.
func (m *Metrics) LockAttemptTotal() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lockAttempts
}

// LockConflictTotal is an accessor used by tests.
func (m *Metrics) LockConflictTotal() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lockConflicts
}

// SSEReconnectTotal is an accessor used by tests.
func (m *Metrics) SSEReconnectTotal() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sseReconnects
}

// SSEConnectionCount returns the live SSE connection gauge.
func (m *Metrics) SSEConnectionCount() int64 {
	if m == nil {
		return 0
	}
	return m.sseConnections.Load()
}

// EventDeliveryCount returns how many deliveries were observed.
func (m *Metrics) EventDeliveryCount() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.delivery.count
}

// RunFailureTotal is an accessor used by tests.
func (m *Metrics) RunFailureTotal(kind, class string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runFailures[runFailureKey{Kind: nonEmpty(kind), Class: nonEmpty(class)}]
}

// DeadLetterTotal is an accessor used by tests.
func (m *Metrics) DeadLetterTotal(kind, class string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deadLetters[runFailureKey{Kind: nonEmpty(kind), Class: nonEmpty(class)}]
}

// WorkerClaimTotal is an accessor used by tests.
func (m *Metrics) WorkerClaimTotal() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workerClaims
}

// LeaseRenewAttemptTotal is an accessor used by tests.
func (m *Metrics) LeaseRenewAttemptTotal() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.leaseRenewAttempts
}

// LeaseRenewFailureTotal is an accessor used by tests.
func (m *Metrics) LeaseRenewFailureTotal() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.leaseRenewFailures
}

// RateLimitRejectionTotal is an accessor used by tests.
func (m *Metrics) RateLimitRejectionTotal(action string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rateLimitRejected[nonEmpty(action)]
}

// BudgetRejectionTotal is an accessor used by tests.
func (m *Metrics) BudgetRejectionTotal(scope string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.budgetRejected[nonEmpty(scope)]
}

// ModelCallDurationCount is an accessor used by tests.
func (m *Metrics) ModelCallDurationCount(provider, model, result string) uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if h, ok := m.modelDurations[modelCallKey{Provider: provider, Model: model, Result: result}]; ok {
		return h.count
	}
	return 0
}

// SessionTransitionTotal is an accessor used by tests.
func (m *Metrics) SessionTransitionTotal(status, phase string) uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.transitions[transitionKey{Status: status, Phase: phase}]
}

// RateLimitDegradedTotal is an accessor used by tests.
func (m *Metrics) RateLimitDegradedTotal(action string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rateLimitDegraded[nonEmpty(action)]
}

// WriteText renders the registry in Prometheus text exposition format.
func (m *Metrics) WriteText(w io.Writer) error {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	writeFamily := func(name, help, typ string, lines []string) error {
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ); err != nil {
			return err
		}
		sort.Strings(lines)
		for _, line := range lines {
			if _, err := io.WriteString(w, line+"\n"); err != nil {
				return err
			}
		}
		return nil
	}

	var modelLines []string
	for k, v := range m.modelCalls {
		modelLines = append(modelLines, fmt.Sprintf(
			`ai_interview_model_calls_total{model="%s",provider="%s",result="%s"} %d`,
			escape(k.Model), escape(k.Provider), escape(k.Result), v))
	}
	if err := writeFamily("ai_interview_model_calls_total",
		"Model invocations by provider, model and result.", "counter", modelLines); err != nil {
		return err
	}

	var modelFailureLines []string
	for k, v := range m.modelCallFailures {
		modelFailureLines = append(modelFailureLines, fmt.Sprintf(
			`ai_interview_model_call_failures_total{class="%s",model="%s",provider="%s"} %d`,
			escape(k.Class), escape(k.Model), escape(k.Provider), v))
	}
	if err := writeFamily("ai_interview_model_call_failures_total",
		"Failed model invocations by error class.", "counter", modelFailureLines); err != nil {
		return err
	}

	var tokenLines []string
	for k, v := range m.tokens {
		tokenLines = append(tokenLines, fmt.Sprintf(
			`ai_interview_model_tokens_total{direction="%s",model="%s",provider="%s"} %d`,
			escape(k.Direction), escape(k.Model), escape(k.Provider), v))
	}
	if err := writeFamily("ai_interview_model_tokens_total",
		"Model token usage by direction.", "counter", tokenLines); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(w, "# HELP ai_interview_model_call_seconds Model invocation latency by provider, model and result.\n# TYPE ai_interview_model_call_seconds histogram\n"); err != nil {
		return err
	}
	durationKeys := make([]modelCallKey, 0, len(m.modelDurations))
	for k := range m.modelDurations {
		durationKeys = append(durationKeys, k)
	}
	sort.Slice(durationKeys, func(i, j int) bool {
		if durationKeys[i].Provider != durationKeys[j].Provider {
			return durationKeys[i].Provider < durationKeys[j].Provider
		}
		if durationKeys[i].Model != durationKeys[j].Model {
			return durationKeys[i].Model < durationKeys[j].Model
		}
		return durationKeys[i].Result < durationKeys[j].Result
	})
	for _, k := range durationKeys {
		labels := fmt.Sprintf(`model="%s",provider="%s",result="%s"`,
			escape(k.Model), escape(k.Provider), escape(k.Result))
		if err := writeHistogramSeries(w, "ai_interview_model_call_seconds", labels, m.modelDurations[k]); err != nil {
			return err
		}
	}

	var evalLines []string
	for reason, v := range m.evaluationDegraded {
		evalLines = append(evalLines, fmt.Sprintf(
			`ai_interview_evaluation_degraded_total{reason="%s"} %d`, escape(reason), v))
	}
	if err := writeFamily("ai_interview_evaluation_degraded_total",
		"Answer evaluations that fell back to the degraded record.", "counter", evalLines); err != nil {
		return err
	}

	var reportLines []string
	for reason, v := range m.reportDegraded {
		reportLines = append(reportLines, fmt.Sprintf(
			`ai_interview_report_degraded_total{reason="%s"} %d`, escape(reason), v))
	}
	if err := writeFamily("ai_interview_report_degraded_total",
		"Reports that fell back to the deterministic template.", "counter", reportLines); err != nil {
		return err
	}

	if err := writeFamily("ai_interview_lock_attempts_total",
		"Session lock acquisition attempts.", "counter",
		[]string{fmt.Sprintf("ai_interview_lock_attempts_total %d", m.lockAttempts)}); err != nil {
		return err
	}

	if err := writeFamily("ai_interview_lock_conflicts_total",
		"Session lock acquisitions that returned busy.", "counter",
		[]string{fmt.Sprintf("ai_interview_lock_conflicts_total %d", m.lockConflicts)}); err != nil {
		return err
	}

	if err := writeFamily("ai_interview_sse_connections",
		"Live SSE connections.", "gauge",
		[]string{fmt.Sprintf("ai_interview_sse_connections %d", m.sseConnections.Load())}); err != nil {
		return err
	}

	if err := writeFamily("ai_interview_sse_reconnects_total",
		"SSE subscriptions that resumed with Last-Event-ID.", "counter",
		[]string{fmt.Sprintf("ai_interview_sse_reconnects_total %d", m.sseReconnects)}); err != nil {
		return err
	}

	var failureLines []string
	for k, v := range m.runFailures {
		failureLines = append(failureLines, fmt.Sprintf(
			`ai_interview_run_failures_total{class="%s",kind="%s"} %d`,
			escape(k.Class), escape(k.Kind), v))
	}
	if err := writeFamily("ai_interview_run_failures_total",
		"Failed runs by kind and error class.", "counter", failureLines); err != nil {
		return err
	}

	var deadLines []string
	for k, v := range m.deadLetters {
		deadLines = append(deadLines, fmt.Sprintf(
			`ai_interview_dead_letters_total{class="%s",kind="%s"} %d`,
			escape(k.Class), escape(k.Kind), v))
	}
	if err := writeFamily("ai_interview_dead_letters_total",
		"Runs that crossed the consecutive-failure threshold.", "counter", deadLines); err != nil {
		return err
	}

	if err := writeFamily("ai_interview_worker_claims_total",
		"Runs claimed by this instance.", "counter",
		[]string{fmt.Sprintf("ai_interview_worker_claims_total %d", m.workerClaims)}); err != nil {
		return err
	}

	if err := writeFamily("ai_interview_lease_renew_attempts_total",
		"Worker lease heartbeat attempts.", "counter",
		[]string{fmt.Sprintf("ai_interview_lease_renew_attempts_total %d", m.leaseRenewAttempts)}); err != nil {
		return err
	}

	if err := writeFamily("ai_interview_lease_renew_failures_total",
		"Worker lease heartbeat failures.", "counter",
		[]string{fmt.Sprintf("ai_interview_lease_renew_failures_total %d", m.leaseRenewFailures)}); err != nil {
		return err
	}

	var rejectionLines []string
	for action, v := range m.rateLimitRejected {
		rejectionLines = append(rejectionLines, fmt.Sprintf(
			`ai_interview_rate_limit_rejections_total{action="%s"} %d`, escape(action), v))
	}
	if err := writeFamily("ai_interview_rate_limit_rejections_total",
		"Requests rejected by the user rate limiter.", "counter", rejectionLines); err != nil {
		return err
	}

	var degradedLines []string
	for action, v := range m.rateLimitDegraded {
		degradedLines = append(degradedLines, fmt.Sprintf(
			`ai_interview_rate_limit_degraded_total{action="%s"} %d`, escape(action), v))
	}
	if err := writeFamily("ai_interview_rate_limit_degraded_total",
		"Requests allowed because the rate-limit backend was unavailable.", "counter", degradedLines); err != nil {
		return err
	}

	var budgetLines []string
	for scope, v := range m.budgetRejected {
		budgetLines = append(budgetLines, fmt.Sprintf(
			`ai_interview_model_budget_rejections_total{scope="%s"} %d`, escape(scope), v))
	}
	if err := writeFamily("ai_interview_model_budget_rejections_total",
		"Runs rejected before spending tokens, by budget scope.", "counter", budgetLines); err != nil {
		return err
	}

	var transitionLines []string
	for k, v := range m.transitions {
		transitionLines = append(transitionLines, fmt.Sprintf(
			`ai_interview_session_transitions_total{phase="%s",status="%s"} %d`,
			escape(k.Phase), escape(k.Status), v))
	}
	if err := writeFamily("ai_interview_session_transitions_total",
		"Successful session state writes by resulting status and phase.", "counter", transitionLines); err != nil {
		return err
	}

	if err := writeFamily("ai_interview_stale_runs_detected_total",
		"Runs found stale by the crash-recovery scan since process start.",
		"counter", []string{fmt.Sprintf("ai_interview_stale_runs_detected_total %d", m.staleRunsDetected)}); err != nil {
		return err
	}

	return writeHistogram(w, "ai_interview_sse_event_delivery_seconds",
		"Time from listing events to writing them to the SSE client.", &m.delivery)
}

func writeHistogram(w io.Writer, name, help string, h *histogram) error {
	if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", name, help, name); err != nil {
		return err
	}
	return writeHistogramSeries(w, name, "", h)
}

// writeHistogramSeries renders one series. labels is an already escaped
// `k="v",k2="v2"` fragment; an empty string means an unlabelled series.
func writeHistogramSeries(w io.Writer, name, labels string, h *histogram) error {
	var cumulative uint64
	for i, bound := range h.buckets {
		cumulative += h.counts[i]
		if err := writeHistogramBucket(w, name, labels, bound.label, cumulative); err != nil {
			return err
		}
	}
	if err := writeHistogramBucket(w, name, labels, "+Inf", h.count); err != nil {
		return err
	}
	sum := strconv.FormatFloat(h.sum, 'f', -1, 64)
	if labels == "" {
		if _, err := fmt.Fprintf(w, "%s_sum %s\n", name, sum); err != nil {
			return err
		}
		_, err := fmt.Fprintf(w, "%s_count %d\n", name, h.count)
		return err
	}
	if _, err := fmt.Fprintf(w, "%s_sum{%s} %s\n", name, labels, sum); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "%s_count{%s} %d\n", name, labels, h.count)
	return err
}

func writeHistogramBucket(w io.Writer, name, labels, le string, value uint64) error {
	fragment := fmt.Sprintf(`le="%s"`, le)
	if labels != "" {
		fragment = fragment + "," + labels
	}
	_, err := fmt.Fprintf(w, "%s_bucket{%s} %d\n", name, fragment, value)
	return err
}

type histogram struct {
	buckets []bucket
	counts  []uint64
	sum     float64
	count   uint64
}

type bucket struct {
	upper float64
	label string
}

func newHistogram() histogram {
	return histogram{
		buckets: []bucket{
			{upper: 0.001, label: "0.001"},
			{upper: 0.005, label: "0.005"},
			{upper: 0.01, label: "0.01"},
			{upper: 0.05, label: "0.05"},
			{upper: 0.1, label: "0.1"},
			{upper: 0.5, label: "0.5"},
			{upper: 1, label: "1"},
			{upper: 5, label: "5"},
			{upper: 10, label: "10"},
		},
		counts: make([]uint64, 9),
	}
}

func (h *histogram) observe(v float64) {
	for i, b := range h.buckets {
		if v <= b.upper {
			h.counts[i]++
			break
		}
	}
	h.sum += v
	h.count++
}

func escape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return strings.ReplaceAll(v, "\n", `\n`)
}

func nonEmpty(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
