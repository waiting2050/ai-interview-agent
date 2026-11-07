package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/auth"
	"github.com/waiting2050/ai-interview-agent/internal/domain/review"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

func newAuthTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	store := memory.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	signer, err := auth.NewSigner([]byte("test-secret-0123456789abcdef0123456789"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewRouter(Deps{
		Auth:     auth.New(store.Repos().Users, signer),
		Service:  app.New(store, app.Options{Logger: logger, Synchronous: true}),
		Logger:   logger,
		Reviewer: review.New(store.Repos().Reviews),
	}))
	t.Cleanup(srv.Close)
	return srv
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	User        struct {
		ID     string `json:"id"`
		Email  string `json:"email"`
		IsDemo bool   `json:"is_demo"`
	} `json:"user"`
}

func postJSON(t *testing.T, srv *httptest.Server, path, token string, payload any) (*http.Response, []byte) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func getWithToken(t *testing.T, srv *httptest.Server, path, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func register(t *testing.T, srv *httptest.Server, email string) tokenResponse {
	t.Helper()
	resp, body := postJSON(t, srv, "/v1/auth/register", "", map[string]string{
		"email":    email,
		"password": "correct-horse-battery",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d body=%s", resp.StatusCode, body)
	}
	var out tokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func createOwnedSession(t *testing.T, srv *httptest.Server, token string) string {
	t.Helper()
	resp, body := postJSON(t, srv, "/v1/sessions", token, map[string]any{
		"candidate_name": "张三",
		"target_role":    "后端工程师",
		"resume_text":    "五年 Go 经验",
		"jd_text":        "负责后端服务",
		"domain":         "go-backend",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create session status = %d body=%s", resp.StatusCode, body)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.SessionID == "" {
		t.Fatalf("empty session id: %s", body)
	}
	return out.SessionID
}

func TestProtectedRoutesRejectAnonymous(t *testing.T) {
	srv := newAuthTestServer(t)

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/sessions/anything"},
		{http.MethodPost, "/v1/sessions"},
		{http.MethodGet, "/v1/review/due"},
		{http.MethodGet, "/v1/jd/anything"},
		{http.MethodGet, "/v1/sessions/anything/events"},
		{http.MethodGet, "/v1/domains"},
	}
	for _, tc := range cases {
		req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d body=%s", tc.method, tc.path, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), `"unauthorized"`) {
			t.Fatalf("%s %s body = %s", tc.method, tc.path, body)
		}
	}
}

func TestCrossUserAccessReturns404(t *testing.T) {
	srv := newAuthTestServer(t)
	alice := register(t, srv, "alice@example.com")
	bob := register(t, srv, "bob@example.com")
	sessionID := createOwnedSession(t, srv, alice.AccessToken)

	owned, _ := getWithToken(t, srv, "/v1/sessions/"+sessionID, alice.AccessToken)
	if owned.StatusCode != http.StatusOK {
		t.Fatalf("owner get status = %d", owned.StatusCode)
	}

	paths := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/sessions/" + sessionID},
		{http.MethodPost, "/v1/sessions/" + sessionID + "/start"},
		{http.MethodPost, "/v1/sessions/" + sessionID + "/abort"},
		{http.MethodGet, "/v1/sessions/" + sessionID + "/report"},
		{http.MethodGet, "/v1/sessions/" + sessionID + "/events"},
		{http.MethodGet, "/v1/review?session_id=" + sessionID},
	}
	for _, tc := range paths {
		req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bob.AccessToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s as bob status = %d body=%s", tc.method, tc.path, resp.StatusCode, body)
		}
	}
}

func TestDemoAccountsAreIsolated(t *testing.T) {
	srv := newAuthTestServer(t)
	respA, bodyA := postJSON(t, srv, "/v1/auth/demo", "", nil)
	if respA.StatusCode != http.StatusOK {
		t.Fatalf("demo A status = %d body=%s", respA.StatusCode, bodyA)
	}
	respB, bodyB := postJSON(t, srv, "/v1/auth/demo", "", nil)
	if respB.StatusCode != http.StatusOK {
		t.Fatalf("demo B status = %d body=%s", respB.StatusCode, bodyB)
	}
	var a, b tokenResponse
	if err := json.Unmarshal(bodyA, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bodyB, &b); err != nil {
		t.Fatal(err)
	}
	if !a.User.IsDemo || !b.User.IsDemo {
		t.Fatalf("expected demo users, got %+v %+v", a.User, b.User)
	}
	if a.User.ID == b.User.ID {
		t.Fatalf("demo users share id %q", a.User.ID)
	}

	sessionID := createOwnedSession(t, srv, a.AccessToken)
	for _, path := range []string{
		"/v1/sessions/" + sessionID,
		"/v1/sessions/" + sessionID + "/events",
	} {
		resp, body := getWithToken(t, srv, path, b.AccessToken)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("demo B %s status = %d body=%s", path, resp.StatusCode, body)
		}
	}
	// A demo token must not unlock another demo user's card list either.
	resp, body := getWithToken(t, srv, "/v1/review?session_id="+sessionID, b.AccessToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("demo B review list status = %d body=%s", resp.StatusCode, body)
	}
}

func TestSSEAcceptsQueryTokenOnlyForEvents(t *testing.T) {
	srv := newAuthTestServer(t)
	alice := register(t, srv, "sse@example.com")
	sessionID := createOwnedSession(t, srv, alice.AccessToken)

	// EventSource cannot set headers, so the events endpoint accepts the token
	// as a query parameter.
	client := &http.Client{Timeout: 400 * time.Millisecond}
	resp, err := client.Get(srv.URL + "/v1/sessions/" + sessionID + "/events?access_token=" + alice.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sse status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// The same token in a query string must not authenticate other routes.
	bad, body := getWithToken(t, srv, "/v1/sessions/"+sessionID+"?access_token="+alice.AccessToken, "")
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("query token on non-events route status = %d body=%s", bad.StatusCode, body)
	}
}

func TestDemoLoginIsRateLimited(t *testing.T) {
	store := memory.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	signer, err := auth.NewSigner([]byte("test-secret-0123456789abcdef0123456789"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewRouter(Deps{
		Auth:            auth.New(store.Repos().Users, signer),
		Service:         app.New(store, app.Options{Logger: logger, Synchronous: true}),
		Logger:          logger,
		DemoLoginLimit:  2,
		DemoLoginWindow: time.Hour,
	}))
	t.Cleanup(srv.Close)

	for i := 0; i < 2; i++ {
		resp, body := postJSON(t, srv, "/v1/auth/demo", "", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("demo %d status = %d body=%s", i, resp.StatusCode, body)
		}
	}
	resp, body := postJSON(t, srv, "/v1/auth/demo", "", nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third demo status = %d body=%s", resp.StatusCode, body)
	}
}
