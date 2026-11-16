package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
)

type sessionListResponse struct {
	Sessions []struct {
		SessionID    string `json:"session_id"`
		TargetRole   string `json:"target_role"`
		Status       string `json:"status"`
		LatestReport *struct {
			TotalScore  float64 `json:"total_score"`
			ScoreBand   string  `json:"score_band"`
			GeneratedAt string  `json:"generated_at"`
		} `json:"latest_report"`
	} `json:"sessions"`
	NextBefore   string `json:"next_before"`
	NextBeforeID string `json:"next_before_id"`
}

func createSessionThroughAPI(t *testing.T, srvURL, role string) string {
	t.Helper()
	resp, err := http.Post(srvURL+"/v1/sessions", "application/json", strings.NewReader(
		`{"target_role":`+jsonString(role)+`,"resume_text":"3 年 Go 经验","jd_text":"Go 后端"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	return created.SessionID
}

func getSessionList(t *testing.T, srvURL, query string) (*http.Response, sessionListResponse) {
	t.Helper()
	target := srvURL + "/v1/sessions"
	if query != "" {
		target += "?" + query
	}
	resp, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	var out sessionListResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp, out
}

func TestListSessionsIsOwnerScopedAndPaginated(t *testing.T) {
	srv, svc, _ := newTestRouter(t)

	created := map[string]bool{}
	for _, role := range []string{"Go 后端", "服务端开发", "基础架构"} {
		created[createSessionThroughAPI(t, srv.URL, role)] = true
	}
	// A session owned by someone else must never leak into the caller's list.
	if _, err := svc.CreateSession(context.Background(), app.CreateSessionInput{
		OwnerID:    "usr_other",
		TargetRole: "别人的岗位",
		ResumeText: "别人的简历",
		JDText:     "别人的 JD",
		Config:     session.DefaultConfig(),
	}); err != nil {
		t.Fatal(err)
	}

	resp, first := getSessionList(t, srv.URL, "limit=2")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(first.Sessions) != 2 {
		t.Fatalf("page 1 = %d sessions", len(first.Sessions))
	}
	if first.NextBefore == "" || first.NextBeforeID == "" {
		t.Fatal("page 1 must expose a keyset cursor")
	}

	cursor := url.Values{}
	cursor.Set("limit", "2")
	cursor.Set("before", first.NextBefore)
	cursor.Set("before_id", first.NextBeforeID)
	resp2, err := http.Get(srv.URL + "/v1/sessions?" + cursor.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("page 2 status = %d", resp2.StatusCode)
	}
	var second sessionListResponse
	if err := json.NewDecoder(resp2.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	if len(second.Sessions) != 1 {
		t.Fatalf("page 2 = %d sessions", len(second.Sessions))
	}
	if second.NextBefore != "" {
		t.Fatal("last page must not expose a cursor")
	}

	seen := map[string]bool{}
	for _, item := range append(first.Sessions, second.Sessions...) {
		if !created[item.SessionID] {
			t.Fatalf("leaked session %q (role %q)", item.SessionID, item.TargetRole)
		}
		if seen[item.SessionID] {
			t.Fatalf("session %q appears twice", item.SessionID)
		}
		if item.LatestReport != nil {
			t.Fatalf("session %q has no report but returned %+v", item.SessionID, item.LatestReport)
		}
		seen[item.SessionID] = true
	}
	if len(seen) != len(created) {
		t.Fatalf("listed %d of %d sessions", len(seen), len(created))
	}
}

func TestListSessionsValidatesCursor(t *testing.T) {
	srv, _, _ := newTestRouter(t)

	resp, err := http.Get(srv.URL + "/v1/sessions?limit=abc")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("limit status = %d", resp.StatusCode)
	}

	resp2, err := http.Get(srv.URL + "/v1/sessions?before=2026-11-06T10:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("cursor status = %d", resp2.StatusCode)
	}
}
