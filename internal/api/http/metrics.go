package httpapi

import "net/http"

// metrics renders the Prometheus text exposition format. It deliberately
// avoids a metrics dependency: the registry writes the small subset we need.
func (h *handlers) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if err := h.deps.Metrics.WriteText(w); err != nil {
		h.deps.Logger.Error("write metrics", "error", err)
	}
}
