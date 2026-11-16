package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type predictionResponse struct {
	Domain      string `json:"domain"`
	Generator   string `json:"generator"`
	ModelCalled bool   `json:"model_called"`
	Degraded    bool   `json:"degraded"`
	Questions   []struct {
		Topic          string   `json:"topic"`
		Question       string   `json:"question"`
		Objective      string   `json:"objective"`
		ExpectedPoints []string `json:"expected_points"`
		Degraded       bool     `json:"degraded"`
	} `json:"questions"`
}

func postPrediction(t *testing.T, srvURL, body string) (*http.Response, predictionResponse) {
	t.Helper()
	resp, err := http.Post(srvURL+"/v1/predictions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var out predictionResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
	}
	return resp, out
}

func TestPredictionReturnsDeterministicQuestions(t *testing.T) {
	srv, _, _ := newTestRouter(t)
	body := `{"target_role":"Go 后端开发工程师","domain":"go-backend","count":3,
		"resume_text":"3 年 Go 后端经验，熟悉 Redis、MySQL，做过订单履约系统，负责缓存链路。"}`

	resp, first := postPrediction(t, srv.URL, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if first.Domain != "go-backend" || first.Generator != "fake-heuristic" {
		t.Fatalf("domain = %q, generator = %q", first.Domain, first.Generator)
	}
	if !first.ModelCalled {
		t.Fatal("heuristic client still counts as a generator call")
	}
	if first.Degraded {
		t.Fatal("heuristic client must not be reported as degraded")
	}
	if len(first.Questions) != 3 {
		t.Fatalf("questions = %d, want 3", len(first.Questions))
	}
	for i, question := range first.Questions {
		if question.Topic == "" || question.Question == "" {
			t.Fatalf("question %d is empty: %+v", i, question)
		}
		if len([]rune(question.Question)) < 15 {
			t.Fatalf("question %d is too short: %q", i, question.Question)
		}
	}

	resp2, second := postPrediction(t, srv.URL, body)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d", resp2.StatusCode)
	}
	for i := range first.Questions {
		if first.Questions[i].Question != second.Questions[i].Question {
			t.Fatalf("prediction is not deterministic at %d", i)
		}
	}
}

func TestPredictionValidation(t *testing.T) {
	srv, _, _ := newTestRouter(t)

	resp, err := http.Post(srv.URL+"/v1/predictions", "application/json",
		strings.NewReader(`{"target_role":"Go 后端"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body errorBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "invalid_argument" {
		t.Fatalf("code = %q", body.Code)
	}

	resp2, err := http.Post(srv.URL+"/v1/predictions", "application/json",
		strings.NewReader(`{"resume_text":"3 年 Go 经验"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing role status = %d", resp2.StatusCode)
	}
}
