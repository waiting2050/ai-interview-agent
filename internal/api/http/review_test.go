package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/domain/review"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

type stubReviewEvaluator struct {
	result review.VariantEvaluation
	err    error
}

func (s stubReviewEvaluator) EvaluateVariant(context.Context, review.VariantEvaluationInput) (review.VariantEvaluation, error) {
	return s.result, s.err
}

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
	svc := review.New(store.Repos().Reviews, stubReviewEvaluator{result: review.VariantEvaluation{
		Score:         45,
		MissingPoints: []string{"没讲补偿"},
		ModelName:     "fake-heuristic",
		PromptVersion: "eval-test",
	}})
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
		Count           int `json:"count"`
		TotalDue        int `json:"total_due"`
		DailyLimit      int `json:"daily_limit"`
		DeferredCount   int `json:"deferred_count"`
		Remaining       int `json:"remaining"`
		TZOffsetMinutes int `json:"tz_offset_minutes"`
		Cards           []struct {
			review.Card
			Priority   string `json:"priority"`
			SortReason string `json:"sort_reason"`
		} `json:"cards"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&due); err != nil {
		t.Fatal(err)
	}
	if due.Count != 1 || due.TZOffsetMinutes != 480 || due.Cards[0].ID != "card-1" {
		t.Fatalf("due = %+v", due)
	}
	if due.TotalDue != 1 || due.DailyLimit != 20 || due.DeferredCount != 0 || due.Remaining != 19 {
		t.Fatalf("due counts = %+v", due)
	}
	if due.Cards[0].Priority != "normal" || due.Cards[0].SortReason == "" {
		t.Fatalf("ranking = %+v", due.Cards[0])
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

func TestReviewVariantObjectiveDoubleTrack(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	card := &review.Card{
		ID:           "card-v",
		SessionID:    "sess-v",
		TurnID:       "turn-v",
		Topic:        "Go 并发模型",
		Question:     "讲讲 Go 的并发模型",
		AnswerDigest: "只讲了 goroutine",
		KeyPoints:    []string{"调度", "channel", "context"},
		SourceScore:  61,
		KeyTopic:     true,
		EaseFactor:   review.DefaultEase,
		DueAt:        now,
		LastGrade:    -1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	srv, _ := newReviewRouter(t, now, []*review.Card{card})

	// The variant question is generated from syllabus expected points with no
	// model call, and stays stable across requests.
	first := getVariant(t, srv.URL, card.ID)
	second := getVariant(t, srv.URL, card.ID)
	if first.ModelCalled || len(first.ExpectedPoints) != 3 || first.Question == "" {
		t.Fatalf("variant = %+v", first)
	}
	if first.Question != second.Question || !reflect.DeepEqual(first.ExpectedPoints, second.ExpectedPoints) {
		t.Fatalf("variant is not deterministic:\nfirst=%+v\nsecond=%+v", first, second)
	}

	// Objective 45 => band 1, SM-2 grade 2. Self 5 is recorded but does not
	// drive the schedule; the gap is flagged and persisted.
	resp, err := http.Post(srv.URL+"/v1/review/card-v/variant/answer", "application/json",
		strings.NewReader(`{"answer":"只讲了一句话","self_grade":5}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("answer status = %d", resp.StatusCode)
	}
	var out struct {
		Card        review.Card            `json:"card"`
		Calculation *review.Calculation    `json:"calculation"`
		Objective   review.ObjectiveResult `json:"objective"`
		Divergence  *review.Divergence     `json:"divergence"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Objective.Score != 45 || out.Objective.Band != 1 || out.Objective.Grade != 2 {
		t.Fatalf("objective = %+v", out.Objective)
	}
	if out.Card.LastGrade != 2 || out.Calculation == nil || out.Calculation.Grade != 2 {
		t.Fatalf("objective grade must drive SM-2: %+v", out.Card)
	}
	if out.Card.LastSelfGrade == nil || *out.Card.LastSelfGrade != 5 || out.Card.LastDivergence == nil || *out.Card.LastDivergence != 3 {
		t.Fatalf("self track = %+v", out.Card)
	}
	if out.Divergence == nil || !out.Divergence.SuspectedOverestimate {
		t.Fatalf("divergence = %+v", out.Divergence)
	}

	// The persisted card exposes the objective and divergence fields.
	cardResp, err := http.Get(srv.URL + "/v1/review/card-v")
	if err != nil {
		t.Fatal(err)
	}
	defer cardResp.Body.Close()
	var stored review.Card
	if err := json.NewDecoder(cardResp.Body).Decode(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.LastObjectiveScore == nil || *stored.LastObjectiveScore != 45 ||
		stored.LastSelfGrade == nil || *stored.LastSelfGrade != 5 {
		t.Fatalf("stored card = %+v", stored)
	}
}

func getVariant(t *testing.T, baseURL, cardID string) review.Variant {
	t.Helper()
	resp, err := http.Get(baseURL + "/v1/review/" + cardID + "/variant")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("variant status = %d", resp.StatusCode)
	}
	var variant review.Variant
	if err := json.NewDecoder(resp.Body).Decode(&variant); err != nil {
		t.Fatal(err)
	}
	return variant
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
