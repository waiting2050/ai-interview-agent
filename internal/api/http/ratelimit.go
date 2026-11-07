package httpapi

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
)

const (
	rateActionSession = "create_session"
	rateActionAnswer  = "submit_answer"
	rateActionReport  = "generate_report"
)

// RateLimitConfig carries the per-user quotas. Demo accounts use the stricter
// Demo* values because their lifecycle is short and abuse is cheaper.
type RateLimitConfig struct {
	Window      time.Duration
	Session     int
	Answer      int
	Report      int
	DemoSession int
	DemoAnswer  int
	DemoReport  int
}

func (c RateLimitConfig) limit(action string, demo bool) int {
	switch action {
	case rateActionSession:
		if demo {
			return c.DemoSession
		}
		return c.Session
	case rateActionAnswer:
		if demo {
			return c.DemoAnswer
		}
		return c.Answer
	case rateActionReport:
		if demo {
			return c.DemoReport
		}
		return c.Report
	default:
		return 0
	}
}

// allowRateLimit enforces one of the three command quotas. A missing limiter
// or a non-positive limit disables the check (unit tests, local debugging).
// When Redis is unavailable the request is allowed and the degradation is
// logged and counted instead of silently swallowed or used to reject the site.
func (h *handlers) allowRateLimit(w http.ResponseWriter, r *http.Request, action string) bool {
	if h.deps.RateLimiter == nil {
		return true
	}
	identity, ok := identityFrom(r.Context())
	if !ok {
		return true
	}
	limit := h.deps.RateLimits.limit(action, identity.IsDemo)
	if limit <= 0 {
		return true
	}
	window := h.deps.RateLimits.Window
	if window <= 0 {
		window = time.Hour
	}

	key := "rl:" + action + ":" + identity.UserID
	result, err := h.deps.RateLimiter.Allow(r.Context(), key, limit, window)
	if err != nil {
		h.deps.Metrics.IncRateLimitDegraded(action)
		h.deps.Logger.Warn("rate limiter unavailable; allowing request",
			"action", action, "user_id", identity.UserID, "error", err)
		return true
	}
	if result.Allowed {
		return true
	}

	h.deps.Metrics.IncRateLimitRejection(action)
	retryAfter := int(math.Ceil(result.ResetAfter.Seconds()))
	if retryAfter < 1 {
		retryAfter = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeJSON(w, http.StatusTooManyRequests, errorBody{
		RequestID: reqctx.From(r.Context()),
		Code:      "rate_limited",
		Message:   "请求过于频繁，请稍后再试",
		Retryable: true,
		Details: map[string]any{
			"action":              action,
			"limit":               limit,
			"window_seconds":      int(window.Seconds()),
			"remaining":           result.Remaining,
			"retry_after_seconds": retryAfter,
		},
	})
	return false
}
