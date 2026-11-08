// Command reportcompare runs the deterministic and autonomous report paths on
// one fixed interview session and prints the measured comparison. It exists
// only for the M20 dual-mode experiment; it is not part of the server.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/interview"
	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/agent/report"
	"github.com/waiting2050/ai-interview-agent/internal/agent/tools"
	"github.com/waiting2050/ai-interview-agent/internal/app"
	"github.com/waiting2050/ai-interview-agent/internal/domain/domainpack"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
	"github.com/waiting2050/ai-interview-agent/internal/platform/config"
	"github.com/waiting2050/ai-interview-agent/internal/store/memory"
)

type modeResult struct {
	Mode             string            `json:"mode"`
	ModelCalls       int               `json:"model_calls"`
	PromptTokens     int               `json:"prompt_tokens"`
	CompletionTokens int               `json:"completion_tokens"`
	DurationMS       int64             `json:"duration_ms"`
	ToolRounds       int               `json:"tool_rounds"`
	ToolCalls        int               `json:"tool_calls"`
	ToolOrder        []string          `json:"tool_order"`
	ToolDetails      []toolDetail      `json:"tool_details"`
	Degraded         bool              `json:"degraded"`
	DegradeReason    string            `json:"degrade_reason,omitempty"`
	Sections         int               `json:"sections"`
	SectionTitles    []string          `json:"section_titles"`
	Unauthorized     bool              `json:"unauthorized"`
	OverBudget       bool              `json:"over_budget"`
	Truncated        bool              `json:"truncated"`
	Attempts         int               `json:"attempts"`
	ValidationIssues []string          `json:"validation_issues,omitempty"`
	Events           []eventRow        `json:"events,omitempty"`
	SectionsText     map[string]string `json:"sections_text"`
	Report           json.RawMessage   `json:"report"`
}

type eventRow struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

type toolDetail struct {
	Tool        string `json:"tool"`
	Status      string `json:"status"`
	Truncated   bool   `json:"truncated"`
	Round       int    `json:"round,omitempty"`
	ResultBytes int    `json:"result_bytes"`
}

type countingClient struct {
	inner            llm.Client
	calls            int
	promptTokens     int
	completionTokens int
}

func (c *countingClient) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	resp, err := c.inner.Generate(ctx, req)
	c.calls++
	c.promptTokens += resp.PromptTokens
	c.completionTokens += resp.CompletionTokens
	return resp, err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "reportcompare:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := memory.New()

	heuristic := interview.NewHeuristicClient()
	svc := app.New(store, app.Options{
		Synchronous:       true,
		Logger:            logger,
		QuestionGenerator: interview.NewQuestionGenerator(heuristic, "fake-heuristic"),
		Evaluator:         interview.NewEvaluator(heuristic, "fake-heuristic"),
		ReportGenerator:   report.NewBuilder(heuristic, "fake-heuristic"),
		ReportTool:        app.ReportToolOptions{Mode: config.ReportToolDeterministic},
	})

	// 1. One deterministic, free interview session. It is created once and is
	// the fixed input for both report runs.
	sess, err := svc.CreateSession(ctx, app.CreateSessionInput{
		CandidateName: "张三",
		TargetRole:    "Go 后端开发",
		ResumeText:    "三年 Go 经验；负责订单服务与会话服务；做过检索、限流与幂等改造，熟悉 PostgreSQL、Redis、消息队列。",
		JDText:        "招聘 Go 后端：分布式系统、PostgreSQL、缓存、消息队列、可观测性。",
		Config:        session.Config{MaxPrimaryQuestions: 6, MaxFollowupsPerQuestion: 1},
	})
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	if _, err := svc.Start(ctx, app.StartCommand{SessionID: sess.ID, IdempotencyKey: "start"}); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	answers := []string{
		"我用消息表加定时对账保证最终一致性，重试是指数退避，压测验证过重复消费不产生脏数据。",
		"检索这块我做了 BM25 和向量的融合，中文用 bigram，评测集分原文型和口语化两类分别看 hit@5 和 MRR。",
		"限流用的是令牌桶，按用户和接口两个维度，超限返回 429，并记录指标。",
		"幂等是请求里带 idempotency key，落库唯一约束，冲突时返回第一次的结果。",
		"可观测性上我埋了耗时和错误码，P95 超过阈值会告警，日志带 trace id。",
		"缓存用 Redis，热点 key 加逻辑过期，避免击穿；缓存和库的一致性用延迟双删兜底。",
	}
	for i := 0; i < 24; i++ {
		current, err := svc.GetSession(ctx, sess.ID)
		if err != nil {
			return fmt.Errorf("reload session: %w", err)
		}
		if current.Status != session.StatusActive || current.PendingTurnID == "" {
			break
		}
		_, err = svc.Answer(ctx, app.AnswerCommand{
			SessionID: sess.ID, TurnID: current.PendingTurnID,
			Answer: answers[i%len(answers)], IdempotencyKey: fmt.Sprintf("a-%d", i),
		})
		if err != nil {
			return fmt.Errorf("answer %d: %w", i, err)
		}
	}
	if _, err := svc.Report(ctx, app.FinishCommand{SessionID: sess.ID}); err != nil {
		// A report may already exist from the last answer; either is fine.
		logger.Info("heuristic report not regenerated", "error", err)
	}
	done, err := svc.GetSession(ctx, sess.ID)
	if err != nil {
		return err
	}
	if done.Status != session.StatusCompleted {
		return fmt.Errorf("fixture session is %s, not completed", done.Status)
	}

	// 2. Real model, both report modes, same session and same evidence.
	realClient, info, err := llm.NewClientFromEnv(ctx, heuristic, logger)
	if err != nil {
		return fmt.Errorf("build model client: %w", err)
	}
	if info.Fallback {
		return fmt.Errorf("model is in fallback mode: %s", info.Reason)
	}

	pack := domainpack.Default().MustGet(done.Domain)
	opts := tools.Options{}
	if pack != nil {
		opts.Rubric = pack.EvaluationRubric()
	}
	q := tools.NewQuery(done.ID, store.Repos(), opts)
	bundle, err := q.Collect(ctx)
	if err != nil {
		return fmt.Errorf("collect evidence: %w", err)
	}

	detClient := &countingClient{inner: realClient}
	detBuilder := report.NewBuilder(detClient, info.Model)
	detStart := time.Now()
	detOut, detErr := detBuilder.Generate(ctx, bundle)
	detDuration := time.Since(detStart)
	if detErr != nil {
		return fmt.Errorf("deterministic report: %w", detErr)
	}

	autoClient := &countingClient{inner: realClient}
	timeout := 20 * time.Second
	if raw := strings.TrimSpace(os.Getenv("REPORT_COMPARE_TIMEOUT")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			timeout = d
		}
	}
	autoBuilder := report.NewBuilder(autoClient, info.Model).WithAutonomous(true, report.AutonomousConfig{
		MaxRounds: 6, MaxCalls: 12, Timeout: timeout,
	})
	// A fresh query keeps the autonomous budget independent; the evidence
	// bundle is exactly the same object the deterministic run used.
	autoQuery := tools.NewQuery(done.ID, store.Repos(), opts)
	if _, err := autoQuery.Collect(ctx); err != nil {
		return fmt.Errorf("autonomous collect: %w", err)
	}
	autoQuery.BeginSection("autonomous")
	var events []eventRow
	sink := func(ev report.Event) {
		events = append(events, eventRow{Type: ev.Type, Payload: ev.Payload})
	}
	autoStart := time.Now()
	autoOut, autoErr := autoBuilder.GenerateWithTools(ctx, bundle, autoQuery, sink)
	autoDuration := time.Since(autoStart)
	if autoErr != nil {
		return fmt.Errorf("autonomous report: %w", autoErr)
	}

	det := measure("deterministic", detClient, detDuration, detOut, nil)
	auto := measure("autonomous", autoClient, autoDuration, autoOut, events)
	if det.ModelCalls+auto.ModelCalls > 12 {
		fmt.Fprintf(os.Stderr, "call cap exceeded: det=%d auto=%d total=%d\n", det.ModelCalls, auto.ModelCalls, det.ModelCalls+auto.ModelCalls)
	}
	diff := map[string]bool{}
	for title, text := range det.SectionsText {
		if auto.SectionsText[title] != text {
			diff[title] = true
		}
	}
	extra := make([]string, 0, len(auto.ToolDetails))
	for _, d := range auto.ToolDetails {
		extra = append(extra, d.Tool)
	}

	summary := map[string]any{
		"session_id":          done.ID,
		"model":               info.Model,
		"provider":            info.Provider,
		"answers":             countTurns(store, done.ID),
		"autonomous_timeout":  timeout.String(),
		"different_sections":  diff,
		"autonomous_tool_seq": extra,
		"deterministic":       det,
		"autonomous":          auto,
	}
	body, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	outPath := filepath.Join("docs", "eval-results", "tool-mode-compare.json")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(outPath, body, 0o644); err != nil {
		return err
	}
	fmt.Printf("det calls=%d prompt=%d completion=%d ms=%d degraded=%v tools=%v\n",
		det.ModelCalls, det.PromptTokens, det.CompletionTokens, det.DurationMS, det.Degraded, det.ToolOrder)
	fmt.Printf("auto calls=%d prompt=%d completion=%d ms=%d degraded=%v reason=%s rounds=%d\n",
		auto.ModelCalls, auto.PromptTokens, auto.CompletionTokens, auto.DurationMS, auto.Degraded, auto.DegradeReason, auto.ToolRounds)
	fmt.Printf("auto tool seq=%v events=%d different sections=%v\n", extra, len(auto.Events), diff)
	fmt.Println("written:", outPath)
	return nil
}

func measure(mode string, c *countingClient, dur time.Duration, out report.Outcome, events []eventRow) modeResult {
	rep := out.Report
	sectionsText := map[string]string{}
	for _, s := range rep.Sections {
		sectionsText[s.Title] = s.Markdown
	}
	titles := make([]string, 0, len(rep.Sections))
	for _, s := range rep.Sections {
		titles = append(titles, s.Title)
	}
	details := make([]toolDetail, 0, len(rep.ToolCallsDetail))
	order := make([]string, 0, len(rep.ToolCallsDetail))
	unauthorized, overBudget, truncated := false, false, false
	for _, d := range rep.ToolCallsDetail {
		details = append(details, toolDetail{
			Tool: d.Tool, Status: d.Status, Truncated: d.Truncated,
			Round: d.Round, ResultBytes: d.ResultBytes,
		})
		order = append(order, d.Tool)
		if strings.Contains(d.Status, "denied") || d.Status == "denied" {
			unauthorized = true
		}
		if d.Truncated {
			truncated = true
		}
	}
	if rep.ToolDegradeReason == "max_calls" || rep.ToolDegradeReason == "budget_exceeded" {
		overBudget = true
	}
	raw, _ := json.Marshal(rep)
	return modeResult{
		Mode: mode, ModelCalls: c.calls,
		PromptTokens: c.promptTokens, CompletionTokens: c.completionTokens,
		DurationMS: dur.Milliseconds(), ToolRounds: rep.ToolRounds,
		ToolCalls: rep.ToolCalls, ToolOrder: order, ToolDetails: details,
		Degraded: rep.Degraded, DegradeReason: rep.ToolDegradeReason,
		Sections: len(rep.Sections), SectionTitles: titles,
		Unauthorized: unauthorized, OverBudget: overBudget, Truncated: truncated,
		Attempts: out.Attempts, ValidationIssues: out.ValidationIssues,
		Events: events, SectionsText: sectionsText, Report: raw,
	}
}

func countTurns(store *memory.Store, sessionID string) int {
	turns, err := store.Repos().Turns.ListBySession(context.Background(), sessionID)
	if err != nil {
		return 0
	}
	return len(turns)
}
