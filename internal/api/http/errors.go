package httpapi

import (
	"errors"
	"net/http"

	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
	"github.com/waiting2050/ai-interview-agent/internal/ports"
)

type errorBody struct {
	RequestID string         `json:"request_id"`
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
	RunID     string         `json:"run_id,omitempty"`
}

func (h *handlers) writeError(w http.ResponseWriter, r *http.Request, err error) {
	body := errorBody{
		RequestID: reqctx.From(r.Context()),
		Code:      "internal",
		Message:   "服务内部错误",
	}

	var inProgress *app.RunInProgressError
	var transition *session.TransitionError

	switch {
	case errors.As(err, &inProgress):
		body.Code = "run_in_progress"
		body.Message = "该会话已有运行中的任务，请订阅事件或稍后重试"
		body.Retryable = true
		body.RunID = inProgress.RunID
		writeJSON(w, http.StatusConflict, body)

	case errors.As(err, &transition):
		body.Code = "state_conflict"
		body.Message = "会话状态不允许该操作"
		body.Details = map[string]any{
			"status":  string(transition.Status),
			"phase":   string(transition.Phase),
			"command": string(transition.Command),
			"reason":  transition.Reason,
		}
		writeJSON(w, http.StatusConflict, body)

	case errors.Is(err, app.ErrInvalidArgument):
		body.Code = "invalid_argument"
		body.Message = err.Error()
		writeJSON(w, http.StatusBadRequest, body)

	case errors.Is(err, app.ErrReportNotReady):
		body.Code = "report_not_ready"
		body.Message = "报告尚未生成，请先调用 finish 或 report"
		body.Retryable = true
		writeJSON(w, http.StatusConflict, body)

	case errors.Is(err, ports.ErrNotFound):
		body.Code = "session_not_found"
		body.Message = "资源不存在"
		writeJSON(w, http.StatusNotFound, body)

	case errors.Is(err, ports.ErrIdempotencyReused):
		body.Code = "idempotency_key_reused"
		body.Message = "同一 Idempotency-Key 被用于不同请求体"
		writeJSON(w, http.StatusUnprocessableEntity, body)

	case errors.Is(err, app.ErrRunInProgress):
		body.Code = "run_in_progress"
		body.Message = "该会话已有运行中的任务"
		body.Retryable = true
		writeJSON(w, http.StatusConflict, body)

	case errors.Is(err, ports.ErrLockBusy):
		body.Code = "lock_busy"
		body.Message = "该会话正在处理其他请求，请稍后重试"
		body.Retryable = true
		writeJSON(w, http.StatusConflict, body)

	case errors.Is(err, ports.ErrLockBackend):
		body.Code = "lock_backend_unavailable"
		body.Message = "会话锁服务暂不可用，请稍后重试"
		body.Retryable = true
		writeJSON(w, http.StatusServiceUnavailable, body)

	case errors.Is(err, ports.ErrStateConflict):
		body.Code = "state_conflict"
		body.Message = "会话状态已变化，请刷新后重试"
		writeJSON(w, http.StatusConflict, body)

	default:
		h.deps.Logger.Error("http handler error", "request_id", body.RequestID, "error", err)
		writeJSON(w, http.StatusInternalServerError, body)
	}
}
