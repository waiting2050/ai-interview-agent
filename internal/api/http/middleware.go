package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/waiting2050/ai-interview-agent/internal/platform/idgen"
	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
)

func withRequestID(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = idgen.New()
		}
		w.Header().Set("X-Request-ID", requestID)

		ctx := reqctx.With(r.Context(), requestID)
		if sessionID := sessionIDFromPath(r.URL.Path); sessionID != "" {
			ctx = reqctx.WithSession(ctx, sessionID)
		}
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
