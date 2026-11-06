package llm

import (
	"context"
	"fmt"

	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// EinoClient wraps any eino model.BaseChatModel (including eino-ext providers
// and the fake used in tests) behind the llm.Client interface.
type EinoClient struct {
	model       einoModel.BaseChatModel
	displayName string
}

func NewEinoClient(m einoModel.BaseChatModel, displayName string) *EinoClient {
	return &EinoClient{model: m, displayName: displayName}
}

var _ Client = (*EinoClient)(nil)

func (c *EinoClient) Generate(ctx context.Context, req Request) (Response, error) {
	input := []*schema.Message{
		schema.SystemMessage(req.System),
		schema.UserMessage(req.User),
	}

	var opts []einoModel.Option
	if req.Temperature > 0 {
		opts = append(opts, einoModel.WithTemperature(req.Temperature))
	}
	if req.MaxTokens > 0 {
		opts = append(opts, einoModel.WithMaxTokens(req.MaxTokens))
	}

	msg, err := c.model.Generate(ctx, input, opts...)
	if err != nil {
		return Response{}, fmt.Errorf("model generate: %w", err)
	}

	resp := Response{Text: msg.Content, Model: c.displayName}
	if msg.ResponseMeta != nil && msg.ResponseMeta.Usage != nil {
		resp.PromptTokens = msg.ResponseMeta.Usage.PromptTokens
		resp.CompletionTokens = msg.ResponseMeta.Usage.CompletionTokens
	}
	return resp, nil
}
