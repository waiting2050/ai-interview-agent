package httpapi

import (
	"errors"
	"net/http"

	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/domain/jd"
	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
	"github.com/waiting2050/ai-interview-agent/internal/domain/review"
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

	case errors.Is(err, resume.ErrUnsupportedFileType):
		body.Code = "unsupported_file_type"
		body.Message = "仅支持 PDF / DOCX / TXT / MD 简历文件"
		body.Details = map[string]any{"allowed_formats": []string{"pdf", "docx", "txt", "md"}}
		writeJSON(w, http.StatusBadRequest, body)

	case errors.Is(err, resume.ErrImageRequiresOCR):
		body.Code = "image_requires_ocr"
		body.Message = "PNG/JPG 简历需要 OCR 或多模态识别，本期尚未启用；请改用 PDF / DOCX / TXT / MD，或直接粘贴文本"
		writeJSON(w, http.StatusBadRequest, body)

	case errors.Is(err, resume.ErrFileTooLarge):
		body.Code = "file_too_large"
		body.Message = "简历文件超过 5 MB 上限"
		body.Details = map[string]any{"max_bytes": resume.MaxDocumentSize}
		writeJSON(w, http.StatusRequestEntityTooLarge, body)

	case errors.Is(err, resume.ErrExtractFailed):
		body.Code = "parse_failed"
		body.Message = "无法从文件中提取文本，请改用粘贴文本或换一份文件（扫描版 PDF 需 OCR，尚未启用）"
		body.Details = map[string]any{"fallback": "manual_text"}
		writeJSON(w, http.StatusUnprocessableEntity, body)

	case errors.Is(err, jd.ErrInvalidArgument), errors.Is(err, jd.ErrEmptyResume):
		body.Code = "invalid_argument"
		body.Message = err.Error()
		writeJSON(w, http.StatusBadRequest, body)

	case errors.Is(err, jd.ErrNotFound):
		body.Code = "jd_not_found"
		body.Message = "JD 记录不存在或尚未缓存"
		writeJSON(w, http.StatusNotFound, body)

	case errors.Is(err, jd.ErrFetchFailed):
		body.Code = "jd_fetch_failed"
		body.Message = "无法抓取该公开页面；请改为粘贴 JD 文本"
		body.Details = map[string]any{"error": err.Error()}
		body.Retryable = true
		writeJSON(w, http.StatusBadGateway, body)

	case errors.Is(err, review.ErrInvalidGrade), errors.Is(err, review.ErrInvalidArgument):
		body.Code = "invalid_argument"
		body.Message = err.Error()
		body.Details = map[string]any{"grade_range": []int{0, 5}}
		writeJSON(w, http.StatusBadRequest, body)

	case errors.Is(err, review.ErrNotFound):
		body.Code = "review_card_not_found"
		body.Message = "复习卡不存在"
		writeJSON(w, http.StatusNotFound, body)

	default:
		h.deps.Logger.Error("http handler error", "request_id", body.RequestID, "error", err)
		writeJSON(w, http.StatusInternalServerError, body)
	}
}
