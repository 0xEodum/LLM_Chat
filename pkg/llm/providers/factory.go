package providers

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
)

type Factory struct {
	logger *zap.Logger
}

func NewFactory(logger *zap.Logger) ProviderFactory {
	return &Factory{
		logger: logger,
	}
}

func (f *Factory) CreateProvider(config Config) (Provider, error) {
	provider := strings.ToLower(config.Provider)

	switch provider {
	case "gemini":
		// Создаем MCP конфигурацию (должна передаваться извне)
		// Это временное решение - в реальности конфигурация будет передаваться из main.go
		mcpConfig := MCPProviderConfig{
			ServerURL:        "http://localhost:8000/mcp", // будет переопределено
			SystemPromptPath: "system_prompt.txt",         // будет переопределено
			MaxIterations:    10,                          // будет переопределено
			HTTPHeaders:      nil,
		}
		return NewMCPGeminiProvider(config, mcpConfig, f.logger)
	default:
		return nil, fmt.Errorf("unsupported provider: %s (only 'gemini' is supported)", config.Provider)
	}
}

func (f *Factory) GetSupportedProviders() []string {
	return []string{"gemini"}
}

// CreateProviderWithMCP создает провайдер с MCP конфигурацией
func (f *Factory) CreateProviderWithMCP(config Config, mcpConfig MCPProviderConfig) (Provider, error) {
	provider := strings.ToLower(config.Provider)

	switch provider {
	case "gemini":
		return NewMCPGeminiProvider(config, mcpConfig, f.logger)
	default:
		return nil, fmt.Errorf("unsupported provider: %s (only 'gemini' is supported)", config.Provider)
	}
}
