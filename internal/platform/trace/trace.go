// Package trace provides a tiny, dependency-free OTLP/HTTP span exporter.
//
// Logs are the primary trace surface: every log line already carries trace_id
// through reqctx. OTLP export is strictly optional and only switched on when an
// endpoint is configured, so local runs and tests keep zero network and zero
// dependency overhead. The exporter speaks the OTLP/HTTP JSON protocol directly
// with net/http + encoding/json; no OpenTelemetry SDK or collector is required.
package trace

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
)

// Span is the subset of an OTLP span this service produces.
type Span struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	Start        time.Time
	End          time.Time
	Attrs        map[string]any
	Error        string
}

// Tracer starts and ends spans. A nil or endpoint-less Tracer is a no-op.
type Tracer struct {
	endpoint string
	service  string
	client   *http.Client
	log      *slog.Logger
	wg       sync.WaitGroup
}

// New builds a tracer. An empty endpoint disables export entirely (no goroutine,
// no HTTP client use). The endpoint may be a base URL such as
// http://localhost:4318 or already include /v1/traces.
func New(endpoint, service string, log *slog.Logger) *Tracer {
	if log == nil {
		log = slog.Default()
	}
	if strings.TrimSpace(service) == "" {
		service = "ai-interview-agent"
	}
	t := &Tracer{
		service: service,
		client:  &http.Client{Timeout: 3 * time.Second},
		log:     log,
	}
	if e := strings.TrimSpace(endpoint); e != "" {
		t.endpoint = strings.TrimRight(e, "/")
		if !strings.HasSuffix(t.endpoint, "/v1/traces") {
			t.endpoint += "/v1/traces"
		}
	}
	return t
}

// Enabled reports whether spans are exported.
func (t *Tracer) Enabled() bool {
	return t != nil && t.endpoint != ""
}

// Start begins a span and returns a context carrying its id plus an end
// function. The span inherits trace_id from ctx; when absent (e.g. startup
// reconciliation) a fresh id is minted so the span still correlates.
func (t *Tracer) Start(ctx context.Context, name string, attrs map[string]any) (context.Context, func(error)) {
	if !t.Enabled() {
		return ctx, func(error) {}
	}
	traceID := reqctx.TraceID(ctx)
	if traceID == "" {
		traceID = newID(16)
	}
	spanID := newID(8)
	parentID := reqctx.SpanID(ctx)
	start := time.Now()

	ctx = reqctx.WithSpan(ctx, spanID)
	return ctx, func(err error) {
		span := Span{
			TraceID:      traceID,
			SpanID:       spanID,
			ParentSpanID: parentID,
			Name:         name,
			Start:        start,
			End:          time.Now(),
			Attrs:        attrs,
		}
		if err != nil {
			span.Error = err.Error()
		}
		t.export(span)
	}
}

// Close waits briefly for in-flight exports.
func (t *Tracer) Close() {
	if t == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

func (t *Tracer) export(span Span) {
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		body, err := json.Marshal(otlpRequest{
			ResourceSpans: []otlpResourceSpans{{
				Resource: otlpResource{Attributes: []otlpKeyValue{
					stringKV("service.name", t.service),
				}},
				ScopeSpans: []otlpScopeSpans{{
					Scope: otlpScope{Name: t.service},
					Spans: []otlpSpan{{
						TraceID:           otlpTraceID(span.TraceID),
						SpanID:            span.SpanID,
						ParentSpanID:      span.ParentSpanID,
						Name:              span.Name,
						Kind:              1,
						StartTimeUnixNano: span.Start.UnixNano(),
						EndTimeUnixNano:   span.End.UnixNano(),
						Attributes:        spanAttributes(span.Attrs),
						Status:            otlpStatus{Code: spanStatus(span.Error), Message: span.Error},
					}},
				}},
			}},
		})
		if err != nil {
			t.log.Warn("encode otlp span", "error", err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
		if err != nil {
			t.log.Warn("create otlp request", "error", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := t.client.Do(req)
		if err != nil {
			t.log.Debug("otlp export failed", "error", err)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.log.Debug("otlp export rejected", "status", resp.StatusCode)
		}
	}()
}

// otlpTraceID converts any trace id (ULID or caller supplied) into the 32 hex
// characters OTLP requires. The mapping is deterministic, so logs and exported
// spans can still be cross-referenced.
func otlpTraceID(traceID string) string {
	sum := sha256.Sum256([]byte(traceID))
	return hex.EncodeToString(sum[:16])
}

func newID(bytesLen int) string {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", bytesLen*2)
	}
	return hex.EncodeToString(buf)
}

func spanStatus(errText string) int {
	if errText != "" {
		return 2 // ERROR
	}
	return 1 // OK
}

func spanAttributes(attrs map[string]any) []otlpKeyValue {
	out := make([]otlpKeyValue, 0, len(attrs))
	for k, v := range attrs {
		out = append(out, otlpKeyValue{Key: k, Value: otlpValue(v)})
	}
	return out
}

func stringKV(key, value string) otlpKeyValue {
	return otlpKeyValue{Key: key, Value: otlpValue(value)}
}

func otlpValue(v any) map[string]any {
	switch value := v.(type) {
	case string:
		return map[string]any{"stringValue": value}
	case bool:
		return map[string]any{"boolValue": value}
	case int:
		return map[string]any{"intValue": value}
	case int64:
		return map[string]any{"intValue": value}
	case float64:
		return map[string]any{"doubleValue": value}
	case time.Duration:
		return map[string]any{"intValue": value.Milliseconds()}
	default:
		return map[string]any{"stringValue": fmt.Sprint(value)}
	}
}

type otlpRequest struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes,omitempty"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type otlpSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	ParentSpanID      string         `json:"parentSpanId,omitempty"`
	Name              string         `json:"name"`
	Kind              int            `json:"kind"`
	StartTimeUnixNano int64          `json:"startTimeUnixNano"`
	EndTimeUnixNano   int64          `json:"endTimeUnixNano"`
	Attributes        []otlpKeyValue `json:"attributes,omitempty"`
	Status            otlpStatus     `json:"status"`
}

type otlpStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type otlpKeyValue struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}
