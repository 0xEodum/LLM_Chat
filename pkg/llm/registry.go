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
	providerNames := r.factory.GetSupportedProviders()
	var providers []ProviderInfo

	for _, name := range providerNames {
		info := r.getProviderInfo(name)
		providers = append(providers, info)
	}

	return providers
}

// ValidateProviderConfig проверяет конфигурацию провайдера
func (r *Registry) ValidateProviderConfig(providerName string, config map[string]interface{}) error {
	// Проверяем, поддерживается ли провайдер
	supportedProviders := r.factory.GetSupportedProviders()
	supported := false
	for _, p := range supportedProviders {
		if strings.ToLower(p) == strings.ToLower(providerName) {
			supported = true
			break
		}
	}

	if !supported {
		return fmt.Errorf("unsupported provider: %s", providerName)
	}

	// Проверяем обязательные поля
	requiredFields := r.getRequiredFields(providerName)
	for _, field := range requiredFields {
		if _, exists := config[field]; !exists {
			return fmt.Errorf("missing required field '%s' for provider '%s'", field, providerName)
		}
	}

	return nil
}

func (r *Registry) getProviderInfo(provider string) ProviderInfo {
	switch strings.ToLower(provider) {
	case "openrouter":
		return ProviderInfo{
			Name: "OpenRouter",
			SupportedModels: []string{
				"google/gemma-3-27b-it:free",
				"anthropic/claude-sonnet-4",
				"openai/gpt-4o",
				"meta/llama-3.1-8b-instruct:free",
			},
			Description:    "OpenRouter provides access to multiple LLM providers through a unified API",
			RequiredConfig: []string{"api_key", "base_url", "model"},
		}
	case "gemini":
		return ProviderInfo{
			Name: "Google Gemini",
			SupportedModels: []string{
				"gemini-2.0-flash",
				"gemini-1.5-pro",
				"gemini-1.5-flash",
			},
			Description:    "Google's Gemini AI models with advanced reasoning capabilities",
			RequiredConfig: []string{"api_key", "base_url", "model"},
		}
	default:
		return ProviderInfo{
			Name:            provider,
			SupportedModels: []string{},
			Description:     "Unknown provider",
			RequiredConfig:  []string{"api_key", "base_url", "model"},
		}
	}
}

func (r *Registry) getRequiredFields(provider string) []string {
	info := r.getProviderInfo(provider)
	return info.RequiredConfig
}

// GetProviderByName создает экземпляр провайдера по имени
func (r *Registry) GetProviderByName(name string, config providers.Config) (providers.Provider, error) {
	config.Provider = name
	return r.factory.CreateProvider(config)
}
