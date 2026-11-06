package llm

import (
	"context"
	"sync"

	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// RespondFunc produces the assistant text for a request. Tests use it to
// script valid, invalid and repairing outputs without an API key.
type RespondFunc func(ctx context.Context, req Request) (string, error)

// FakeChatModel implements eino's model.BaseChatModel. It is the default model
// in local runs and the test double in unit tests.
type FakeChatModel struct {
	Name    string
	Respond RespondFunc

	calls int
}

func NewFakeChatModel(name string, respond RespondFunc) *FakeChatModel {
	return &FakeChatModel{Name: name, Respond: respond}
}

var _ einoModel.BaseChatModel = (*FakeChatModel)(nil)

func (f *FakeChatModel) Generate(ctx context.Context, in []*schema.Message, _ ...einoModel.Option) (*schema.Message, error) {
	req := flatten(in)
	f.calls++

	text, err := f.Respond(ctx, req)
	if err != nil {
		return nil, err
	}
	return &schema.Message{
		Role:    schema.Assistant,
		Content: text,
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop",
			Usage: &schema.TokenUsage{
				PromptTokens:     len([]rune(req.System+req.User)) / 3,
				CompletionTokens: len([]rune(text)) / 3,
			},
		},
	}, nil
}

func (f *FakeChatModel) Stream(ctx context.Context, in []*schema.Message, opts ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := f.Generate(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

// Calls reports how many Generate calls were made, for repair-retry assertions.
func (f *FakeChatModel) Calls() int { return f.calls }

func flatten(in []*schema.Message) Request {
	var req Request
	for _, msg := range in {
		if msg == nil {
			continue
		}
		if msg.Role == schema.System {
			req.System += msg.Content
		} else {
			req.User += msg.Content
		}
	}
	return req
}

// Scripted returns a fake model that replies with the supplied texts in order
// and repeats the last one afterwards. A nil entry is treated as a transport
// error so retry/repair paths can be exercised.
func Scripted(name string, responses ...string) *FakeChatModel {
	var (
		mu  sync.Mutex
		idx int
	)
	return NewFakeChatModel(name, func(context.Context, Request) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(responses) == 0 {
			return "", nil
		}
		if idx >= len(responses) {
			idx = len(responses) - 1
		}
		text := responses[idx]
		idx++
		return text, nil
	})
}
