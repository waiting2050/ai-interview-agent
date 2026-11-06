package obs

import "testing"

func TestUsageTrackerAggregatesPerSession(t *testing.T) {
	tracker := NewUsageTracker()

	tracker.Record("s1", "evaluate", 100, 20)
	tracker.Record("s1", "question", 50, 10)
	tracker.Record("s1", "report", 30, 15)
	tracker.Record("s2", "evaluate", 7, 3)
	tracker.Record("", "evaluate", 999, 999) // no session, ignored

	s1 := tracker.Snapshot("s1")
	if s1.ModelCalls != 3 {
		t.Fatalf("model_calls = %d", s1.ModelCalls)
	}
	if s1.PromptTokens != 180 || s1.CompletionTokens != 45 || s1.TotalTokens != 225 {
		t.Fatalf("tokens = %+v", s1)
	}
	if s1.ModelCallBreakdown["evaluate"] != 1 || s1.ModelCallBreakdown["question"] != 1 ||
		s1.ModelCallBreakdown["report"] != 1 {
		t.Fatalf("breakdown = %+v", s1.ModelCallBreakdown)
	}

	s2 := tracker.Snapshot("s2")
	if s2.ModelCalls != 1 || s2.TotalTokens != 10 || s2.ModelCallBreakdown["evaluate"] != 1 {
		t.Fatalf("s2 usage = %+v", s2)
	}

	tracker.Forget("s1")
	if got := tracker.Snapshot("s1"); got.ModelCalls != 0 || got.TotalTokens != 0 {
		t.Fatalf("forgotten usage = %+v", got)
	}
}
