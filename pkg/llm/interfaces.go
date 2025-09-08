package llm

import (
	"context"
)

// LLMClient интерфейс для работы с LLM API (расширенный)
type LLMClient interface {
	ChatCompletion(ctx context.Context, messages []Message) (*ChatResponse, error)
	ChatCompletionStream(ctx context.Context, messages []Message) (<-chan StreamChunk, error)

	// Новые методы для работы с провайдерами
	GetProviderName() string
	GetSupportedModels() []string
}

// SummaryClient интерфейс для сжатия контекста (остается без изменений)
type SummaryClient interface {
	SummarizeHistory(ctx context.Context, messages []Message) (string, error)
	CreateAnchors(ctx context.Context, messages []Message) ([]string, error)
}

// ProviderInfo информация о провайдере
type ProviderInfo struct {
	Name            string   `json:"name"`
	SupportedModels []string `json:"supported_models"`
	Description     string   `json:"description"`
	RequiredConfig  []string `json:"required_config"`
}

// ProviderRegistry интерфейс для работы с реестром провайдеров
type ProviderRegistry interface {
	GetAvailableProviders() []ProviderInfo
	ValidateProviderConfig(provider string, config map[string]interface{}) error
}

// Verify interface implementation
var _ LLMClient = (*Client)(nil)
