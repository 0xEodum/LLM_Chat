package llm

import (
	"context"
	"fmt"
	"time"

	"LLM_Chat/pkg/llm/providers"

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

// Config конфигурация для клиента
type Config struct {
	Provider         string            `mapstructure:"provider"` // новое поле
	BaseURL          string            `mapstructure:"base_url"`
	APIKey           string            `mapstructure:"api_key"`
	Model            string            `mapstructure:"model"`
	Timeout          time.Duration     `mapstructure:"timeout"`
	ServerURL        string            `mapstructure:"server_url"`
	HTTPHeaders      map[string]string `mapstructure:"http_headers"`
	SystemPromptPath string            `mapstructure:"system_prompt_path"`
}

// NewClient создает новый клиент с выбранным провайдером
func NewClient(config Config, logger *zap.Logger) (*Client, error) {
	// Конвертируем в конфиг провайдера
	providerConfig := providers.Config{
		Provider:         config.Provider,
		BaseURL:          config.BaseURL,
		APIKey:           config.APIKey,
		Model:            config.Model,
		Timeout:          config.Timeout,
		ServerURL:        config.ServerURL,
		HTTPHeaders:      config.HTTPHeaders,
		SystemPromptPath: config.SystemPromptPath,
	}

	// Создаем фабрику и провайдер
	factory := providers.NewFactory(logger)
	provider, err := factory.CreateProvider(providerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider: %w", err)
	}

	return &Client{
		provider: provider,
		logger:   logger,
	}, nil
}

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
	factory := providers.NewFactory(logger)
	supportedProviders := factory.GetSupportedProviders()

	for _, supported := range supportedProviders {
		if supported == providerName {
			return nil
		}
	}

	return fmt.Errorf("unsupported provider '%s', supported providers: %v",
		providerName, supportedProviders)
}

// GetSupportedProviders возвращает список всех поддерживаемых провайдеров
func GetSupportedProviders(logger *zap.Logger) []string {
	factory := providers.NewFactory(logger)
	return factory.GetSupportedProviders()
}
