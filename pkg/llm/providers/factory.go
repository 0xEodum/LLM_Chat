package providers

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
)

type factory struct {
	logger *zap.Logger
}

func NewFactory(logger *zap.Logger) ProviderFactory {
	return &factory{
		logger: logger,
	}
}

func (f *factory) CreateProvider(config Config) (Provider, error) {
	provider := strings.ToLower(config.Provider)

	switch provider {
	case "openrouter":
		return NewOpenRouterProvider(config, f.logger)
	case "gemini":
		return NewGeminiProvider(config, f.logger)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", config.Provider)
	}
}

func (f *factory) GetSupportedProviders() []string {
	return []string{"openrouter", "gemini"}
}
