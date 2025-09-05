package llm

import (
	"context"
)

// LLMClient интерфейс для работы с LLM API
type LLMClient interface {
	ChatCompletion(ctx context.Context, messages []Message) (*ChatResponse, error)
	ChatCompletionStream(ctx context.Context, messages []Message) (<-chan StreamChunk, error)
}

// SummaryClient интерфейс для сжатия контекста (будет использоваться позже)
type SummaryClient interface {
	SummarizeHistory(ctx context.Context, messages []Message) (string, error)
	CreateAnchors(ctx context.Context, messages []Message) ([]string, error)
}

// Verify interface implementation
var _ LLMClient = (*Client)(nil)
