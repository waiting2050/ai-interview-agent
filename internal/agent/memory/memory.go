// Package memory builds the three-layer prompt context: structured state
// (persisted separately), the last N answered turns verbatim and a compressed
// summary of everything older.
package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
)

// SummaryVersion is persisted with every summary so a prompt change can be
// identified without storing the full text.
const SummaryVersion = "memory-v1"

// State is the persisted summary layer. It is derived data: deleting it only
// costs one more summarisation, never interview state.
type State struct {
	Summary          string    `json:"summary"`
	UptoSeq          int       `json:"upto_seq"`
	CoveredTurns     int       `json:"covered_turns"`
	PromptVersion    string    `json:"prompt_version"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	Degraded         bool      `json:"degraded"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Store persists one summary per session. Missing rows are not an error.
type Store interface {
	Load(ctx context.Context, sessionID string) (State, bool, error)
	Save(ctx context.Context, sessionID string, state State) error
}

// Prompt is the memory slice injected into question generation.
type Prompt struct {
	Summary     string   `json:"summary,omitempty"`
	RecentTurns []string `json:"recent_turns,omitempty"`
}

// Empty reports whether both layers are absent.
func (p Prompt) Empty() bool { return p.Summary == "" && len(p.RecentTurns) == 0 }

// Builder owns the threshold and the compression call. One Builder is shared
// by all sessions; state is keyed by session id in the store.
type Builder struct {
	store   Store
	client  llm.Client
	recent  int
	every   int
	model   string
	modelOK bool
	now     func() time.Time
	log     *slog.Logger
}

// Options configure the builder. ModelOK is false when the provider fell back
// to the heuristic client; in that case compression uses a deterministic
// digest so a no-key demo still gets a summary.
type Options struct {
	Recent  int
	Every   int
	Model   string
	ModelOK bool
	Now     func() time.Time
	Logger  *slog.Logger
}

func NewBuilder(store Store, client llm.Client, opts Options) *Builder {
	if opts.Recent <= 0 {
		opts.Recent = 4
	}
	if opts.Every <= 0 {
		opts.Every = 5
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Builder{
		store: store, client: client, recent: opts.Recent, every: opts.Every,
		model: opts.Model, modelOK: opts.ModelOK, now: opts.Now, log: opts.Logger,
	}
}

// RecentWindow exposes the configured verbatim window for tests and docs.
func (b *Builder) RecentWindow() int { return b.recent }

// SummaryEvery exposes the trigger interval.
func (b *Builder) SummaryEvery() int { return b.every }

// Context loads the summary and renders the last N answered turns. A store
// failure degrades to recent-only context instead of failing the answer run.
func (b *Builder) Context(ctx context.Context, sessionID string, turns []session.Turn) Prompt {
	prompt := Prompt{RecentTurns: recentTurns(turns, b.recent)}
	if b.store == nil {
		return prompt
	}
	state, ok, err := b.store.Load(ctx, sessionID)
	if err != nil {
		b.log.Warn("load session memory failed, using recent turns only", "error", err)
		return prompt
	}
	if ok {
		prompt.Summary = state.Summary
	}
	return prompt
}

// Update compresses older turns when the threshold is hit. The trigger is
// answer-count based, not token based: it fires at 6, 11, 16 ... answered
// turns (every+1, then each `every` turns). Recent turns are never included in
// the summary, so the two layers never duplicate one another.
func (b *Builder) Update(ctx context.Context, sessionID string, turns []session.Turn) (State, bool, error) {
	if b.store == nil {
		return State{}, false, nil
	}
	state, _, err := b.store.Load(ctx, sessionID)
	if err != nil {
		return State{}, false, fmt.Errorf("load session memory: %w", err)
	}
	answered := answeredTurns(turns)
	if len(answered) < b.every+1 {
		return state, false, nil
	}
	answeredCount := len(answered)
	if (answeredCount-1)%b.every != 0 {
		return state, false, nil
	}

	boundary := answeredCount - b.recent
	if boundary <= state.UptoSeq {
		return state, false, nil
	}
	toCompress := compactable(answered, state.UptoSeq, boundary)
	if len(toCompress) == 0 {
		return state, false, nil
	}

	next := State{
		UptoSeq:      boundary,
		CoveredTurns: boundary,
		UpdatedAt:    b.now().UTC(),
	}
	if b.modelOK && b.client != nil {
		summary, promptTokens, completionTokens, err := b.compress(ctx, state.Summary, toCompress)
		if err == nil && summary != "" {
			next.Summary = summary
			next.PromptVersion = SummaryVersion
			next.Model = b.model
			next.PromptTokens = promptTokens
			next.CompletionTokens = completionTokens
		} else if err != nil {
			b.log.Warn("summary model call failed, using deterministic digest", "error", err)
			next.Summary = digest(state.Summary, toCompress)
			next.Degraded = true
			next.PromptVersion = SummaryVersion + "-fallback"
		}
	} else {
		next.Summary = digest(state.Summary, toCompress)
		next.Degraded = true
		next.PromptVersion = SummaryVersion + "-fallback"
	}
	if next.Summary == "" {
		return state, false, nil
	}
	if err := b.store.Save(ctx, sessionID, next); err != nil {
		return state, false, fmt.Errorf("save session memory: %w", err)
	}
	b.log.Info("session memory summarized", "session_id", sessionID, "covered_turns", next.CoveredTurns,
		"degraded", next.Degraded, "prompt_tokens", next.PromptTokens, "completion_tokens", next.CompletionTokens)
	return next, true, nil
}

func (b *Builder) compress(ctx context.Context, previous string, turns []session.Turn) (string, int, int, error) {
	var transcript strings.Builder
	for _, turn := range turns {
		fmt.Fprintf(&transcript, "第 %d 题（%s，追问 %d 层）\n问：%s\n答：%s\n\n",
			turn.Seq, turn.Topic, turn.Depth, truncate(turn.Question, 200), truncate(turn.AnswerText, 300))
	}
	user := fmt.Sprintf("已有摘要：\n%s\n\n需要压缩的问答：\n%s", previousOrNone(previous), transcript.String())
	user = truncate(user, 2200)

	resp, err := b.client.Generate(ctx, llm.Request{
		System:      summarySystem,
		User:        user,
		Temperature: 0,
		MaxTokens:   600,
	})
	if err != nil {
		return "", 0, 0, err
	}
	summary := sanitizeSummary(resp.Text)
	if summary == "" {
		return "", 0, 0, errors.New("empty summary")
	}
	return summary, resp.PromptTokens, resp.CompletionTokens, nil
}

const summarySystem = `[TASK:memory] 你是面试记录压缩器。把「已有摘要」与「需要压缩的问答」合并成 600 字以内的客观摘要。只输出摘要正文，不要 JSON、不要 Markdown 标题。

必须保留：
1. 候选人提到的项目、技术栈与关键经历事实，保留可核验的具体名词（系统名、版本、指标）。
2. 已问题目主题、追问方向、明显薄弱点。
3. 候选人自己做出的承诺或约束（例如「这里我会用异步」「我们规定不超过 3 次重试」）。
4. 不要编造摘要中不存在的信息，不要写评价性套话。

信息有冲突时以更新的问答为准，并在摘要中写「已更新」。`

// digest is the no-model fallback. It keeps topic names, question stems and
// candidate answer fragments so early facts survive even without a provider.
func digest(previous string, turns []session.Turn) string {
	var b strings.Builder
	if trimmed := strings.TrimSpace(previous); trimmed != "" {
		b.WriteString(trimmed)
		b.WriteString("\n")
	}
	for _, turn := range turns {
		fmt.Fprintf(&b, "第%d题[%s] 问：%s 答：%s；",
			turn.Seq, turn.Topic, truncate(turn.Question, 60), truncate(turn.AnswerText, 90))
	}
	return sanitizeSummary(b.String())
}

// sanitizeSummary enforces the 600-rune cap.
func sanitizeSummary(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "`")
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > 600 {
		return strings.TrimSpace(string(runes[:600])) + "…"
	}
	return text
}

func answeredTurns(turns []session.Turn) []session.Turn {
	out := make([]session.Turn, 0, len(turns))
	for _, turn := range turns {
		if turn.Status == session.TurnStatusAnswered {
			out = append(out, turn)
		}
	}
	// Turns arrive in seq order from the repository; sorting is a defensive
	// no-op that keeps the boundary arithmetic stable if that changes.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Seq > out[j].Seq; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func recentTurns(turns []session.Turn, n int) []string {
	answered := answeredTurns(turns)
	if len(answered) > n {
		answered = answered[len(answered)-n:]
	}
	out := make([]string, 0, len(answered))
	for _, turn := range answered {
		out = append(out, fmt.Sprintf("问：%s\n答：%s", truncate(turn.Question, 120), truncate(turn.AnswerText, 300)))
	}
	return out
}

func compactable(turns []session.Turn, afterSeq, uptoSeq int) []session.Turn {
	out := make([]session.Turn, 0, uptoSeq-afterSeq)
	for _, turn := range turns {
		if turn.Seq > afterSeq && turn.Seq <= uptoSeq {
			out = append(out, turn)
		}
	}
	return out
}

func previousOrNone(previous string) string {
	if strings.TrimSpace(previous) == "" {
		return "（无）"
	}
	return previous
}

func truncate(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "…"
}
