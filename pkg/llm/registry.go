package llm

import (
	"fmt"
	"strings"

	"LLM_Chat/pkg/llm/providers"

	"go.uber.org/zap"
)

// Registry реестр доступных провайдеров
type Registry struct {
	factory providers.ProviderFactory
	logger  *zap.Logger
}

// NewRegistry создает новый реестр провайдеров
func NewRegistry(logger *zap.Logger) *Registry {
	return &Registry{
		factory: providers.NewFactory(logger),
		logger:  logger,
	}
}

// GetAvailableProviders возвращает список доступных провайдеров с их описанием
func (r *Registry) GetAvailableProviders() []ProviderInfo {
	return []ProviderInfo{
		r.getGeminiMCPInfo(),
	}
}

// ValidateProviderConfig проверяет конфигурацию провайдера
func (r *Registry) ValidateProviderConfig(providerName string, config map[string]interface{}) error {
	// Проверяем, что это Gemini
	if strings.ToLower(providerName) != "gemini" {
		return fmt.Errorf("unsupported provider: %s (only 'gemini' is supported)", providerName)
	}

	// Проверяем обязательные поля для Gemini MCP
	requiredFields := []string{"api_key", "model"}
	for _, field := range requiredFields {
		if _, exists := config[field]; !exists {
			return fmt.Errorf("missing required field '%s' for Gemini MCP provider", field)
		}
	}

	return nil
}

func (r *Registry) getGeminiMCPInfo() ProviderInfo {
	return ProviderInfo{
		Name: "Gemini (MCP)",
		SupportedModels: []string{
			"gemini-2.5-flash",
			"gemini-2.0-flash",
			"gemini-1.5-pro",
			"gemini-1.5-flash",
		},
		Description:    "Google's Gemini AI models with MCP (Model Context Protocol) tool support for enhanced capabilities",
		RequiredConfig: []string{"api_key", "model", "mcp_server_url", "system_prompt_path"},
	}
}

// GetProviderByName создает экземпляр провайдера по имени
func (r *Registry) GetProviderByName(name string, config providers.Config) (providers.Provider, error) {
	if strings.ToLower(name) != "gemini" {
		return nil, fmt.Errorf("unsupported provider: %s", name)
	}

	config.Provider = "gemini"
	return r.factory.CreateProvider(config)
}

// GetProviderByNameWithMCP создает экземпляр MCP провайдера
func (r *Registry) GetProviderByNameWithMCP(name string, config providers.Config, mcpConfig providers.MCPProviderConfig) (providers.Provider, error) {
	if strings.ToLower(name) != "gemini" {
		return nil, fmt.Errorf("unsupported provider: %s", name)
	}

	config.Provider = "gemini"
	factory := r.factory.(*providers.Factory)
	return factory.CreateProviderWithMCP(config, mcpConfig)
}
