package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/domain/jd"
)

// analyzeJD parses a public URL or pasted text, matches it against the resume
// and plans an outline. No model is called on this path.
func (h *handlers) analyzeJD(w http.ResponseWriter, r *http.Request) {
	if h.deps.JDAnalyzer == nil {
		h.writeJDUnavailable(w)
		return
	}
	var req jd.AnalyzeInput
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, fmt.Errorf("%w: %s", jd.ErrInvalidArgument, err))
		return
	}
	result, err := h.deps.JDAnalyzer.Analyze(r.Context(), req)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// getJD returns the cached listing, including the original fetched_at and
// source URL used when it was parsed.
func (h *handlers) getJD(w http.ResponseWriter, r *http.Request) {
	if h.deps.JDAnalyzer == nil {
		h.writeJDUnavailable(w)
		return
	}
	listing, err := h.deps.JDAnalyzer.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

// generateApplyKit builds the M16 package from a cached listing. It returns
// text only: there is no auto-apply, form filling or account storage path.
func (h *handlers) generateApplyKit(w http.ResponseWriter, r *http.Request) {
	if h.deps.JDAnalyzer == nil {
		h.writeJDUnavailable(w)
		return
	}
	var req jd.KitInput
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, fmt.Errorf("%w: %s", jd.ErrInvalidArgument, err))
		return
	}
	listing, err := h.deps.JDAnalyzer.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	kit, err := jd.BuildKit(listing, req, time.Now().UTC())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, kit)
}

func (h *handlers) writeJDUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, errorBody{
		Code:      "jd_unavailable",
		Message:   "JD 分析服务未启用",
		Retryable: true,
	})
}
