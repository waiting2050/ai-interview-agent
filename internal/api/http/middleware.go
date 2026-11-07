package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/waiting2050/ai-interview-agent/internal/platform/idgen"
	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
	"github.com/waiting2050/ai-interview-agent/internal/platform/trace"
)

func withRequestID(next http.Handler, log *slog.Logger, tracer *trace.Tracer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = idgen.New()
		}
		// trace_id is the end-to-end correlation key; request_id identifies this
		// single HTTP call. When the caller sends no trace header we reuse the
		// request id, so the normal case shows one id instead of two unrelated ones.
		traceID := strings.TrimSpace(r.Header.Get("X-Trace-Id"))
		if traceID == "" {
			traceID = requestID
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Trace-Id", traceID)

		ctx := reqctx.WithTrace(reqctx.With(r.Context(), requestID), traceID)
		if sessionID := sessionIDFromPath(r.URL.Path); sessionID != "" {
			ctx = reqctx.WithSession(ctx, sessionID)
		}
		ctx, endSpan := tracer.Start(ctx, "http.request", map[string]any{
			"http.method": r.Method,
			"http.path":   r.URL.Path,
		})
		defer endSpan(nil)
		r = r.WithContext(ctx)

		attrs := append(reqctx.Attrs(ctx), "method", r.Method, "path", r.URL.Path)
		log.With(attrs...).Info("http request")
		next.ServeHTTP(w, r)
	})
}

// sessionIDFromPath extracts the id from /v1/sessions/{id}[/...]. The mux has
// not routed the request yet, so PathValue is not available in middleware.
func sessionIDFromPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "v1" && parts[1] == "sessions" {
		return parts[2]
	}
	return ""
}
