package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	"github.com/waiting2050/ai-interview-agent/internal/domain/jd"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	platformlock "github.com/waiting2050/ai-interview-agent/internal/platform/lock"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

// fixedEvaluator keeps the HTTP tests independent from any model.
type fixedEvaluator struct{}

func (fixedEvaluator) Evaluate(_ context.Context, in interview.EvaluateInput) (interview.Outcome, error) {
	scores := map[evaluation.Dimension]int{}
	for _, d := range evaluation.DimensionOrder {
		scores[d] = 4
	}
	quote := in.Answer
	if r := []rune(quote); len(r) > 8 {
		quote = string(r[:8])
	}
	return interview.Outcome{
		Result: evaluation.Result{
			SchemaVersion: evaluation.SchemaVersion,
			Scores:        scores,
			Evidence:      []evaluation.Evidence{{Dimension: evaluation.DimensionCorrectness, Quote: quote, Reason: "ok"}},
			Confidence:    0.8,
		},
		ModelName:     "fixed",
		PromptVersion: "eval-v1",
	}, nil
}

func newTestRouter(t *testing.T) (*httptest.Server, *app.Service, *obs.Metrics) {
	t.Helper()
	store := memory.New()
	metrics := obs.NewMetrics()
	svc := app.New(store, app.Options{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Synchronous: true,
		Now:         func() time.Time { return time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC) },
		Evaluator:   fixedEvaluator{},
		Metrics:     metrics,
	})
	authSvc, token, _ := testAuthFor(t, store)
	srv := httptest.NewServer(withTestAuthorization(NewRouter(Deps{
		Auth:       authSvc,
		Service:    svc,
		Health:     store.Ping,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:    metrics,
		JDAnalyzer: jd.New(store.Repos().JDs, nil),
	}), token))
	t.Cleanup(srv.Close)
	return srv, svc, metrics
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s", msg)
}

func TestCreateSessionValidatesAndReturnsDomainRubric(t *testing.T) {
	srv, _, _ := newTestRouter(t)

	resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", strings.NewReader(`{
		"target_role":"AI 应用工程师","resume_text":"resume","jd_text":"RAG","domain":"ai-agent"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created struct {
		Domain string `json:"domain"`
		Rubric struct {
			Dimensions []struct {
				Key    string  `json:"key"`
				Name   string  `json:"name"`
				Weight float64 `json:"weight"`
			} `json:"dimensions"`
		} `json:"rubric"`
	}
	if err := decodeBody(resp.Body, &created); err != nil {
		t.Fatal(err)
	}
	if created.Domain != "ai-agent" {
		t.Fatalf("domain = %q", created.Domain)
	}
	found := false
	for _, dim := range created.Rubric.Dimensions {
		if dim.Key == "system_design" && dim.Name == "系统设计" && dim.Weight == 0.20 {
			found = true
		}
	}
	if !found {
		t.Fatalf("rubric did not come from the ai-agent pack: %+v", created.Rubric)
	}

	bad, err := http.Post(srv.URL+"/v1/sessions", "application/json", strings.NewReader(`{
		"target_role":"x","resume_text":"resume","jd_text":"jd","domain":"not-a-pack"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown domain status = %d, want 400", bad.StatusCode)
	}
	var body errorBody
	if err := decodeBody(bad.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "invalid_argument" || !strings.Contains(body.Message, "unknown domain") {
		t.Fatalf("error = %+v", body)
	}
}

func TestListDomainsReturnsThreePacks(t *testing.T) {
	srv, _, _ := newTestRouter(t)
	resp, err := http.Get(srv.URL + "/v1/domains")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Domains []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			TopicCount int    `json:"topic_count"`
			Rubric     struct {
				Dimensions []struct {
					Key string `json:"key"`
				} `json:"dimensions"`
			} `json:"rubric"`
		} `json:"domains"`
	}
	if err := decodeBody(resp.Body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Domains) != 3 {
		t.Fatalf("domains = %d", len(out.Domains))
	}
	for _, d := range out.Domains {
		if d.ID == "" || d.Name == "" || d.TopicCount < 10 || len(d.Rubric.Dimensions) == 0 {
			t.Fatalf("incomplete domain %+v", d)
		}
	}
}

func TestCreateStartAndSSEReplayFromLastEventID(t *testing.T) {
	srv, svc, _ := newTestRouter(t)

	resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", strings.NewReader(`{
		"candidate_name":"张三","target_role":"Go 后端","resume_text":"resume","jd_text":"jd"
	}`))
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
	if err := decodeBody(resp.Body, &created); err != nil {
		t.Fatal(err)
	}
	if created.SessionID == "" {
		t.Fatal("missing session_id")
	}

	start := func(key string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions/"+created.SessionID+"/start", bytes.NewReader([]byte(`{}`)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Idempotency-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	first := start("http-key")
	defer first.Body.Close()
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d", first.StatusCode)
	}
	var started struct {
		RunID string `json:"run_id"`
	}
	if err := decodeBody(first.Body, &started); err != nil {
		t.Fatal(err)
	}
	if started.RunID == "" {
		t.Fatal("missing run_id")
	}

	second := start("http-key")
	defer second.Body.Close()
	if second.StatusCode != http.StatusAccepted || second.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status=%d header=%q", second.StatusCode, second.Header.Get("Idempotent-Replayed"))
	}

	// Abort appends a terminal event so the SSE handler can finish.
	abort, err := http.Post(srv.URL+"/v1/sessions/"+created.SessionID+"/abort", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer abort.Body.Close()
	if abort.StatusCode != http.StatusOK {
		t.Fatalf("abort status = %d", abort.StatusCode)
	}

	events, err := svc.ListEvents(context.Background(), created.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var questionID string
	for _, e := range events {
		if e.Type == session.EventQuestionAsked {
			questionID = strconv.FormatInt(e.ID, 10)
		}
	}
	if questionID == "" {
		t.Fatal("question.asked event missing")
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/sessions/"+created.SessionID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", questionID)
	sseResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	body, err := io.ReadAll(sseResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "event: "+session.EventSessionAborted) {
		t.Fatalf("SSE body missing abort event:\n%s", text)
	}
	if strings.Contains(text, session.EventSessionCreated) || strings.Contains(text, session.EventQuestionAsked) {
		t.Fatalf("Last-Event-ID was not respected:\n%s", text)
	}
	if !strings.Contains(text, `"schema_version":1`) {
		t.Fatalf("SSE body missing envelope:\n%s", text)
	}
}

func TestSubmitAnswerReturns202AndAdvancesSession(t *testing.T) {
	srv, svc, _ := newTestRouter(t)

	resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", strings.NewReader(`{
		"candidate_name":"张三","target_role":"Go 后端","resume_text":"resume","jd_text":"jd"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var created struct {
		SessionID string `json:"session_id"`
	}
	if err := decodeBody(resp.Body, &created); err != nil {
		t.Fatal(err)
	}

	startReq, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions/"+created.SessionID+"/start", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	startReq.Header.Set("Idempotency-Key", "start-http")
	startResp, err := http.DefaultClient.Do(startReq)
	if err != nil {
		t.Fatal(err)
	}
	defer startResp.Body.Close()
	if startResp.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d", startResp.StatusCode)
	}

	sess, err := svc.GetSession(context.Background(), created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.PendingTurnID == "" {
		t.Fatal("start did not produce a pending turn")
	}

	answerBody := `{"turn_id":"` + sess.PendingTurnID + `","answer":"我在订单服务里用本地消息表加定时补偿保证最终一致性，压测下单峰值 1.2 万 TPS。"}`
	submit := func(key string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions/"+created.SessionID+"/answers", strings.NewReader(answerBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	first := submit("answer-http")
	defer first.Body.Close()
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("answer status = %d", first.StatusCode)
	}
	var accepted struct {
		RunID string `json:"run_id"`
	}
	if err := decodeBody(first.Body, &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.RunID == "" {
		t.Fatal("missing run_id")
	}

	second := submit("answer-http")
	defer second.Body.Close()
	if second.StatusCode != http.StatusAccepted || second.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status=%d header=%q", second.StatusCode, second.Header.Get("Idempotent-Replayed"))
	}

	updated, err := svc.GetSession(context.Background(), created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PlanCursor != sess.PlanCursor+1 {
		t.Fatalf("plan_cursor = %d, want %d", updated.PlanCursor, sess.PlanCursor+1)
	}
	if updated.PendingTurnID == "" || updated.PendingTurnID == sess.PendingTurnID {
		t.Fatalf("pending turn did not advance: %q", updated.PendingTurnID)
	}

	events, err := svc.ListEvents(context.Background(), created.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		session.EventAnswerReceived,
		session.EventEvaluationDone,
		session.EventDecisionMade,
		session.EventQuestionAsked,
	} {
		found := false
		for _, e := range events {
			if e.Type == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing event %s in %v", want, eventTypesOf(events))
		}
	}
}

func TestFinishGeneratesReportOverHTTP(t *testing.T) {
	srv, svc, _ := newTestRouter(t)

	resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", strings.NewReader(`{
		"candidate_name":"张三","target_role":"Go 后端","resume_text":"resume","jd_text":"jd"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var created struct {
		SessionID string `json:"session_id"`
	}
	if err := decodeBody(resp.Body, &created); err != nil {
		t.Fatal(err)
	}

	startReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions/"+created.SessionID+"/start", bytes.NewReader([]byte(`{}`)))
	startReq.Header.Set("Idempotency-Key", "report-start")
	startResp, err := http.DefaultClient.Do(startReq)
	if err != nil {
		t.Fatal(err)
	}
	startResp.Body.Close()

	sess, err := svc.GetSession(context.Background(), created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	answerBody := `{"turn_id":"` + sess.PendingTurnID + `","answer":"我在订单服务里用本地消息表加定时补偿保证最终一致性，压测下单峰值 1.2 万 TPS。"}`
	answerReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions/"+created.SessionID+"/answers", strings.NewReader(answerBody))
	answerReq.Header.Set("Content-Type", "application/json")
	answerReq.Header.Set("Idempotency-Key", "report-answer")
	answerResp, err := http.DefaultClient.Do(answerReq)
	if err != nil {
		t.Fatal(err)
	}
	answerResp.Body.Close()

	finish := func(key string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions/"+created.SessionID+"/finish", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Idempotency-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	first := finish("report-finish")
	defer first.Body.Close()
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("finish status = %d", first.StatusCode)
	}
	var accepted struct {
		RunID       string `json:"run_id"`
		ReportReady bool   `json:"report_ready"`
	}
	if err := decodeBody(first.Body, &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.RunID == "" {
		t.Fatal("missing run_id")
	}

	reportResp, err := http.Get(srv.URL + "/v1/sessions/" + created.SessionID + "/report")
	if err != nil {
		t.Fatal(err)
	}
	defer reportResp.Body.Close()
	if reportResp.StatusCode != http.StatusOK {
		t.Fatalf("get report status = %d", reportResp.StatusCode)
	}
	var report struct {
		ReportID   string `json:"report_id"`
		Markdown   string `json:"markdown"`
		Structured struct {
			OverallScore float64 `json:"overall_score"`
			Sections     []struct {
				Index int `json:"index"`
			} `json:"sections"`
		} `json:"structured"`
	}
	if err := decodeBody(reportResp.Body, &report); err != nil {
		t.Fatal(err)
	}
	if report.ReportID == "" || report.Markdown == "" || len(report.Structured.Sections) != 5 {
		t.Fatalf("report = %+v", report)
	}
	if report.Structured.OverallScore <= 0 {
		t.Fatalf("overall score = %.1f", report.Structured.OverallScore)
	}
	second := finish("other-key")
	defer second.Body.Close()
	if second.StatusCode != http.StatusAccepted || second.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status=%d header=%q", second.StatusCode, second.Header.Get("Idempotent-Replayed"))
	}

}

func TestSSEDeliversNewEventWithoutPolling(t *testing.T) {
	srv, svc, metrics := newTestRouter(t)
	ctx := context.Background()

	resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", strings.NewReader(`{
		"candidate_name":"张三","target_role":"Go 后端","resume_text":"resume","jd_text":"jd"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		SessionID string `json:"session_id"`
	}
	if err := decodeBody(resp.Body, &created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	startReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions/"+created.SessionID+"/start", bytes.NewReader([]byte(`{}`)))
	startReq.Header.Set("Idempotency-Key", "sse-start")
	startResp, err := http.DefaultClient.Do(startReq)
	if err != nil {
		t.Fatal(err)
	}
	startResp.Body.Close()

	events, err := svc.ListEvents(ctx, created.SessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	lastID := strconv.FormatInt(events[len(events)-1].ID, 10)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/sessions/"+created.SessionID+"/events", nil)
	req.Header.Set("Last-Event-ID", lastID)
	sseResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()

	bodyCh := make(chan string, 1)
	go func() {
		body, _ := io.ReadAll(sseResp.Body)
		bodyCh <- string(body)
	}()

	// Give the SSE handler time to subscribe, then abort from another request.
	// With LISTEN/NOTIFY the abort arrives by wake-up; a 250ms poll is gone.
	waitFor(t, 2*time.Second, func() bool {
		return metrics.SSEConnectionCount() == 1
	}, "SSE connection gauge was not incremented")
	abort, err := http.Post(srv.URL+"/v1/sessions/"+created.SessionID+"/abort", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	abort.Body.Close()

	select {
	case body := <-bodyCh:
		if !strings.Contains(body, session.EventSessionAborted) {
			t.Fatalf("SSE body missing abort:\n%s", body)
		}
		if strings.Contains(body, session.EventQuestionAsked) {
			t.Fatalf("replayed events before Last-Event-ID:\n%s", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SSE did not deliver the new event")
	}

	waitFor(t, 2*time.Second, func() bool {
		return metrics.SSEConnectionCount() == 0
	}, "SSE connection gauge was not decremented")
	if got := metrics.EventDeliveryCount(); got == 0 {
		t.Fatal("event delivery latency was not observed")
	}
}

func TestMetricsEndpointExposesRegistry(t *testing.T) {
	srv, _, metrics := newTestRouter(t)
	metrics.IncModelCall("test-provider", "test-model", "success")

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("metrics content-type = %q", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "ai_interview_model_calls_total") ||
		!strings.Contains(text, `result="success"`) {
		t.Fatalf("metrics body missing model call sample:\n%s", text)
	}
}

func TestCommandReturnsRetryableBusyWhenLockHeld(t *testing.T) {
	store := memory.New()
	locker := platformlock.NewProcessLocker()
	metrics := obs.NewMetrics()
	svc := app.New(store, app.Options{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Synchronous: true,
		Evaluator:   fixedEvaluator{},
		Locker:      locker,
		Metrics:     metrics,
	})
	authSvc, token, _ := testAuthFor(t, store)
	srv := httptest.NewServer(withTestAuthorization(NewRouter(Deps{
		Auth:    authSvc,
		Service: svc,
		Health:  store.Ping,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics: metrics,
	}), token))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", strings.NewReader(`{
		"candidate_name":"张三","target_role":"Go 后端","resume_text":"resume","jd_text":"jd"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var created struct {
		SessionID string `json:"session_id"`
	}
	if err := decodeBody(resp.Body, &created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	ctx := context.Background()
	if err := locker.Acquire(ctx, "lock:session:"+created.SessionID, "holder"); err != nil {
		t.Fatal(err)
	}

	startReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions/"+created.SessionID+"/start", bytes.NewReader([]byte(`{}`)))
	startReq.Header.Set("Idempotency-Key", "locked-start")
	lockedResp, err := http.DefaultClient.Do(startReq)
	if err != nil {
		t.Fatal(err)
	}
	defer lockedResp.Body.Close()
	if lockedResp.StatusCode != http.StatusConflict {
		t.Fatalf("locked status = %d", lockedResp.StatusCode)
	}
	var body struct {
		Code      string `json:"code"`
		Retryable bool   `json:"retryable"`
	}
	if err := decodeBody(lockedResp.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "lock_busy" || !body.Retryable {
		t.Fatalf("error body = %+v", body)
	}
	if got := metrics.LockConflictTotal(); got != 1 {
		t.Fatalf("lock_conflicts_total = %d, want 1", got)
	}
}

func TestDemoPageServesEmbeddedAssets(t *testing.T) {
	srv, _, _ := newTestRouter(t)

	resp, err := http.Get(srv.URL + "/demo")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("demo status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("demo content-type = %q", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "面试教练") {
		t.Fatal("demo page must render Chinese copy")
	}

	for _, asset := range []string{"/demo/app.js", "/demo/style.css"} {
		resp, err := http.Get(srv.URL + asset)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", asset, resp.StatusCode)
		}
		if len(body) == 0 {
			t.Fatalf("%s must not be empty", asset)
		}
	}
}

func eventTypesOf(events []session.Event) []string {
	types := make([]string, 0, len(events))
	for _, e := range events {
		types = append(types, e.Type)
	}
	return types
}

func decodeBody(r io.Reader, dst any) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, dst)
}
