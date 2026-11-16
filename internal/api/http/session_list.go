package httpapi

import (
	"net/http"
	"strconv"
	"time"
)

type latestReportDTO struct {
	TotalScore  float64   `json:"total_score"`
	ScoreBand   string    `json:"score_band"`
	GeneratedAt time.Time `json:"generated_at"`
}

type sessionSummaryDTO struct {
	SessionID     string     `json:"session_id"`
	CandidateName string     `json:"candidate_name"`
	TargetRole    string     `json:"target_role"`
	Domain        string     `json:"domain"`
	Status        string     `json:"status"`
	Phase         string     `json:"phase"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	// LatestReport is omitted when the session has no report. Clients that
	// predate this field keep working, and a missing report never becomes 0.
	LatestReport *latestReportDTO `json:"latest_report,omitempty"`
}

// listSessions backs the history page. The newest report is fetched by the
// same SQL as the page (LEFT JOIN LATERAL), so adding the score does not
// introduce a per-session lookup.
func (h *handlers) listSessions(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		writeUnauthorized(w, r, "缺少访问令牌")
		return
	}

	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeJSON(w, http.StatusBadRequest, errorBody{
				Code:    "invalid_argument",
				Message: "limit 必须是正整数",
			})
			return
		}
		limit = parsed
	}

	var before time.Time
	beforeID := r.URL.Query().Get("before_id")
	if raw := r.URL.Query().Get("before"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody{
				Code:    "invalid_argument",
				Message: "before 必须是 RFC3339 时间",
			})
			return
		}
		if beforeID == "" {
			writeJSON(w, http.StatusBadRequest, errorBody{
				Code:    "invalid_argument",
				Message: "before 与 before_id 必须同时提供",
			})
			return
		}
		before = parsed
	}

	sessions, err := h.deps.Service.ListSessions(r.Context(), identity.UserID, before, beforeID, limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	items := make([]sessionSummaryDTO, 0, len(sessions))
	for _, sess := range sessions {
		item := sessionSummaryDTO{
			SessionID:     sess.ID,
			CandidateName: sess.CandidateName,
			TargetRole:    sess.TargetRole,
			Domain:        sess.Domain,
			Status:        string(sess.Status),
			Phase:         string(sess.Phase),
			StartedAt:     sess.StartedAt,
			EndedAt:       sess.EndedAt,
			CreatedAt:     sess.CreatedAt,
			UpdatedAt:     sess.UpdatedAt,
		}
		if sess.LatestReport != nil {
			item.LatestReport = &latestReportDTO{
				TotalScore:  sess.LatestReport.TotalScore,
				ScoreBand:   sess.LatestReport.ScoreBand,
				GeneratedAt: sess.LatestReport.GeneratedAt,
			}
		}
		items = append(items, item)
	}

	body := map[string]any{"sessions": items}
	if len(sessions) > 0 && len(sessions) == limit {
		last := sessions[len(sessions)-1]
		body["next_before"] = last.CreatedAt.UTC().Format(time.RFC3339Nano)
		body["next_before_id"] = last.ID
	}
	writeJSON(w, http.StatusOK, body)
}
