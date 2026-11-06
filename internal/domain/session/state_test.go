package session

import (
	"errors"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

func newSession() *Session {
	return &Session{ID: "sess-1", Status: StatusCreated}
}

func TestStartTransitionsToAwaitingAnswer(t *testing.T) {
	s := newSession()
	if err := s.Start(testNow); err != nil {
		t.Fatalf("start: %v", err)
	}
	if s.Status != StatusActive || s.Phase != PhaseAwaitingAnswer {
		t.Fatalf("got %s/%s", s.Status, s.Phase)
	}
	if s.StartedAt == nil || !s.StartedAt.Equal(testNow) {
		t.Fatalf("started_at not set: %v", s.StartedAt)
	}

	if err := s.Start(testNow); err == nil {
		t.Fatal("second start should fail")
	} else {
		var te *TransitionError
		if !errors.As(err, &te) || te.Command != CommandStart {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestFinishRequiresActiveAndAnswerPhase(t *testing.T) {
	s := newSession()
	if err := s.Finish(testNow); err == nil {
		t.Fatal("finish on created should fail")
	}

	if err := s.Start(testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(testNow); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if s.Phase != PhaseReadyToFinalize {
		t.Fatalf("phase = %s", s.Phase)
	}
}

func TestReportAndComplete(t *testing.T) {
	s := newSession()
	if err := s.BeginReport(); err == nil {
		t.Fatal("report before finish should fail")
	}
	if err := s.Start(testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginReport(); err != nil {
		t.Fatalf("begin report: %v", err)
	}
	if s.Status != StatusReporting {
		t.Fatalf("status = %s", s.Status)
	}
	if err := s.Complete(testNow); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if s.Status != StatusCompleted || s.EndedAt == nil {
		t.Fatalf("status = %s ended_at = %v", s.Status, s.EndedAt)
	}
	if !s.IsTerminal() {
		t.Fatal("completed must be terminal")
	}
}

func TestAbortIsIdempotentButRejectsTerminal(t *testing.T) {
	s := newSession()
	if err := s.Abort(testNow); err != nil {
		t.Fatalf("abort created: %v", err)
	}
	if s.Status != StatusAborted {
		t.Fatalf("status = %s", s.Status)
	}
	if err := s.Abort(testNow); err != nil {
		t.Fatalf("abort twice should be nil: %v", err)
	}

	done := newSession()
	if err := done.Start(testNow); err != nil {
		t.Fatal(err)
	}
	if err := done.Finish(testNow); err != nil {
		t.Fatal(err)
	}
	if err := done.BeginReport(); err != nil {
		t.Fatal(err)
	}
	if err := done.Complete(testNow); err != nil {
		t.Fatal(err)
	}
	if err := done.Abort(testNow); err == nil {
		t.Fatal("abort completed should fail")
	}
}

func TestConfigNormalizeAndValidate(t *testing.T) {
	var cfg Config
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}

	bad := DefaultConfig()
	bad.MaxDepth = 9
	if err := bad.Validate(); err == nil {
		t.Fatal("out-of-range max_depth should fail")
	}

	bad = DefaultConfig()
	bad.Difficulty = "expert"
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown difficulty should fail")
	}
}
