package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
)

type segmentTypeOverride struct {
	Index int    `json:"index"`
	Type  string `json:"type"`
}

type resumeAnalyzeRequest struct {
	Text         string                `json:"text"`
	JDText       string                `json:"jd_text"`
	TargetRole   string                `json:"target_role"`
	Filename     string                `json:"filename"`
	SegmentTypes []segmentTypeOverride `json:"segment_types"`
}

// analyzeResume runs the deterministic rules engine. Input is pasted or
// extracted text; uploads go through POST /v1/resume/parse first. No model is
// called on this path, so model_called is always false.
func (h *handlers) analyzeResume(w http.ResponseWriter, r *http.Request) {
	var req resumeAnalyzeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		h.writeError(w, r, fmt.Errorf("%w: %s", app.ErrInvalidArgument, err))
		return
	}

	overrides := make(map[int]resume.SegmentType, len(req.SegmentTypes))
	for _, item := range req.SegmentTypes {
		if item.Index < 0 {
			h.writeError(w, r, fmt.Errorf("%w: segment index must be >= 0", app.ErrInvalidArgument))
			return
		}
		if !resume.IsValidSegmentType(item.Type) || resume.SegmentType(item.Type) == resume.TypeUnknown {
			h.writeError(w, r, fmt.Errorf("%w: %q（可用值：%v）", app.ErrInvalidArgument, item.Type, resume.SegmentTypeOptions()))
			return
		}
		overrides[item.Index] = resume.SegmentType(item.Type)
	}

	analyzer := h.deps.ResumeAnalyzer
	if analyzer == nil {
		analyzer = resume.DefaultAnalyzer()
	}
	diag, err := analyzer.Analyze(resume.AnalyzeOptions{
		Text:         req.Text,
		JDText:       req.JDText,
		TargetRole:   req.TargetRole,
		Filename:     req.Filename,
		SegmentTypes: overrides,
	})
	if err != nil {
		if errors.Is(err, resume.ErrEmptyResume) || errors.Is(err, resume.ErrInvalidSegmentType) {
			h.writeError(w, r, fmt.Errorf("%w: %s", app.ErrInvalidArgument, err.Error()))
			return
		}
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, diag)
}
