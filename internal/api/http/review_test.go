package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/domain/review"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

func newReviewRouter(t *testing.T, now time.Time, cards []*review.Card) (*httptest.Server, *memory.Store) {
	t.Helper()
	store := memory.New()
	authSvc, token, userID := testAuthFor(t, store)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	seen := map[string]bool{}
	for _, card := range cards {
		if card.UserID == "" {
			card.UserID = userID
		}
		seen[card.SessionID] = true
	}
	if len(cards) > 0 {
		if err := store.Repos().Reviews.SaveMany(context.Background(), cards); err != nil {
			t.Fatal(err)
		}
	}
	// listReviews verifies session ownership through the app service, so seed
	// one owned session per card.
	for sessionID := range seen {
		err := store.Repos().Sessions.Create(context.Background(), &session.Session{
			ID:            sessionID,
			OwnerID:       userID,
			CandidateName: "review-test",
			TargetRole:    "backend",
			ResumeText:    "resume",
			JDText:        "jd",
			Config:        session.DefaultConfig(),
			Status:        session.StatusCreated,
			CreatedAt:     now,
			UpdatedAt:     now,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	svc := review.New(store.Repos().Reviews)
	svc.SetClock(func() time.Time { return now })
	srv := httptest.NewServer(withTestAuthorization(NewRouter(Deps{
		Auth:     authSvc,
		Service:  app.New(store, app.Options{Logger: logger, Synchronous: true}),
		Logger:   logger,
		Reviewer: svc,
	}), token))
	t.Cleanup(srv.Close)
	return srv, store
}

func TestReviewDueEmpty(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	srv, _ := newReviewRouter(t, now, nil)

	resp, err := http.Get(srv.URL + "/v1/review/due")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Count int           `json:"count"`
		Cards []interface{} `json:"cards"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 0 || len(body.Cards) != 0 {
		t.Fatalf("body = %+v", body)
	}
}

func TestReviewGradeValidationAndSchedule(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	card := &review.Card{
		ID:           "card-1",
		SessionID:    "sess-1",
		TurnID:       "turn-1",
		Topic:        "订单幂等",
		Question:     "如何保证订单不重复扣减？",
		AnswerDigest: "用订单号做幂等键",
		KeyPoints:    []string{"幂等键", "本地消息表"},
		EaseFactor:   review.DefaultEase,
		IntervalDays: 0,
		Repetitions:  0,
		DueAt:        now,
		LastGrade:    -1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	srv, _ := newReviewRouter(t, now, []*review.Card{card})

	// Due query with an explicit UTC+8 offset.
	resp, err := http.Get(srv.URL + "/v1/review/due?tz_offset_minutes=480")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var due struct {
		Count           int           `json:"count"`
		TZOffsetMinutes int           `json:"tz_offset_minutes"`
		Cards           []review.Card `json:"cards"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&due); err != nil {
		t.Fatal(err)
	}
	if due.Count != 1 || due.TZOffsetMinutes != 480 || due.Cards[0].ID != "card-1" {
		t.Fatalf("due = %+v", due)
	}

	// Out-of-range grades are rejected.
	badResp, err := http.Post(srv.URL+"/v1/review/card-1/grade", "application/json",
		strings.NewReader(`{"grade":9}`))
	if err != nil {
		t.Fatal(err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad grade status = %d", badResp.StatusCode)
	}
	var badBody errorBody
	if err := json.NewDecoder(badResp.Body).Decode(&badBody); err != nil {
		t.Fatal(err)
	}
	if badBody.Code != "invalid_argument" {
		t.Fatalf("bad grade code = %q", badBody.Code)
	}

	// A valid grade applies SM-2 and persists the calculation trace.
	okResp, err := http.Post(srv.URL+"/v1/review/card-1/grade", "application/json",
		strings.NewReader(`{"grade":5}`))
	if err != nil {
		t.Fatal(err)
	}
	defer okResp.Body.Close()
	if okResp.StatusCode != http.StatusOK {
		t.Fatalf("grade status = %d", okResp.StatusCode)
	}
	var graded struct {
		Card        review.Card        `json:"card"`
		Calculation review.Calculation `json:"calculation"`
	}
	if err := json.NewDecoder(okResp.Body).Decode(&graded); err != nil {
		t.Fatal(err)
	}
	if graded.Card.EaseFactor != 2.6 || graded.Card.Repetitions != 1 || graded.Card.IntervalDays != 1 {
		t.Fatalf("graded card = %+v", graded.Card)
	}
	if graded.Calculation.Formula == "" || graded.Card.LastCalculation == nil {
		t.Fatal("calculation trace is missing")
	}

	// The retest question is rule-generated and rotates by repetitions.
	retestResp, err := http.Get(srv.URL + "/v1/review/card-1/retest")
	if err != nil {
		t.Fatal(err)
	}
	defer retestResp.Body.Close()
	if retestResp.StatusCode != http.StatusOK {
		t.Fatalf("retest status = %d", retestResp.StatusCode)
	}
	var retest review.Retest
	if err := json.NewDecoder(retestResp.Body).Decode(&retest); err != nil {
		t.Fatal(err)
	}
	if retest.ModelCalled || retest.Question == "" {
		t.Fatalf("retest = %+v", retest)
	}

	// Session listing and a missing card.
	listResp, err := http.Get(srv.URL + "/v1/review?session_id=sess-1")
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", listResp.StatusCode)
	}
	var list struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 {
		t.Fatalf("list count = %d", list.Count)
	}

	missingResp, err := http.Get(srv.URL + "/v1/review/missing")
	if err != nil {
		t.Fatal(err)
	}
	defer missingResp.Body.Close()
	if missingResp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status = %d", missingResp.StatusCode)
	}
}

func TestReviewUnavailableWithoutService(t *testing.T) {
	store := memory.New()
	authSvc, token, _ := testAuthFor(t, store)
	srv := httptest.NewServer(withTestAuthorization(NewRouter(Deps{
		Auth:   authSvc,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}), token))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/v1/review/due")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body errorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "review_unavailable" {
		t.Fatalf("code = %q", body.Code)
	}
}
