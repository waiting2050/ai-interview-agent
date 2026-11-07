package trace

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDisabledTracerIsNoOp(t *testing.T) {
	tracer := New("", "ai-interview-agent", testLogger())
	if tracer.Enabled() {
		t.Fatal("empty endpoint must disable export")
	}
	ctx, end := tracer.Start(context.Background(), "run.answer", nil)
	end(nil)
	tracer.Close()
	if id := reqctx.SpanID(ctx); id != "" {
		t.Fatalf("span id assigned while disabled: %q", id)
	}
}

func TestOTLPExportCarriesTraceID(t *testing.T) {
	var (
		mu      sync.Mutex
		paths   []string
		bodies  []string
		headers []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, string(body))
		headers = append(headers, r.Header.Get("Content-Type"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tracer := New(srv.URL, "ai-interview-agent", testLogger())
	if !tracer.Enabled() {
		t.Fatal("endpoint did not enable export")
	}

	ctx := reqctx.WithTrace(context.Background(), "trace-abc")
	_, end := tracer.Start(ctx, "run.answer", map[string]any{"run.kind": "answer"})
	end(nil)
	tracer.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("exported spans = %d, want 1", len(bodies))
	}
	if paths[0] != "/v1/traces" {
		t.Fatalf("path = %q", paths[0])
	}
	if headers[0] != "application/json" {
		t.Fatalf("content-type = %q", headers[0])
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &decoded); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	resourceSpans, ok := decoded["resourceSpans"].([]any)
	if !ok || len(resourceSpans) != 1 {
		t.Fatalf("resourceSpans = %#v", decoded["resourceSpans"])
	}
	rs := resourceSpans[0].(map[string]any)
	scopeSpans := rs["scopeSpans"].([]any)[0].(map[string]any)
	spans := scopeSpans["spans"].([]any)
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	span := spans[0].(map[string]any)
	if span["name"] != "run.answer" {
		t.Fatalf("span name = %v", span["name"])
	}
	traceID, _ := span["traceId"].(string)
	if traceID != otlpTraceID("trace-abc") {
		t.Fatalf("traceId = %q, want %q", traceID, otlpTraceID("trace-abc"))
	}
	if len(traceID) != 32 {
		t.Fatalf("traceId must be 32 hex chars, got %d", len(traceID))
	}
	if !strings.Contains(bodies[0], "run.kind") {
		t.Fatalf("attributes missing: %s", bodies[0])
	}
}
