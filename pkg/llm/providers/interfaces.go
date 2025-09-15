// pkg/llm/providers/interfaces.go
package providers

import (
	"context"
	"time"
)

// Message представляет сообщение в диалоге (универсальный формат)
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatResponse представляет ответ от LLM (универсальный формат)
type ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	Delta        Delta   `json:"delta,omitempty"`
	FinishReason string  `json:"finish_reason"`
}

type Delta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamChunk представляет чанк в потоковом ответе
type StreamChunk struct {
	Content string
	Done    bool
	Error   error
}

// Provider интерфейс для LLM провайдеров
type Provider interface {
	// GetName возвращает имя провайдера
	GetName() string

	// ChatCompletion выполняет запрос без стриминга
	ChatCompletion(ctx context.Context, messages []Message) (*ChatResponse, error)

	// ChatCompletionStream выполняет стриминговый запрос
	ChatCompletionStream(ctx context.Context, messages []Message) (<-chan StreamChunk, error)

	// GetSupportedModels возвращает список поддерживаемых моделей
	GetSupportedModels() []string

	// ValidateConfig проверяет корректность конфигурации
	ValidateConfig() error
}

// Config общая конфигурация для всех провайдеров
type Config struct {
	Provider         string            `mapstructure:"provider"` // "openrouter", "gemini", etc.
	BaseURL          string            `mapstructure:"base_url"`
	APIKey           string            `mapstructure:"api_key"`
	Model            string            `mapstructure:"model"`
	Timeout          time.Duration     `mapstructure:"timeout"`
	ServerURL        string            `mapstructure:"server_url"`
	HTTPHeaders      map[string]string `mapstructure:"http_headers"`
	SystemPromptPath string            `mapstructure:"system_prompt_path"`
}

// ProviderFactory создает провайдеров
type ProviderFactory interface {
	CreateProvider(config Config) (Provider, error)
	GetSupportedProviders() []string
}
