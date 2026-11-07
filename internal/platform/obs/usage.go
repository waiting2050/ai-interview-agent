package obs

import "sync"

// Usage is the per-session model usage summary. Field names follow the
// tool_calls / tool_breakdown naming already used by the M4 report.
type Usage struct {
	ModelCalls         int            `json:"model_calls"`
	ModelCallBreakdown map[string]int `json:"model_call_breakdown,omitempty"`
	PromptTokens       int            `json:"prompt_tokens"`
	CompletionTokens   int            `json:"completion_tokens"`
	TotalTokens        int            `json:"total_tokens"`
}

type sessionUsage struct {
	modelCalls       int
	promptTokens     int
	completionTokens int
	breakdown        map[string]int
}

// UsageTracker aggregates model calls and tokens per session in memory. It is
// the source for the token and call-count fields persisted in the final report; a process
// restart loses unfinished sessions, which is acceptable because completed
// reports keep their snapshot.
type UsageTracker struct {
	mu        sync.Mutex
	bySession map[string]*sessionUsage
}

// NewUsageTracker returns an empty tracker.
func NewUsageTracker() *UsageTracker {
	return &UsageTracker{bySession: map[string]*sessionUsage{}}
}

// Record adds one model call to a session. Purpose is one of evaluate,
// question or report.
func (t *UsageTracker) Record(sessionID, purpose string, prompt, completion int) {
	if t == nil || sessionID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	u, ok := t.bySession[sessionID]
	if !ok {
		u = &sessionUsage{breakdown: map[string]int{}}
		t.bySession[sessionID] = u
	}
	u.modelCalls++
	u.promptTokens += max(prompt, 0)
	u.completionTokens += max(completion, 0)
	u.breakdown[nonEmpty(purpose)]++
}

// Snapshot returns a copy for the session. Unknown sessions return zeroes.
func (t *UsageTracker) Snapshot(sessionID string) Usage {
	if t == nil {
		return Usage{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	u, ok := t.bySession[sessionID]
	if !ok {
		return Usage{ModelCallBreakdown: map[string]int{}}
	}
	breakdown := make(map[string]int, len(u.breakdown))
	for k, v := range u.breakdown {
		breakdown[k] = v
	}
	return Usage{
		ModelCalls:         u.modelCalls,
		ModelCallBreakdown: breakdown,
		PromptTokens:       u.promptTokens,
		CompletionTokens:   u.completionTokens,
		TotalTokens:        u.promptTokens + u.completionTokens,
	}
}

// Forget drops a session's counters. Used by tests and by callers that want
// to release memory after a report has been persisted.
func (t *UsageTracker) Forget(sessionID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.bySession, sessionID)
}
