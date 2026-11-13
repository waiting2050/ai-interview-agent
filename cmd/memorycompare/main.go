// Command memorycompare runs one minimal multi-turn check: a fact planted in
// turn 2 is recalled after turn 10 with layered memory on and off on the same
// model at temperature 0. It records raw prompts, raw outputs and token usage.
//
// It is a measurement tool, not part of the service. Point it at any
// OpenAI-compatible provider through the usual MODEL_PROVIDER variables.
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

	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/agent/memory"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
)

const (
	defaultOut   = "docs/eval-results/memory-gap.json"
	factTurn     = 2
	checkTurn    = 10
	recallSystem = "你是资深技术面试官。你正在做多轮一致性检查：只根据 user 消息中提供的 <memory> 内容回答，" +
		"不要使用未提供的推测；<memory> 中没有相关信息时 answer 直接写「不知道」。回答控制在 60 字以内。" +
		"只输出一个 JSON 对象：{\"answer\":\"...\"}，不要输出 Markdown 或多余文字。"
	recallQuestion = "我在本场面试第 2 题里最终选择了哪种任务队列存储方案？为什么放弃另一种？"
)

var topics = []string{
	"Go 垃圾回收",
	"任务队列存储选型",
	"Kafka 幂等与重复消费",
	"MySQL 索引最左前缀",
	"Redis 缓存一致性",
	"分库分表路由",
	"服务熔断与降级",
	"RAG 检索链路",
	"向量数据库选型",
	"灰度发布与回滚",
}

var questions = []string{
	"线上服务 GC 停顿升高，你会如何定位并调整？",
	"任务队列的持久化存储你会选什么，为什么？",
	"Kafka 消费端如何保证幂等？",
	"联合索引 (a,b,c) 在哪些查询下会失效？",
	"缓存与数据库不一致，你会怎么处理？",
	"分库分表后跨分片查询怎么解决？",
	"下游接口超时，熔断与降级如何设计？",
	"RAG 检索召回率低，你会从哪些环节排查？",
	"向量数据库选型要考虑哪些因素？",
	"灰度发布出问题，如何快速回滚？",
}

var answers = []string{
	"先看 GC 频率和堆增长曲线，用 pprof 定位分配热点。若对象生命周期短，优先减少逃逸和对象复用；再把 GOGC 从默认值往上调，观察 P99 是否下降，同时用一周的监控对比基线。",
	"我们最终选了 PostgreSQL 而不是 MySQL，因为需要 SKIP LOCKED 做任务队列的并发领取；Redis 只做锁和限流，不承担正确性。MySQL 的行锁在长事务下容易互相阻塞，而 SKIP LOCKED 能让 worker 跳过被锁行。代价是 PostgreSQL 写入吞吐低于 MySQL，所以队列表按状态分区并定期归档。",
	"消费端用业务唯一键做幂等表，先插入幂等记录再处理业务，重复消息直接丢弃。边界是幂等表要设置过期时间，否则会无限增长；失败时依赖事务回滚保证幂等记录与业务一致。",
	"最左前缀要求查询从 a 开始连续使用索引列；a 上做范围查询会让 b、c 失效，b 单独查询也用不上。我试过拆成单列索引，回表次数多了三倍，所以保留联合索引，代价是写入多维护一个索引。",
	"先更新数据库再删缓存，并用延迟双删兜底。边界是删除失败会短暂不一致，所以加版本号校验；最终一致性窗口控制在秒级，监控缓存命中率作为验证。",
	"跨分片查询用冗余表或异步汇总，避免广播查询。路由键按用户 id 取模，代价是运营后台需要额外的聚合服务，回滚时保留双写开关。",
	"熔断按错误率和慢调用比例触发，半开状态放少量流量探测。降级要准备静态兜底数据，边界是熔断期间不能把流量直接打到数据库，否则会雪崩。",
	"先看切块是否把考点拆散，再看 BM25 与向量的融合权重。我们做过对照：只用 BM25 时专业名词命中高，但同义表述漏召；加向量后召回提升，代价是延迟增加约 80ms。",
	"选型看维度、过滤能力、持久化和运维成本。我们对比过 pgvector 与专用向量库，数据量在百万级时 pgvector 足够，且能和业务表 join；代价是超大规模下索引构建更慢。",
	"灰度按流量比例和用户白名单放量，回滚用保留的旧版本镜像加配置开关。边界是数据库迁移不可回滚，所以迁移必须双写兼容；验证看错误率和核心接口 P99。",
}

// memStore is the in-memory equivalent of the Postgres summary store. The
// experiment only needs one session and no persistence across processes.
type memStore struct{ states map[string]memory.State }

func newMemStore() *memStore { return &memStore{states: map[string]memory.State{}} }

func (s *memStore) Load(_ context.Context, sessionID string) (memory.State, bool, error) {
	state, ok := s.states[sessionID]
	return state, ok, nil
}

func (s *memStore) Save(_ context.Context, sessionID string, state memory.State) error {
	s.states[sessionID] = state
	return nil
}

type updateEvent struct {
	AfterAnswered    int    `json:"after_answered"`
	Fired            bool   `json:"fired"`
	CoveredTurns     int    `json:"covered_turns"`
	Degraded         bool   `json:"degraded"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Summary          string `json:"summary"`
	Error            string `json:"error,omitempty"`
}

type recallResult struct {
	WithMemory       bool   `json:"with_memory"`
	UserPrompt       string `json:"user_prompt"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	LatencyMS        int64  `json:"latency_ms"`
	Output           string `json:"output"`
	Cited            bool   `json:"cited"`
	MentionsPostgres bool   `json:"mentions_postgresql"`
	MentionsSkipLock bool   `json:"mentions_skip_locked"`
	MentionsMySQL    bool   `json:"mentions_mysql"`
	SaysUnknown      bool   `json:"says_unknown"`
	Error            string `json:"error,omitempty"`
}

type report struct {
	GeneratedAt         string        `json:"generated_at"`
	Provider            string        `json:"provider"`
	Model               string        `json:"model"`
	BaseURL             string        `json:"base_url"`
	SessionID           string        `json:"session_id"`
	Turns               int           `json:"turns"`
	FactTurn            int           `json:"fact_turn"`
	CheckTurn           int           `json:"check_turn"`
	Fact                string        `json:"fact"`
	UpdateEvents        []updateEvent `json:"update_events"`
	MemorySummary       string        `json:"memory_summary"`
	MemoryDegraded      bool          `json:"memory_degraded"`
	MemoryPromptTokens  int           `json:"memory_prompt_tokens"`
	MemoryCompletionTok int           `json:"memory_completion_tokens"`
	RecallQuestion      string        `json:"recall_question"`
	NoMemory            recallResult  `json:"no_memory"`
	WithMemory          recallResult  `json:"with_memory"`
	PromptTokenDelta    int           `json:"prompt_token_delta"`
	CitationDelta       int           `json:"citation_delta"`
	CostUSD             float64       `json:"cost_usd"`
	CostCNY             float64       `json:"cost_cny"`
	PriceNote           string        `json:"price_note"`
	Note                string        `json:"note"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "memorycompare: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	outPath := flag.String("out", defaultOut, "result JSON path")
	priceIn := flag.Float64("price-in-usd", 0.15, "USD per 1M prompt tokens")
	priceOut := flag.Float64("price-out-usd", 0.60, "USD per 1M completion tokens")
	usdCNY := flag.Float64("usd-cny", 7.2, "USD to CNY rate for reporting")
	flag.Parse()

	cfg, err := llm.LoadProviderConfig()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	client, info, err := llm.NewClient(context.Background(), cfg, nil, logger)
	if err != nil {
		return err
	}
	if info.Fallback {
		return fmt.Errorf("provider %s is not usable: %s", info.Provider, info.Reason)
	}

	ctx := context.Background()
	sessionID := "memory-gap-demo"
	store := newMemStore()
	builder := memory.NewBuilder(store, client, memory.Options{
		Model:   info.Model,
		ModelOK: true,
		Logger:  logger,
	})

	turns := make([]session.Turn, 0, len(questions))
	events := make([]updateEvent, 0, len(questions))
	for i := 0; i < len(questions); i++ {
		seq := i + 1
		turns = append(turns, session.Turn{
			ID:         fmt.Sprintf("turn-%d", seq),
			SessionID:  sessionID,
			Seq:        seq,
			Kind:       session.TurnKindPrimary,
			Topic:      topics[i],
			Question:   questions[i],
			Status:     session.TurnStatusAnswered,
			AnswerText: answers[i],
		})
		state, fired, err := builder.Update(ctx, sessionID, turns)
		ev := updateEvent{AfterAnswered: seq, Fired: fired}
		if err != nil {
			ev.Error = err.Error()
			events = append(events, ev)
			continue
		}
		if fired {
			ev.CoveredTurns = state.CoveredTurns
			ev.Degraded = state.Degraded
			ev.PromptTokens = state.PromptTokens
			ev.CompletionTokens = state.CompletionTokens
			ev.Summary = state.Summary
		}
		events = append(events, ev)
	}

	memPrompt := builder.Context(ctx, sessionID, turns)
	state, ok, err := store.Load(ctx, sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("memory summary was never produced; cannot compare")
	}

	noMemory := ask(ctx, client, renderRecall(memPrompt, false), false)
	withMemory := ask(ctx, client, renderRecall(memPrompt, true), true)

	totalPrompt := state.PromptTokens + noMemory.PromptTokens + withMemory.PromptTokens
	totalCompletion := state.CompletionTokens + noMemory.CompletionTokens + withMemory.CompletionTokens
	costUSD := float64(totalPrompt)/1e6*(*priceIn) + float64(totalCompletion)/1e6*(*priceOut)

	out := report{
		GeneratedAt:         time.Now().UTC().Format(time.RFC3339),
		Provider:            info.Provider,
		Model:               info.Model,
		BaseURL:             info.BaseURL,
		SessionID:           sessionID,
		Turns:               len(turns),
		FactTurn:            factTurn,
		CheckTurn:           checkTurn,
		Fact:                "第 2 题答：选 PostgreSQL 而非 MySQL，因为需要 SKIP LOCKED 做任务队列；Redis 只做锁和限流，不承担正确性。",
		UpdateEvents:        events,
		MemorySummary:       state.Summary,
		MemoryDegraded:      state.Degraded,
		MemoryPromptTokens:  state.PromptTokens,
		MemoryCompletionTok: state.CompletionTokens,
		RecallQuestion:      recallQuestion,
		NoMemory:            noMemory,
		WithMemory:          withMemory,
		PromptTokenDelta:    withMemory.PromptTokens - noMemory.PromptTokens,
		CitationDelta:       boolToInt(withMemory.Cited) - boolToInt(noMemory.Cited),
		CostUSD:             costUSD,
		CostCNY:             costUSD * (*usdCNY),
		PriceNote: fmt.Sprintf("DeepSeek 官方定价（https://api-docs.deepseek.com/quick_start/pricing）：输入 cache-miss $%.2f/M、输出 $%.2f/M；2026-11-08 为周日全时段 off-peak；按 1 USD=%.1f CNY 折算。",
			*priceIn, *priceOut, *usdCNY),
		Note: "同一会话、同一模型、temperature=0；第 2 题埋事实，第 10 题后提问。开记忆=摘要层（覆盖第 1-2 题）+ 最近 4 轮原文；关记忆=两者都不注入。引用正确判定：输出含 PostgreSQL，且含 SKIP LOCKED 或「队列」。",
	}

	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*outPath, append(raw, '\n'), 0o644); err != nil {
		return err
	}

	fmt.Printf("provider=%s model=%s turns=%d fact_turn=%d check_turn=%d\n",
		info.Provider, info.Model, len(turns), factTurn, checkTurn)
	fmt.Printf("summary_fired=%v covered=%d degraded=%v summary_tokens=%d/%d\n",
		summaryFired(events), state.CoveredTurns, state.Degraded, state.PromptTokens, state.CompletionTokens)
	fmt.Printf("%-14s %6s %8s %8s %8s\n", "arm", "cited", "prompt", "completion", "latency_ms")
	for _, r := range []recallResult{noMemory, withMemory} {
		fmt.Printf("%-14s %6v %8d %8d %8d\n", armName(r.WithMemory), r.Cited, r.PromptTokens, r.CompletionTokens, r.LatencyMS)
	}
	fmt.Printf("prompt_token_delta=%d citation_delta=%d cost=%.4f USD / %.4f CNY\n",
		out.PromptTokenDelta, out.CitationDelta, costUSD, costUSD*(*usdCNY))
	fmt.Printf("result_file: %s\n", *outPath)
	return nil
}

func ask(ctx context.Context, client llm.Client, user string, withMemory bool) recallResult {
	res := recallResult{WithMemory: withMemory, UserPrompt: user}
	start := time.Now()
	resp, err := client.Generate(ctx, llm.Request{
		System:      recallSystem,
		User:        user,
		Temperature: 0,
		MaxTokens:   300,
	})
	res.LatencyMS = time.Since(start).Milliseconds()
	res.PromptTokens = resp.PromptTokens
	res.CompletionTokens = resp.CompletionTokens
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Output = resp.Text
	judge(&res)
	return res
}

func judge(res *recallResult) {
	lower := strings.ToLower(res.Output)
	res.MentionsPostgres = strings.Contains(lower, "postgresql")
	res.MentionsSkipLock = strings.Contains(lower, "skip locked") || strings.Contains(lower, "队列")
	res.MentionsMySQL = strings.Contains(lower, "mysql")
	res.SaysUnknown = strings.Contains(res.Output, "不知道")
	res.Cited = res.MentionsPostgres && res.MentionsSkipLock && !res.SaysUnknown
}

func renderRecall(p memory.Prompt, withMemory bool) string {
	var b strings.Builder
	if withMemory && !p.Empty() {
		b.WriteString("<memory>\n")
		if p.Summary != "" {
			fmt.Fprintf(&b, "较早问答摘要：%s\n", truncate(p.Summary, 600))
		}
		if len(p.RecentTurns) > 0 {
			b.WriteString("最近问答：\n")
			for _, turn := range p.RecentTurns {
				fmt.Fprintf(&b, "- %s\n", truncate(turn, 420))
			}
		}
		b.WriteString("</memory>\n\n")
	}
	b.WriteString("问题：")
	b.WriteString(recallQuestion)
	return b.String()
}

func summaryFired(events []updateEvent) bool {
	for _, ev := range events {
		if ev.Fired {
			return true
		}
	}
	return false
}

func armName(withMemory bool) string {
	if withMemory {
		return "with-memory"
	}
	return "no-memory"
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
