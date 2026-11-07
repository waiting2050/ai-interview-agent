package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/eval"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
	"github.com/waiting2050/ai-interview-agent/internal/platform/obs"
)

func TestEvalPanelRendersMissesByDomainAndCompares(t *testing.T) {
	dir := t.TempDir()
	writeEvalReport(t, dir, time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC), "heuristic", "fake/heuristic", []eval.CaseResult{
		{
			CaseID: "go-01", Domain: eval.DomainGoBackend, Type: eval.TypeHigh, Topic: "GMP",
			EvidenceAnchor: "本地 runq", ExpectedBand: 5, ActualBand: 1, TotalScore: 40,
			Deviations: []eval.Deviation{{Dimension: evaluation.DimensionDepth, Expected: 5, Actual: 2, Delta: -3}},
			Notes:      "期望高分，实际按低档处理",
		},
		{
			CaseID: "ai-01", Domain: eval.DomainAIAgent, Type: eval.TypeMid, Topic: "RAG",
			EvidenceAnchor: "召回率", ExpectedBand: 3, ActualBand: 3, TotalScore: 60,
		},
	})
	writeEvalReport(t, dir, time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), "openai", "deepseek-flash", []eval.CaseResult{
		{CaseID: "go-01", Domain: eval.DomainGoBackend, Type: eval.TypeHigh, Topic: "GMP",
			EvidenceAnchor: "本地 runq", ExpectedBand: 5, ActualBand: 5, TotalScore: 85},
		{CaseID: "ai-01", Domain: eval.DomainAIAgent, Type: eval.TypeMid, Topic: "RAG",
			EvidenceAnchor: "召回率", ExpectedBand: 3, ActualBand: 1, TotalScore: 35},
	})

	srv := httptest.NewServer(NewRouter(Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		EvalResultsDir: dir,
		Metrics:        obs.NewMetrics(),
	}))
	defer srv.Close()

	body, status := getBody(t, srv.URL+"/eval")
	if status != http.StatusOK {
		t.Fatalf("GET /eval = %d", status)
	}
	for _, want := range []string{"最近的结果文件", "go-backend", "ai-agent", "期望档", "逐条失准 case", "A/B 对比"} {
		if !strings.Contains(body, want) {
			t.Fatalf("panel missing %q", want)
		}
	}

	// Selecting the heuristic file must show the miss with both bands, the
	// dimension deviation and the anchor, not just a global score.
	base := "eval-heuristic-fake-heuristic-eval-v2-20261101T100000Z.json"
	cand := "eval-openai-deepseek-flash-eval-v2-20261102T100000Z.json"
	body, status = getBody(t, srv.URL+"/eval?file="+base)
	if status != http.StatusOK {
		t.Fatalf("select = %d", status)
	}
	for _, want := range []string{"go-01", "本地 runq", "depth", "期望高分"} {
		if !strings.Contains(body, want) {
			t.Fatalf("selected report missing %q", want)
		}
	}

	body, status = getBody(t, srv.URL+"/eval?file="+cand+"&base="+base+"&candidate="+cand)
	if status != http.StatusOK {
		t.Fatalf("compare = %d", status)
	}
	for _, want := range []string{"improved", "regressed", "band_hit_rate", "判定翻转"} {
		if !strings.Contains(body, want) {
			t.Fatalf("compare missing %q", want)
		}
	}
}

func getBody(t *testing.T, url string) (string, int) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(raw), resp.StatusCode
}

func writeEvalReport(t *testing.T, dir string, at time.Time, provider, model string, cases []eval.CaseResult) string {
	t.Helper()
	domains := map[string]int{}
	for _, c := range cases {
		domains[c.Domain]++
	}
	report := eval.Report{
		GeneratedAt:        at,
		Provider:           provider,
		Model:              model,
		PromptVersion:      "eval-v2",
		Metrics:            eval.Metrics{Total: len(cases), Evaluated: len(cases)},
		Thresholds:         eval.DefaultThresholds(),
		DomainDistribution: domains,
		Cases:              cases,
	}
	path, err := eval.WriteReport(report, dir)
	if err != nil {
		t.Fatalf("write report: %v", err)
	}
	return path
}
