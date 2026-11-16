package httpapi

import (
	"net/http"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
)

type predictionRequest struct {
	TargetRole    string          `json:"target_role"`
	Difficulty    string          `json:"difficulty"`
	JDText        string          `json:"jd_text"`
	Domain        string          `json:"domain"`
	ResumeText    string          `json:"resume_text"`
	ResumeProfile *resume.Profile `json:"resume_profile"`
	Count         int             `json:"count"`
}

// predictQuestions forecasts likely interview questions from a resume. It is
// stateless: nothing is written until the candidate starts a session, so the
// API can be retried freely.
func (h *handlers) predictQuestions(w http.ResponseWriter, r *http.Request) {
	if h.deps.Predictor == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{
			RequestID: "",
			Code:      "prediction_unavailable",
			Message:   "预测题服务未启用",
		})
		return
	}

	var req predictionRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}

	profile := resume.Profile{}
	if req.ResumeProfile != nil {
		profile = *req.ResumeProfile
	}
	if req.ResumeText == "" && profile.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code:    "invalid_argument",
			Message: "resume_text 或 resume_profile 至少提供一个",
		})
		return
	}
	if req.ResumeText != "" {
		// Explicit profile wins; text parsing is the convenience path the web
		// form uses so the client does not have to duplicate the parser.
		if profile.IsZero() {
			profile = resume.Parse(req.ResumeText)
		}
	}
	if req.TargetRole == "" {
		writeJSON(w, http.StatusBadRequest, errorBody{
			Code:    "invalid_argument",
			Message: "target_role 不能为空",
		})
		return
	}

	result, err := h.deps.Predictor.Predict(r.Context(), interview.PredictionInput{
		TargetRole: req.TargetRole,
		Difficulty: req.Difficulty,
		JDText:     req.JDText,
		Domain:     req.Domain,
		Profile:    profile,
		Count:      req.Count,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"domain":       result.Domain,
		"generator":    result.Generator,
		"model_called": result.ModelCalled,
		"degraded":     result.Degraded,
		"questions":    result.Questions,
	})
}
