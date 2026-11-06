package httpapi

import (
	"errors"
	"io"
	"net/http"

	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
	"github.com/waiting2050/ai-interview-agent/internal/platform/reqctx"
)

// maxUploadBody leaves room for multipart boundaries and headers around the
// 5 MiB file itself. The file size is checked again after reading the part.
const maxUploadBody = resume.MaxDocumentSize + 512<<10

type resumeParseResponse struct {
	Filename    string         `json:"filename"`
	Format      string         `json:"format"`
	Bytes       int            `json:"bytes"`
	Chars       int            `json:"chars"`
	Truncated   bool           `json:"truncated"`
	Text        string         `json:"text"`
	Profile     resume.Profile `json:"profile"`
	ModelCalled bool           `json:"model_called"`
}

// parseResume accepts one multipart file field named "file" and extracts text
// locally. No model or network call happens on this path; images return the
// reserved OCR branch message.
func (h *handlers) parseResume(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)

	reader, err := r.MultipartReader()
	if err != nil {
		h.writeUploadError(w, r, "invalid_upload", "请使用 multipart/form-data 上传简历文件", http.StatusBadRequest, nil)
		return
	}

	var (
		filename string
		data     []byte
	)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			h.writeUploadReadError(w, r, err)
			return
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		filename = part.FileName()
		data, err = io.ReadAll(io.LimitReader(part, resume.MaxDocumentSize+1))
		part.Close()
		if err != nil {
			h.writeUploadReadError(w, r, err)
			return
		}
		break
	}
	if filename == "" {
		h.writeUploadError(w, r, "invalid_upload", "缺少 file 字段，请在表单中选择一份简历", http.StatusBadRequest, nil)
		return
	}
	if len(data) > resume.MaxDocumentSize {
		h.writeUploadError(w, r, "file_too_large", "简历文件超过 5 MB 上限", http.StatusRequestEntityTooLarge,
			map[string]any{"max_bytes": resume.MaxDocumentSize})
		return
	}

	doc, err := resume.Extract(filename, data)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	profile := resume.Parse(doc.Text)
	profile.Normalize()

	writeJSON(w, http.StatusOK, resumeParseResponse{
		Filename:    doc.Filename,
		Format:      string(doc.Format),
		Bytes:       doc.Bytes,
		Chars:       doc.Chars,
		Truncated:   doc.Truncated,
		Text:        doc.Text,
		Profile:     profile,
		ModelCalled: false,
	})
}

func (h *handlers) writeUploadReadError(w http.ResponseWriter, r *http.Request, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		h.writeUploadError(w, r, "file_too_large", "简历文件超过 5 MB 上限",
			http.StatusRequestEntityTooLarge, map[string]any{"max_bytes": resume.MaxDocumentSize})
		return
	}
	h.writeUploadError(w, r, "invalid_upload", "上传请求读取失败："+err.Error(), http.StatusBadRequest, nil)
}

func (h *handlers) writeUploadError(w http.ResponseWriter, r *http.Request, code, message string, status int, details map[string]any) {
	writeJSON(w, status, errorBody{
		RequestID: reqctx.From(r.Context()),
		Code:      code,
		Message:   message,
		Details:   details,
	})
}
