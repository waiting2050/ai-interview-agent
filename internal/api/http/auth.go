package httpapi

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/auth"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

type identityCtxKey struct{}

// identityFrom returns the verified caller attached by withAuth.
func identityFrom(ctx context.Context) (auth.Identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(auth.Identity)
	return id, ok
}

// withAuth guards the /v1 surface. Health, metrics and the demo static assets
// stay public; only the three account endpoints are exempt from the token
// requirement so a first-time visitor can obtain one.
//
// EventSource cannot set headers, so GET requests to an /events path may pass
// the token as ?access_token= instead of Authorization.
func withAuth(next http.Handler, svc *auth.Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") || isPublicAuthPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		if svc == nil {
			writeAuthUnavailable(w, r)
			return
		}

		token := bearerToken(r)
		if token == "" && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events") {
			token = r.URL.Query().Get("access_token")
		}
		if token == "" {
			writeUnauthorized(w, r, "缺少访问令牌")
			return
		}
		identity, err := svc.Parse(token)
		if err != nil {
			writeUnauthorized(w, r, "访问令牌无效或已过期")
			return
		}
		ctx := context.WithValue(r.Context(), identityCtxKey{}, identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isPublicAuthPath(r *http.Request) bool {
	switch r.URL.Path {
	case "/v1/auth/register", "/v1/auth/login", "/v1/auth/demo":
		return true
	default:
		return false
	}
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return ""
	}
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func writeUnauthorized(w http.ResponseWriter, r *http.Request, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="ai-interview"`)
	writeJSON(w, http.StatusUnauthorized, errorBody{
		RequestID: reqctx.From(r.Context()),
		Code:      "unauthorized",
		Message:   message,
	})
}

func writeAuthUnavailable(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusServiceUnavailable, errorBody{
		RequestID: reqctx.From(r.Context()),
		Code:      "auth_unavailable",
		Message:   "鉴权服务未配置",
		Retryable: true,
	})
}

// requireSessionOwner loads the session and turns a cross-account read into a
// uniform 404, so the API never reveals whether another user's id exists.
func (h *handlers) requireSessionOwner(w http.ResponseWriter, r *http.Request) (*session.Session, bool) {
	return h.requireSessionOwnership(w, r, r.PathValue("id"))
}

// requireSessionOwnership is the same check for handlers that receive the
// session id outside the path (for example GET /v1/review?session_id=).
func (h *handlers) requireSessionOwnership(w http.ResponseWriter, r *http.Request, sessionID string) (*session.Session, bool) {
	sess, err := h.deps.Service.GetSession(r.Context(), sessionID)
	if err != nil {
		h.writeError(w, r, err)
		return nil, false
	}
	identity, ok := identityFrom(r.Context())
	if !ok || sess.OwnerID != identity.UserID {
		h.writeError(w, r, ports.ErrNotFound)
		return nil, false
	}
	return sess, true
}

type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *handlers) register(w http.ResponseWriter, r *http.Request) {
	if h.deps.Auth == nil {
		writeAuthUnavailable(w, r)
		return
	}
	var req credentialsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	token, err := h.deps.Auth.Register(r.Context(), req.Email, req.Password)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, token)
}

func (h *handlers) login(w http.ResponseWriter, r *http.Request) {
	if h.deps.Auth == nil {
		writeAuthUnavailable(w, r)
		return
	}
	var req credentialsRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	token, err := h.deps.Auth.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, token)
}

// demoLogin creates a throwaway account per call. Because every anonymous
// visitor gets a distinct user id, the demo channel cannot read another
// visitor's sessions.
func (h *handlers) demoLogin(w http.ResponseWriter, r *http.Request) {
	if h.deps.Auth == nil {
		writeAuthUnavailable(w, r)
		return
	}
	if h.demoLimiter != nil && !h.demoLimiter.allow(clientIP(r)) {
		writeJSON(w, http.StatusTooManyRequests, errorBody{
			RequestID: reqctx.From(r.Context()),
			Code:      "demo_rate_limited",
			Message:   "匿名体验请求过于频繁，请稍后再试",
			Retryable: true,
		})
		return
	}
	token, err := h.deps.Auth.Demo(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, token)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// demoLimiter is a process-local sliding window. A single instance is enough
// because demo logins are best-effort and per-process limits are acceptable;
// multi-replica deployments would move this to Redis later.
type demoLimiter struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	limit  int
	window time.Duration
	now    func() time.Time
}

func newDemoLimiter(limit int, window time.Duration) *demoLimiter {
	if window <= 0 {
		window = time.Hour
	}
	return &demoLimiter{
		hits:   map[string][]time.Time{},
		limit:  limit,
		window: window,
		now:    func() time.Time { return time.Now() },
	}
}

func (l *demoLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	cutoff := now.Add(-l.window)
	kept := l.hits[ip][:0]
	for _, at := range l.hits[ip] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= l.limit {
		l.hits[ip] = kept
		return false
	}
	l.hits[ip] = append(kept, now)
	return true
}
