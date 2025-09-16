package llm

import (
	"LLM_Chat/pkg/llm/providers"
	"context"
	"fmt"

	"go.uber.org/zap"
)

// Client обертка над провайдерами для обратной совместимости
type Client struct {
	provider providers.Provider
	logger   *zap.Logger
}

// Message совместимый тип (переиспользуем из providers)
type Message = providers.Message

// ChatResponse совместимый тип
type ChatResponse = providers.ChatResponse

// Choice совместимый тип
type Choice = providers.Choice

// Delta совместимый тип
type Delta = providers.Delta

// Usage совместимый тип
type Usage = providers.Usage

// StreamChunk совместимый тип
type StreamChunk = providers.StreamChunk

// NewClientWithProvider создает клиент с готовым провайдером
func NewClientWithProvider(provider providers.Provider, logger *zap.Logger) *Client {
	return &Client{
		provider: provider,
		logger:   logger,
	}
}

// ChatCompletion выполняет запрос к LLM (делегирует провайдеру)
func (c *Client) ChatCompletion(ctx context.Context, messages []Message) (*ChatResponse, error) {
	c.logger.Debug("Executing chat completion",
		zap.String("provider", c.provider.GetName()),
		zap.Int("messages_count", len(messages)),
	)

	return c.provider.ChatCompletion(ctx, messages)
}

// ChatCompletionStream выполняет стриминговый запрос к LLM
func (c *Client) ChatCompletionStream(ctx context.Context, messages []Message) (<-chan StreamChunk, error) {
	c.logger.Debug("Executing streaming chat completion",
		zap.String("provider", c.provider.GetName()),
		zap.Int("messages_count", len(messages)),
	)

	return c.provider.ChatCompletionStream(ctx, messages)
}

// GetProviderName возвращает имя используемого провайдера
func (c *Client) GetProviderName() string {
	return c.provider.GetName()
}

// GetSupportedModels возвращает список поддерживаемых моделей текущего провайдера
func (c *Client) GetSupportedModels() []string {
	return c.provider.GetSupportedModels()
}

// ValidateProvider проверяет, поддерживается ли провайдер
func ValidateProvider(providerName string, logger *zap.Logger) error {
	if providerName != "gemini" {
		return fmt.Errorf("unsupported provider '%s', only 'gemini' is supported", providerName)
	}
	return nil
}

// GetSupportedProviders возвращает список всех поддерживаемых провайдеров
func GetSupportedProviders(logger *zap.Logger) []string {
	return []string{"gemini"}
}
