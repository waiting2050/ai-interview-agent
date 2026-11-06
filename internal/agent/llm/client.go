// Package llm adapts eino chat models to the small request/response shape
// used by the interview agent. Keeping the adapter here means application
// code never imports eino types directly.
package llm

import "context"

type Request struct {
	System      string
	User        string
	Temperature float32
	MaxTokens   int
}

type Response struct {
	Text             string
	Model            string
	PromptTokens     int
	CompletionTokens int
}

// Client is the only model interface the application depends on.
type Client interface {
	Generate(ctx context.Context, req Request) (Response, error)
}
