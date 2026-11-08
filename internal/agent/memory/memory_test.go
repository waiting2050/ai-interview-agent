package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/llm"
	"github.com/waiting2050/ai-interview-agent/internal/domain/session"
)

type fakeStore struct {
	state   State
	ok      bool
	loadErr error
	saved   []State
}

func (f *fakeStore) Load(context.Context, string) (State, bool, error) {
	return f.state, f.ok, f.loadErr
}

func (f *fakeStore) Save(_ context.Context, _ string, state State) error {
	f.state = state
	f.ok = true
	f.saved = append(f.saved, state)
	return nil
}

type fakeClient struct {
	requests []llm.Request
	reply    string
	err      error
}

func (f *fakeClient) Generate(_ context.Context, req llm.Request) (llm.Response, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return llm.Response{}, f.err
	}
	reply := f.reply
	if reply == "" {
		reply = "候选人负责订单服务，使用 Go 与 MySQL；已答并发与索引；薄弱点是边界条件。"
	}
	return llm.Response{Text: reply, Model: "fake", PromptTokens: 100, CompletionTokens: 30}, nil
}

func makeTurns(n int) []session.Turn {
	turns := make([]session.Turn, 0, n)
	for i := 1; i <= n; i++ {
		turns = append(turns, session.Turn{
			Seq: i, Topic: "Go 并发模型", Depth: 1,
			Question:   "第" + itoa(i) + "个问题：goroutine 调度如何工作？",
			AnswerText: "候选人提到早期事实：项目代号 Orion，QPS 是从 800 提到 3000。",
			Status:     session.TurnStatusAnswered,
		})
	}
	return turns
}

func itoa(v int) string {
	if v < 10 {
		return string(rune('0' + v))
	}
	return string(rune('0'+v/10)) + string(rune('0'+v%10))
}

func TestSummaryTriggersEveryFiveTurns(t *testing.T) {
	store := &fakeStore{}
	client := &fakeClient{}
	b := NewBuilder(store, client, Options{Recent: 4, Every: 5, Model: "fake", ModelOK: true,
		Now: func() time.Time { return time.Unix(1700000000, 0) }})

	for answered := 1; answered <= 5; answered++ {
		if _, fired, err := b.Update(context.Background(), "s1", makeTurns(answered)); err != nil {
			t.Fatalf("update at %d: %v", answered, err)
		} else if fired {
			t.Fatalf("summary fired too early at %d answered turns", answered)
		}
	}
	if len(client.requests) != 0 {
		t.Fatalf("model called before the threshold: %d calls", len(client.requests))
	}

	if _, fired, err := b.Update(context.Background(), "s1", makeTurns(6)); err != nil || !fired {
		t.Fatalf("summary must fire at 6 answered turns (fired=%v err=%v)", fired, err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("model calls = %d, want 1", len(client.requests))
	}
	if state := store.state; state.UptoSeq != 2 || state.CoveredTurns != 2 {
		t.Fatalf("first summary must cover turns 1-2 (answered 6 - recent 4), got %+v", state)
	}

	for answered := 7; answered <= 10; answered++ {
		if _, fired, _ := b.Update(context.Background(), "s1", makeTurns(answered)); fired {
			t.Fatalf("summary fired again at %d answered turns", answered)
		}
	}
	if _, fired, err := b.Update(context.Background(), "s1", makeTurns(11)); err != nil || !fired {
		t.Fatalf("summary must fire at 11 answered turns (fired=%v err=%v)", fired, err)
	}
	if store.state.UptoSeq != 7 {
		t.Fatalf("second summary must cover through seq 7, got %d", store.state.UptoSeq)
	}
	if len(client.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(client.requests))
	}
}

func TestSummaryPromptContainsEarlyFactAndRecentTurnsStayOut(t *testing.T) {
	store := &fakeStore{}
	client := &fakeClient{}
	b := NewBuilder(store, client, Options{Recent: 4, Every: 5, Model: "fake", ModelOK: true})

	turns := makeTurns(6)
	if _, fired, _ := b.Update(context.Background(), "s1", turns); !fired {
		t.Fatal("summary did not fire")
	}
	// The compression input must contain turn 1's early fact; otherwise the
	// summary can never retain it.
	if len(client.requests) != 1 || !strings.Contains(client.requests[0].User, "Orion") {
		t.Fatalf("compression input lost the early fact: %+v", client.requests)
	}

	prompt := b.Context(context.Background(), "s1", turns)
	if prompt.Summary == "" {
		t.Fatal("summary missing from context")
	}
	if len(prompt.RecentTurns) != 4 {
		t.Fatalf("recent turns = %d, want 4", len(prompt.RecentTurns))
	}
	if strings.Contains(strings.Join(prompt.RecentTurns, "\n"), "第1个问题") {
		t.Fatal("recent window must not repeat summarized turns")
	}
}

func TestSummaryFallsBackWhenModelUnavailable(t *testing.T) {
	store := &fakeStore{}
	b := NewBuilder(store, nil, Options{Recent: 4, Every: 5, ModelOK: false})
	if _, fired, err := b.Update(context.Background(), "s1", makeTurns(6)); err != nil || !fired {
		t.Fatalf("fallback summary must still fire (fired=%v err=%v)", fired, err)
	}
	if !store.state.Degraded {
		t.Fatal("fallback summary must be marked degraded")
	}
	if !strings.Contains(store.state.Summary, "Orion") || !strings.Contains(store.state.Summary, "Go 并发模型") {
		t.Fatalf("fallback summary lost facts: %q", store.state.Summary)
	}
}

func TestSummaryModelFailureDegradesExplicitly(t *testing.T) {
	store := &fakeStore{}
	client := &fakeClient{err: errors.New("provider down")}
	b := NewBuilder(store, client, Options{Recent: 4, Every: 5, ModelOK: true})
	state, fired, err := b.Update(context.Background(), "s1", makeTurns(6))
	if err != nil {
		t.Fatalf("degradation must not fail the answer run: %v", err)
	}
	if !fired || !state.Degraded || state.PromptVersion != SummaryVersion+"-fallback" {
		t.Fatalf("model failure not recorded as degraded: %+v", state)
	}
	if !strings.Contains(state.Summary, "Orion") {
		t.Fatalf("digest lost the early fact: %q", state.Summary)
	}
}

func TestContextDegradesWhenStoreFails(t *testing.T) {
	store := &fakeStore{loadErr: errors.New("db down")}
	b := NewBuilder(store, nil, Options{Recent: 4, Every: 5})
	prompt := b.Context(context.Background(), "s1", makeTurns(6))
	if prompt.Summary != "" {
		t.Fatalf("summary should be empty on store failure: %+v", prompt)
	}
	if len(prompt.RecentTurns) != 4 {
		t.Fatalf("recent turns must survive store failure: %d", len(prompt.RecentTurns))
	}
}

func TestSummaryCapAndThresholdBoundaries(t *testing.T) {
	store := &fakeStore{}
	client := &fakeClient{reply: strings.Repeat("摘", 700)}
	b := NewBuilder(store, client, Options{Recent: 4, Every: 5, ModelOK: true})
	if _, _, err := b.Update(context.Background(), "s1", makeTurns(6)); err != nil {
		t.Fatal(err)
	}
	if len([]rune(store.state.Summary)) > 601 {
		t.Fatalf("summary exceeds cap: %d runes", len([]rune(store.state.Summary)))
	}
}
