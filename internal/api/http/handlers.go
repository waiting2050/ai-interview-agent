package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/domain/domainpack"
	domainreport "github.com/waiting2050/ai-interview-agent/internal/domain/report"
	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
	"github.com/waiting2050/ai-interview-agent/internal/transport/sse"
)

const maxRequestBody = 1 << 20 // 1 MiB

type handlers struct {
	deps Deps
}

func (h *handlers) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	body := map[string]any{
		"status":  "ok",
		"service": "ai-interview-agent",
		"storage": "postgres",
	}
	if h.deps.Health != nil {
		if err := h.deps.Health(ctx); err != nil {
			body["status"] = "degraded"
			body["database"] = "unavailable"
			writeJSON(w, http.StatusServiceUnavailable, body)
			return
		}
		body["database"] = "ok"
	}
	writeJSON(w, http.StatusOK, body)
}

type createSessionRequest struct {
	CandidateName string          `json:"candidate_name"`
	TargetRole    string          `json:"target_role"`
	ResumeText    string          `json:"resume_text"`
	ResumeProfile *resume.Profile `json:"resume_profile"`
	JDText        string          `json:"jd_text"`
	Domain        string          `json:"domain"`
	Config        *session.Config `json:"config"`
}

type sessionDTO struct {
	SessionID     string            `json:"session_id"`
	CandidateName string            `json:"candidate_name"`
	TargetRole    string            `json:"target_role"`
	ResumeProfile resume.Profile    `json:"resume_profile"`
	Domain        string            `json:"domain"`
	Rubric        domainpack.Rubric `json:"rubric"`
	Config        session.Config    `json:"config"`
	Status        string            `json:"status"`
	Phase         string            `json:"phase"`
	StateVersion  uint64            `json:"state_version"`
	PendingTurnID string            `json:"pending_turn_id,omitempty"`
	PlanCursor    int               `json:"plan_cursor"`
	FollowupsUsed int               `json:"followups_used"`
	StartedAt     *time.Time        `json:"started_at,omitempty"`
	EndedAt       *time.Time        `json:"ended_at,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

func toSessionDTO(s *session.Session) sessionDTO {
	dto := sessionDTO{
		SessionID:     s.ID,
		CandidateName: s.CandidateName,
		TargetRole:    s.TargetRole,
		ResumeProfile: s.ResumeProfile,
		Domain:        s.Domain,
		Config:        s.Config,
		Status:        string(s.Status),
		Phase:         string(s.Phase),
		StateVersion:  s.StateVersion,
		PendingTurnID: s.PendingTurnID,
		PlanCursor:    s.PlanCursor,
		FollowupsUsed: s.FollowupsUsed,
		StartedAt:     s.StartedAt,
		EndedAt:       s.EndedAt,
		CreatedAt:     s.CreatedAt,
		UpdatedAt:     s.UpdatedAt,
	}
	if pack := domainpack.Default().MustGet(s.Domain); pack != nil {
		dto.Rubric = pack.Rubric
	}
	return dto
}

type domainDTO struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Version    string                 `json:"version"`
	Directions []domainpack.Direction `json:"directions"`
	Rubric     domainpack.Rubric      `json:"rubric"`
	TopicCount int                    `json:"topic_count"`
}

// listDomains backs the demo domain selector so the client never hardcodes
// pack ids or rubric labels.
func (h *handlers) listDomains(w http.ResponseWriter, r *http.Request) {
	packs := domainpack.Default().List()
	domains := make([]domainDTO, 0, len(packs))
	for _, pack := range packs {
		domains = append(domains, domainDTO{
			ID:         pack.ID,
			Name:       pack.Name,
			Version:    pack.Version,
			Directions: pack.Directions,
			Rubric:     pack.Rubric,
			TopicCount: len(pack.Topics),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": domains})
}

func (h *handlers) createSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}

	cfg := session.DefaultConfig()
	if req.Config != nil {
		cfg = *req.Config
	}
	var profile resume.Profile
	if req.ResumeProfile != nil {
		profile = *req.ResumeProfile
	}

	sess, err := h.deps.Service.CreateSession(r.Context(), app.CreateSessionInput{
		CandidateName: req.CandidateName,
		TargetRole:    req.TargetRole,
		ResumeText:    req.ResumeText,
		ResumeProfile: profile,
		JDText:        req.JDText,
		Domain:        req.Domain,
		Config:        cfg,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSessionDTO(sess))
}

func (h *handlers) getSession(w http.ResponseWriter, r *http.Request) {
	sess, err := h.deps.Service.GetSession(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSessionDTO(sess))
}

func (h *handlers) startSession(w http.ResponseWriter, r *http.Request) {
	body, err := readOptionalBody(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{
			RequestID: "",
			Code:      "invalid_argument",
			Message:   err.Error(),
		})
		return
	}

	result, err := h.deps.Service.Start(r.Context(), app.StartCommand{
		SessionID:      r.PathValue("id"),
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		RequestBody:    body,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeAccepted(w, result)
}

type submitAnswerRequest struct {
	TurnID string `json:"turn_id"`
	Answer string `json:"answer"`
}

func (h *handlers) submitAnswer(w http.ResponseWriter, r *http.Request) {
	var req submitAnswerRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}

	result, err := h.deps.Service.Answer(r.Context(), app.AnswerCommand{
		SessionID:      r.PathValue("id"),
		TurnID:         req.TurnID,
		Answer:         req.Answer,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeAccepted(w, result)
}

func writeAccepted(w http.ResponseWriter, result app.StartResult) {
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"run_id":    result.RunID,
		"status":    "accepted",
		"run_state": string(result.Status),
	})
}

func (h *handlers) finishSession(w http.ResponseWriter, r *http.Request) {
	result, err := h.deps.Service.Finish(r.Context(), app.FinishCommand{
		SessionID:      r.PathValue("id"),
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeReportAccepted(w, result)
}

func (h *handlers) generateReport(w http.ResponseWriter, r *http.Request) {
	result, err := h.deps.Service.Report(r.Context(), app.FinishCommand{
		SessionID:      r.PathValue("id"),
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeReportAccepted(w, result)
}

func (h *handlers) writeReportAccepted(w http.ResponseWriter, result app.FinishResult) {
	if result.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	body := map[string]any{
		"run_id":       result.RunID,
		"status":       "accepted",
		"run_state":    string(result.Status),
		"report_ready": result.ReportReady,
	}
	if result.ReportID != "" {
		body["report_id"] = result.ReportID
	}
	writeJSON(w, http.StatusAccepted, body)
}

type reportDTO struct {
	ReportID     string              `json:"report_id"`
	SessionID    string              `json:"session_id"`
	RunID        string              `json:"run_id"`
	Status       string              `json:"status"`
	OverallScore float64             `json:"overall_score"`
	Degraded     bool                `json:"degraded"`
	Structured   domainreport.Report `json:"structured"`
	Markdown     string              `json:"markdown"`
	CreatedAt    time.Time           `json:"created_at"`
}

func (h *handlers) getReport(w http.ResponseWriter, r *http.Request) {
	rec, err := h.deps.Service.GetReport(r.Context(), r.PathValue("id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reportDTO{
		ReportID:     rec.ID,
		SessionID:    rec.SessionID,
		RunID:        rec.RunID,
		Status:       rec.Status,
		OverallScore: rec.Report.OverallScore,
		Degraded:     rec.Report.Degraded,
		Structured:   rec.Report,
		Markdown:     rec.Markdown,
		CreatedAt:    rec.CreatedAt,
	})
}

func (h *handlers) abortSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.deps.Service.Abort(r.Context(), id); err != nil {
		h.writeError(w, r, err)
		return
	}
	sess, err := h.deps.Service.GetSession(r.Context(), id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSessionDTO(sess))
}

func (h *handlers) retryRun(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	runID := r.PathValue("run_id")
	if err := h.deps.Service.Retry(r.Context(), sessionID, runID); err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"run_id": runID,
		"status": "accepted",
	})
}

type eventEnvelope struct {
	EventID       int64           `json:"event_id"`
	SessionID     string          `json:"session_id"`
	RunID         string          `json:"run_id,omitempty"`
	Type          string          `json:"type"`
	OccurredAt    time.Time       `json:"occurred_at"`
	SchemaVersion int             `json:"schema_version"`
	Payload       json.RawMessage `json:"payload"`
}

func (h *handlers) subscribeEvents(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	log := h.deps.Logger.With("session_id", sessionID, "request_id", reqctx.From(r.Context()))
	if _, err := h.deps.Service.GetSession(r.Context(), sessionID); err != nil {
		h.writeError(w, r, err)
		return
	}

	afterID := parseAfterID(r)
	writer, err := sse.NewWriter(w)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()

	// Subscribe before the first query: events committed between LISTEN being
	// registered and our backlog read are picked up by that read, and later
	// commits wake the loop. The signal is only a hint; session_events stays
	// the source of truth, which keeps Last-Event-ID replay duplicate-free.
	signals, err := h.deps.Service.SubscribeEvents(ctx, sessionID)
	if err != nil {
		log.Error("subscribe events for sse", "error", err)
		return
	}

	h.deps.Metrics.SSEConnectionOpened()
	defer h.deps.Metrics.SSEConnectionClosed()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		// Drain in batches until caught up; only then wait for a notification.
		for {
			listStart := time.Now()
			events, err := h.deps.Service.ListEvents(ctx, sessionID, afterID, 100)
			if err != nil {
				log.Error("list events for sse", "error", err)
				return
			}
			if len(events) == 0 {
				break
			}
			for _, e := range events {
				payload := e.Payload
				if len(payload) == 0 {
					payload = []byte("{}")
				}
				envelope := eventEnvelope{
					EventID:       e.ID,
					SessionID:     e.SessionID,
					RunID:         e.RunID,
					Type:          e.Type,
					OccurredAt:    e.CreatedAt,
					SchemaVersion: 1,
					Payload:       json.RawMessage(payload),
				}
				if err := writer.WriteEvent(strconv.FormatInt(e.ID, 10), e.Type, envelope); err != nil {
					return
				}
				h.deps.Metrics.ObserveEventDelivery(time.Since(listStart))
				afterID = e.ID

				if isTerminalEvent(e.Type) {
					return
				}
			}
			if len(events) < 100 {
				break
			}
		}

		select {
		case <-ctx.Done():
			return
		case _, ok := <-signals:
			if !ok {
				return
			}
		case <-heartbeat.C:
			writer.Heartbeat()
		}
	}
}

func isTerminalEvent(eventType string) bool {
	switch eventType {
	case session.EventSessionCompleted, session.EventSessionFailed, session.EventSessionAborted:
		return true
	default:
		return false
	}
}

func parseAfterID(r *http.Request) int64 {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after")
	}
	if raw == "" {
		return 0
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}

func readOptionalBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		return nil, errors.New("读取请求体失败或超过 1 MiB")
	}
	return body, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	body, err := readOptionalBody(w, r)
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return errors.New("请求体不能为空")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return errors.New("请求体不是合法 JSON：" + err.Error())
	}
	return nil
}
