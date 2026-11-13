// Command ablation compares three scoring arms on the same regression subset:
// single LLM, LLM plus BM25 retrieval, and the production chain (structured
// rubric, schema/evidence validation, deterministic score mapping).
//
// It is a measurement tool, not part of the service. The default provider is
// whatever MODEL_PROVIDER resolves to; for a zero-cost run point it at a local
// OpenAI-compatible endpoint such as Ollama.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/eval"
	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/agent/knowledge"
	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/domain/domainpack"
	"github.com/waiting2050/ai-interview-agent/internal/domain/evaluation"
)

const (
	defaultSet = "internal/agent/eval/testdata/regression.json"
	defaultOut = "docs/eval-results/ablation-m20.json"
	armSingle  = "single-llm"
	armRAG     = "llm+rag"
	armFull    = "full-rules+evidence"
)

var defaultCases = []string{
	"high-01-gmp", "high-02-mysql-slow", "high-05-kafka-idempotent", "high-09-escape",
	"ai-high-01-rag-pipeline", "ai-high-03-hybrid", "mid-01-gc", "ai-mid-03-memory",
	"low-01-channel", "ai-low-01-vector-db",
}

type caseResult struct {
	CaseID        string  `json:"case_id"`
	Type          string  `json:"type"`
	ExpectedBand  int     `json:"expected_band"`
	ExpectedTotal float64 `json:"expected_total"`
	TotalScore    float64 `json:"total_score"`
	Band          int     `json:"band"`
	PromptTokens  int     `json:"prompt_tokens"`
	Completion    int     `json:"completion_tokens"`
	LatencyMS     int64   `json:"latency_ms"`
	Degraded      bool    `json:"degraded"`
	Error         string  `json:"error,omitempty"`
}

type armReport struct {
	Arm                 string       `json:"arm"`
	Cases               []caseResult `json:"cases"`
	BandHits            int          `json:"band_hits"`
	Evaluated           int          `json:"evaluated"`
	Errors              int          `json:"errors"`
	Degraded            int          `json:"degraded"`
	BandHitRate         float64      `json:"band_hit_rate"`
	MAE                 float64      `json:"mae"`
	AvgPromptTokens     float64      `json:"avg_prompt_tokens"`
	AvgCompletionTokens float64      `json:"avg_completion_tokens"`
	AvgLatencyMS        float64      `json:"avg_latency_ms"`
}

type report struct {
	GeneratedAt     string      `json:"generated_at"`
	Provider        string      `json:"provider"`
	Model           string      `json:"model"`
	BaseURL         string      `json:"base_url"`
	DatasetPath     string      `json:"dataset_path"`
	DatasetSHA256   string      `json:"dataset_sha256"`
	Cases           int         `json:"cases"`
	DirectMaxTok    int         `json:"direct_max_tokens"`
	TotalCalls      int         `json:"total_calls"`
	TotalPrompt     int         `json:"total_prompt_tokens"`
	TotalCompletion int         `json:"total_completion_tokens"`
	CostUSD         float64     `json:"cost_usd"`
	CostCNY         float64     `json:"cost_cny"`
	PriceNote       string      `json:"price_note"`
	Arms            []armReport `json:"arms"`
	Note            string      `json:"note"`
}

type scoreReply struct {
	TotalScore *float64 `json:"total_score"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ablation: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	setPath := flag.String("set", defaultSet, "regression set path")
	outPath := flag.String("out", defaultOut, "result JSON path")
	casesFlag := flag.String("cases", "all", "comma-separated case ids, or all")
	armsFlag := flag.String("arms", "all", "comma-separated arms: "+armSingle+","+armRAG+","+armFull+", or all")
	directMaxTok := flag.Int("direct-max-tokens", 400, "MaxTokens for the two direct arms")
	priceIn := flag.Float64("price-in-usd", 0.15, "USD per 1M prompt tokens")
	priceOut := flag.Float64("price-out-usd", 0.60, "USD per 1M completion tokens")
	usdCNY := flag.Float64("usd-cny", 7.2, "USD to CNY rate for reporting")
	flag.Parse()

	ds, err := eval.LoadDataset(*setPath)
	if err != nil {
		return err
	}
	selected := ds.Cases
	if *casesFlag != "all" {
		want := map[string]bool{}
		for _, id := range strings.Split(*casesFlag, ",") {
			if id = strings.TrimSpace(id); id != "" {
				want[id] = true
			}
		}
		selected = make([]eval.Case, 0, len(want))
		for _, c := range ds.Cases {
			if want[c.CaseID] {
				selected = append(selected, c)
			}
		}
		if len(selected) != len(want) {
			return fmt.Errorf("selected %d cases but requested %d", len(selected), len(want))
		}
	}

	corpus, err := knowledge.BuildCorpus(knowledge.CorpusSources{
		Packs:    domainpack.Default().List(),
		DocsRoot: "docs",
	})
	if err != nil {
		return fmt.Errorf("build corpus: %w", err)
	}
	index := knowledge.NewBM25Index(corpus)

	cfg, err := llm.LoadProviderConfig()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	client, info, err := llm.NewClient(context.Background(), cfg, interview.NewHeuristicClient(), logger)
	if err != nil {
		return err
	}
	if info.Fallback {
		return fmt.Errorf("provider %s is not usable: %s", info.Provider, info.Reason)
	}

	arms := map[string]*armReport{
		armSingle: {Arm: armSingle},
		armRAG:    {Arm: armRAG},
		armFull:   {Arm: armFull},
	}
	order := []string{armSingle, armRAG, armFull}
	if *armsFlag != "all" {
		want := map[string]bool{}
		for _, name := range strings.Split(*armsFlag, ",") {
			if name = strings.TrimSpace(name); name != "" {
				if _, ok := arms[name]; !ok {
					return fmt.Errorf("unknown arm %q", name)
				}
				want[name] = true
			}
		}
		order = order[:0]
		for _, name := range []string{armSingle, armRAG, armFull} {
			if want[name] {
				order = append(order, name)
			}
		}
	}

	totalCalls := 0
	for _, c := range selected {
		rubric := domainpack.Default().MustGet(c.Domain).EvaluationRubric()
		expectedScore := evaluation.ComputeScoreWith(c.ExpectedScores, nil, nil, rubric)

		refs := ""
		if hits := index.Search(c.Topic+" "+c.Question, 3); len(hits) > 0 {
			var b strings.Builder
			for _, hit := range hits {
				for _, chunk := range corpus {
					if chunk.ID == hit.ChunkID {
						fmt.Fprintf(&b, "- %s：%s\n", chunk.Title, truncate(chunk.Content, 600))
						break
					}
				}
			}
			refs = b.String()
		}

		for _, arm := range order {
			res := caseResult{CaseID: c.CaseID, Type: c.Type, ExpectedBand: c.ExpectedBand, ExpectedTotal: expectedScore.Total}
			start := time.Now()
			switch arm {
			case armSingle:
				res = scoreDirect(context.Background(), client, c, "", *directMaxTok, res)
			case armRAG:
				res = scoreDirect(context.Background(), client, c, refs, *directMaxTok, res)
			case armFull:
				res = scoreFull(context.Background(), client, info.Model, c, rubric, res)
			}
			res.LatencyMS = time.Since(start).Milliseconds()
			totalCalls++
			arms[arm].Cases = append(arms[arm].Cases, res)
		}
		fmt.Fprintf(os.Stderr, "case %s done (%d/%d)\n", c.CaseID, totalCalls/len(order), len(selected))
	}

	for _, arm := range order {
		a := arms[arm]
		a.MAE = 0
		for _, c := range a.Cases {
			a.Evaluated++
			if c.Error != "" {
				a.Errors++
				continue
			}
			if c.Degraded {
				a.Degraded++
			}
			if c.Band == c.ExpectedBand {
				a.BandHits++
			}
			a.MAE += abs(c.TotalScore - c.ExpectedTotal)
			a.AvgPromptTokens += float64(c.PromptTokens)
			a.AvgCompletionTokens += float64(c.Completion)
			a.AvgLatencyMS += float64(c.LatencyMS)
		}
		if n := float64(a.Evaluated - a.Errors); n > 0 {
			a.BandHitRate = float64(a.BandHits) / n
			a.MAE /= n
			a.AvgPromptTokens /= n
			a.AvgCompletionTokens /= n
			a.AvgLatencyMS /= n
		}
	}

	digest, err := eval.FileDigest(*setPath)
	if err != nil {
		return err
	}
	totalPrompt, totalCompletion := 0, 0
	for _, arm := range order {
		for _, c := range arms[arm].Cases {
			totalPrompt += c.PromptTokens
			totalCompletion += c.Completion
		}
	}
	costUSD := float64(totalPrompt)/1e6*(*priceIn) + float64(totalCompletion)/1e6*(*priceOut)
	costCNY := costUSD * (*usdCNY)
	out := report{
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
		Provider:        info.Provider,
		Model:           info.Model,
		BaseURL:         info.BaseURL,
		DatasetPath:     *setPath,
		DatasetSHA256:   digest,
		Cases:           len(selected),
		DirectMaxTok:    *directMaxTok,
		TotalCalls:      totalCalls,
		TotalPrompt:     totalPrompt,
		TotalCompletion: totalCompletion,
		CostUSD:         costUSD,
		CostCNY:         costCNY,
		PriceNote: fmt.Sprintf("DeepSeek 官方定价（https://api-docs.deepseek.com/quick_start/pricing）：输入 cache-miss $%.2f/M、输出 $%.2f/M；2026-11-08 为周日全时段 off-peak；按 1 USD=%.1f CNY 折算。",
			*priceIn, *priceOut, *usdCNY),
		Note: fmt.Sprintf("完整回归集、同一模型、temperature=0（完整臂沿用生产 evaluator 的 0.1）；单次 LLM 与 LLM+RAG 直接给总分（MaxTokens=%d），band 由统一阈值映射；完整臂走维度分→ComputeScore→Band 并做 schema/证据校验（MaxTokens=2000）。", *directMaxTok),
	}
	for _, arm := range order {
		out.Arms = append(out.Arms, *arms[arm])
	}
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*outPath, append(raw, '\n'), 0o644); err != nil {
		return err
	}

	fmt.Printf("provider=%s model=%s cases=%d calls=%d\n", info.Provider, info.Model, len(selected), totalCalls)
	fmt.Printf("%-22s %8s %8s %8s %10s %10s %8s\n", "arm", "band", "mae", "errors", "prompt", "completion", "latency_ms")
	for _, a := range out.Arms {
		fmt.Printf("%-22s %7.1f%% %8.2f %8d %10.1f %10.1f %8.0f\n",
			a.Arm, a.BandHitRate*100, a.MAE, a.Errors, a.AvgPromptTokens, a.AvgCompletionTokens, a.AvgLatencyMS)
	}
	fmt.Printf("\ntotal: prompt=%d completion=%d cost=%.4f USD / %.4f CNY\n",
		totalPrompt, totalCompletion, costUSD, costCNY)
	fmt.Printf("result_file: %s\n", *outPath)
	return nil
}

func scoreDirect(ctx context.Context, client llm.Client, c eval.Case, refs string, maxTokens int, res caseResult) caseResult {
	user := fmt.Sprintf("题目主题：%s\n题目：%s\n\n候选人回答：\n%s", c.Topic, c.Question, truncate(c.Answer, 2000))
	if refs != "" {
		user += "\n\n<reference>\n" + refs + "\n</reference>"
	}
	resp, err := client.Generate(ctx, llm.Request{
		System:      directSystem,
		User:        user,
		Temperature: 0,
		MaxTokens:   maxTokens,
	})
	res.PromptTokens += resp.PromptTokens
	res.Completion += resp.CompletionTokens
	if err != nil {
		res.Error = err.Error()
		return res
	}
	var reply scoreReply
	if err := decodeJSON(resp.Text, &reply); err != nil || reply.TotalScore == nil {
		res.Error = fmt.Sprintf("invalid score JSON: %v", err)
		return res
	}
	res.TotalScore = *reply.TotalScore
	res.Band = evaluation.Band(res.TotalScore)
	res.Degraded = false
	return res
}

func scoreFull(ctx context.Context, client llm.Client, model string, c eval.Case, rubric evaluation.Rubric, res caseResult) caseResult {
	ev := interview.NewEvaluator(client, model)
	out, err := ev.Evaluate(ctx, interview.EvaluateInput{
		Topic:          c.Topic,
		Question:       c.Question,
		Answer:         c.Answer,
		TargetRole:     targetRole(c.Domain),
		Difficulty:     c.Difficulty,
		ExpectedPoints: c.ExpectedPoints,
		Rubric:         rubric,
	})
	res.PromptTokens += out.PromptTokens
	res.Completion += out.CompletionTokens
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if out.Degraded {
		res.Degraded = true
		res.Error = out.ErrorCode
		return res
	}
	score := evaluation.ComputeScoreWith(out.Result.Scores, out.Result.Bonus, out.Result.Penalty, rubric)
	res.TotalScore = score.Total
	res.Band = evaluation.Band(score.Total)
	return res
}

const directSystem = `你是严格的技术面试评分员。只读题目与候选人回答，按 0-100 给出总分，并只输出一个 JSON 对象：{"total_score": <数字>}。不要输出解释、Markdown 或多个 JSON。分数锚点：90-100 机制、边界、取舍、验证齐备；80-89 明显高于合格；65-79 基本合格但缺关键项；50-64 方向对但浅；0-49 错误、答非所问或空泛。`

func decodeJSON(text string, out interface{}) error {
	text = strings.TrimSpace(text)
	if start, end := strings.Index(text, "{"), strings.LastIndex(text, "}"); start >= 0 && end > start {
		text = text[start : end+1]
	}
	return json.Unmarshal([]byte(text), out)
}

func targetRole(domain string) string {
	if domain == "ai-agent" {
		return "AI Agent 工程师"
	}
	return "Go 后端开发"
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
