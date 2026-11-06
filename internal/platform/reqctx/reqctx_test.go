package reqctx

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// TestAttrsAreEmittedAsStructuredLogFields is the minimal log-field assertion:
// one log line must carry request, session, run and turn ids together.
func TestAttrsAreEmittedAsStructuredLogFields(t *testing.T) {
	ctx := With(context.Background(), "req-1")
	ctx = WithSession(ctx, "sess-1")
	ctx = WithRun(ctx, "run-1")
	ctx = WithTurn(ctx, "turn-1")

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	log.Info("probe", Attrs(ctx)...)

	line := buf.String()
	for _, want := range []string{
		"request_id=req-1",
		"session_id=sess-1",
		"run_id=run-1",
		"turn_id=turn-1",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line missing %q: %s", want, line)
		}
	}
}

func TestAttrsSkipEmptyIdentifiers(t *testing.T) {
	if attrs := Attrs(context.Background()); len(attrs) != 0 {
		t.Fatalf("empty ctx attrs = %v", attrs)
	}
}
