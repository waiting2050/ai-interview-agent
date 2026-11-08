package interview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
)

func TestQuestionGeneratorParsesModelJSON(t *testing.T) {
	client := llm.NewEinoClient(llm.Scripted("m", `{"question":"请结合支付项目的对账流程，说明消息重复消费时如何保证幂等？"}`), "m")
	generator := NewQuestionGenerator(client, "m")

	out, err := generator.Generate(context.Background(), QuestionInput{Topic: "一致性"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out.Degraded {
		t.Fatalf("unexpected degradation: %+v", out)
	}
	if out.Text == "" || out.Attempts != 1 || out.PromptVersion == "" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestQuestionGeneratorRetriesDuplicate(t *testing.T) {
	history := []string{"请说明 goroutine 的调度模型与抢占机制，以及它在高并发下的表现。"}
	client := llm.NewEinoClient(llm.Scripted("m",
		`{"question":"请说明 goroutine 的调度模型与抢占机制，以及它在高并发下的表现。"}`,
		`{"question":"请结合订单对账项目，说明 channel 缓冲与 context 取消在超时链路中的取舍。"}`,
	), "m")
	generator := NewQuestionGenerator(client, "m")

	out, err := generator.Generate(context.Background(), QuestionInput{Topic: "并发", History: history})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out.Degraded || out.Attempts != 2 {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Text == history[0] {
		t.Fatal("duplicate question accepted")
	}
}

func TestQuestionGeneratorFallsBackWhenModelKeepsRepeating(t *testing.T) {
	history := []string{"请说明 goroutine 的调度模型与抢占机制，以及它在高并发下的表现。"}
	client := llm.NewEinoClient(llm.Scripted("m", `{"question":"请说明 goroutine 的调度模型与抢占机制，以及它在高并发下的表现。"}`), "m")
	generator := NewQuestionGenerator(client, "m")

	out, err := generator.Generate(context.Background(), QuestionInput{
		Topic:          "Go 并发模型",
		ExpectedPoints: []string{"goroutine 调度与 G-M-P 模型"},
		History:        history,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !out.Degraded || out.ErrorCode != ErrorCodeQuestionDuplicate {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Text == history[0] {
		t.Fatal("fallback repeated a history question")
	}
}

func TestQuestionGeneratorFallsBackOnTransportError(t *testing.T) {
	generator := NewQuestionGenerator(failingClient{}, "m")

	out, err := generator.Generate(context.Background(), QuestionInput{Topic: "Redis 缓存"})
	if err != nil {
		t.Fatalf("generate must degrade: %v", err)
	}
	if !out.Degraded || out.ErrorCode != ErrorCodeModelUnavailable || out.Text == "" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestHeuristicQuestionIsUnique(t *testing.T) {
	generator := NewQuestionGenerator(NewHeuristicClient(), "fake-heuristic")
	history := []string{FallbackQuestion(QuestionInput{Kind: "primary", Topic: "Go 并发模型", History: []string{"a"}})}

	out, err := generator.Generate(context.Background(), QuestionInput{
		Kind:           "primary",
		Topic:          "Go 并发模型",
		ExpectedPoints: []string{"channel 阻塞与缓冲区设计", "context 取消与超时传递"},
		History:        history,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out.Degraded || out.Text == "" || out.Text == history[0] {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestQuestionPromptCarriesLayeredMemory(t *testing.T) {
	client := &capturingClient{reply: `{"question":"你刚才说选 PostgreSQL 是为了 SKIP LOCKED，请说明长事务下这个方案的边界。"}`}
	generator := NewQuestionGenerator(client, "m")

	out, err := generator.Generate(context.Background(), QuestionInput{
		Kind:              "primary",
		Topic:             "数据库选型",
		MemorySummary:     "第 2 轮：数据库选 PostgreSQL，因为需要 SKIP LOCKED 的队列语义。",
		MemoryRecentTurns: []string{"问：为什么不用 MySQL？答：需要 SKIP LOCKED。"},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if out.Degraded {
		t.Fatalf("unexpected degradation: %+v", out)
	}
	if !strings.Contains(client.req.User, "<memory>") || !strings.Contains(client.req.User, "SKIP LOCKED") {
		t.Fatalf("memory block missing from prompt:\n%s", client.req.User)
	}
	if !strings.Contains(client.req.User, "最近问答") || !strings.Contains(client.req.User, "</memory>") {
		t.Fatalf("recent turns missing from prompt:\n%s", client.req.User)
	}
}

type capturingClient struct {
	req   llm.Request
	reply string
}

func (c *capturingClient) Generate(_ context.Context, req llm.Request) (llm.Response, error) {
	c.req = req
	return llm.Response{Text: c.reply, Model: "m", PromptTokens: 1, CompletionTokens: 1}, nil
}

type failingClient struct{}

func (failingClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("dial tcp: connection refused")
}
