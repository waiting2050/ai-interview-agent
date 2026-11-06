package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/domain/resume"
)

type resumeParseBody struct {
	Filename    string         `json:"filename"`
	Format      string         `json:"format"`
	Chars       int            `json:"chars"`
	Text        string         `json:"text"`
	Profile     resume.Profile `json:"profile"`
	ModelCalled bool           `json:"model_called"`
	Code        string         `json:"code"`
	Message     string         `json:"message"`
}

func postResumeFile(t *testing.T, srv *httptest.Server, field, filename string, data []byte) (*http.Response, resumeParseBody) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if field != "" {
		part, err := mw.CreateFormFile(field, filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/resume/parse", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var parsed resumeParseBody
	_ = json.Unmarshal(body, &parsed)
	return resp, parsed
}

func TestResumeParseSuccessFillsProfile(t *testing.T) {
	srv, _, _ := newTestRouter(t)

	text := "5 年 Go 后端经验，负责订单履约链路\n" +
		"技能：Go、MySQL、Redis\n" +
		"项目：订单幂等改造，用本地消息表保证最终一致性\n"
	resp, body := postResumeFile(t, srv, "file", "resume.txt", []byte(text))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %+v", resp.StatusCode, body)
	}
	if body.Format != "txt" || body.Chars == 0 || body.Text == "" {
		t.Fatalf("body = %+v", body)
	}
	if body.Profile.Years != 5 {
		t.Fatalf("years = %d, want 5", body.Profile.Years)
	}
	found := false
	for _, skill := range body.Profile.Skills {
		if skill == "Go" {
			found = true
		}
	}
	if !found || len(body.Profile.Projects) == 0 {
		t.Fatalf("profile = %+v", body.Profile)
	}
	if body.ModelCalled {
		t.Fatal("parse must not call the model")
	}
}

func TestResumeParseRejectsUnsupportedType(t *testing.T) {
	srv, _, _ := newTestRouter(t)

	resp, body := postResumeFile(t, srv, "file", "resume.exe", []byte("MZ..."))
	if resp.StatusCode != http.StatusBadRequest || body.Code != "unsupported_file_type" {
		t.Fatalf("status = %d, body = %+v", resp.StatusCode, body)
	}
}

func TestResumeParseRejectsImageAndExplainsOCR(t *testing.T) {
	srv, _, _ := newTestRouter(t)

	resp, body := postResumeFile(t, srv, "file", "resume.png", []byte{0x89, 'P', 'N', 'G'})
	if resp.StatusCode != http.StatusBadRequest || body.Code != "image_requires_ocr" {
		t.Fatalf("status = %d, body = %+v", resp.StatusCode, body)
	}
	if body.Message == "" {
		t.Fatal("image rejection must explain the OCR branch")
	}
}

func TestResumeParseRejectsOversizedFile(t *testing.T) {
	srv, _, _ := newTestRouter(t)

	data := make([]byte, resume.MaxDocumentSize+1)
	resp, body := postResumeFile(t, srv, "file", "resume.txt", data)
	if resp.StatusCode != http.StatusRequestEntityTooLarge || body.Code != "file_too_large" {
		t.Fatalf("status = %d, body = %+v", resp.StatusCode, body)
	}
}

func TestResumeParseFailureAllowsManualFallback(t *testing.T) {
	srv, _, _ := newTestRouter(t)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("readme.txt")
	_, _ = w.Write([]byte("broken resume"))
	_ = zw.Close()

	resp, body := postResumeFile(t, srv, "file", "resume.docx", buf.Bytes())
	if resp.StatusCode != http.StatusUnprocessableEntity || body.Code != "parse_failed" {
		t.Fatalf("status = %d, body = %+v", resp.StatusCode, body)
	}
	if body.Message == "" {
		t.Fatal("parse failure must carry a message the UI can show")
	}
}

func TestResumeParseRequiresFileField(t *testing.T) {
	srv, _, _ := newTestRouter(t)

	resp, body := postResumeFile(t, srv, "", "", nil)
	if resp.StatusCode != http.StatusBadRequest || body.Code != "invalid_upload" {
		t.Fatalf("status = %d, body = %+v", resp.StatusCode, body)
	}
}
