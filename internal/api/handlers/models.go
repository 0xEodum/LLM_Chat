// internal/api/handlers/models.go (обновленная версия)
package handlers

import (
	"net/http"

	"LLM_Chat/pkg/llm"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ModelsHandler struct {
	logger   *zap.Logger
	registry *llm.Registry
}

func NewModelsHandler(logger *zap.Logger) *ModelsHandler {
	return &ModelsHandler{
		logger:   logger,
		registry: llm.NewRegistry(logger),
	}
}

type ModelInfo struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Provider    string  `json:"provider"`
	Description string  `json:"description"`
	ContextSize int     `json:"context_size,omitempty"`
	CostPer1K   float64 `json:"cost_per_1k_tokens,omitempty"`
}

type ProviderInfo struct {
	Name            string      `json:"name"`
	Description     string      `json:"description"`
	SupportedModels []ModelInfo `json:"supported_models"`
	RequiredConfig  []string    `json:"required_config"`
}

type ModelsResponse struct {
	CurrentProvider    string         `json:"current_provider"`
	AvailableProviders []ProviderInfo `json:"available_providers"`
	SupportedProviders []string       `json:"supported_providers"`
}

// GET /models - получение информации о доступных провайдерах и моделях
func (h *ModelsHandler) GetAvailableModels(c *gin.Context) {
	// Получаем информацию о всех доступных провайдерах
	providerInfos := h.registry.GetAvailableProviders()

	var availableProviders []ProviderInfo
	var supportedProviders []string

	for _, info := range providerInfos {
		supportedProviders = append(supportedProviders, info.Name)

		// Конвертируем модели в наш формат
		var models []ModelInfo
		for _, modelID := range info.SupportedModels {
			model := h.getModelDetails(modelID, info.Name)
			models = append(models, model)
		}

		providerInfo := ProviderInfo{
			Name:            info.Name,
			Description:     info.Description,
			SupportedModels: models,
			RequiredConfig:  info.RequiredConfig,
		}
		availableProviders = append(availableProviders, providerInfo)
	}

	// Получаем текущий провайдер из конфигурации (если доступен через контекст)
	currentProvider := h.getCurrentProvider(c)

	response := ModelsResponse{
		CurrentProvider:    currentProvider,
		AvailableProviders: availableProviders,
		SupportedProviders: supportedProviders,
	}

	c.JSON(http.StatusOK, response)
}

// GET /models/:provider - получение моделей конкретного провайдера
func (h *ModelsHandler) GetProviderModels(c *gin.Context) {
	providerName := c.Param("provider")
	if providerName == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: "provider parameter is required",
			Code:  "MISSING_PROVIDER",
		})
		return
	}

	// Получаем информацию о провайдере
	providerInfos := h.registry.GetAvailableProviders()

	var targetProvider *llm.ProviderInfo
	for _, info := range providerInfos {
		if info.Name == providerName {
			targetProvider = &info
			break
		}
	}

	if targetProvider == nil {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: "provider not found",
			Code:  "PROVIDER_NOT_FOUND",
		})
		return
	}

	// Конвертируем модели в детальный формат
	var models []ModelInfo
	for _, modelID := range targetProvider.SupportedModels {
		model := h.getModelDetails(modelID, targetProvider.Name)
		models = append(models, model)
	}

	response := ProviderInfo{
		Name:            targetProvider.Name,
		Description:     targetProvider.Description,
		SupportedModels: models,
		RequiredConfig:  targetProvider.RequiredConfig,
	}

	c.JSON(http.StatusOK, response)
}

func (h *ModelsHandler) getModelDetails(modelID, provider string) ModelInfo {
	// Базовая информация о моделях
	baseModel := ModelInfo{
		ID:       modelID,
		Name:     modelID,
		Provider: provider,
	}

	// Детали специфичные для моделей
	switch {
	// OpenRouter модели
	case modelID == "google/gemma-3-27b-it:free":
		baseModel.Name = "Gemma 3 27B IT (Free)"
		baseModel.Description = "Free tier model with good performance"
		baseModel.ContextSize = 8192
		baseModel.CostPer1K = 0.0
	case modelID == "anthropic/claude-sonnet-4":
		baseModel.Name = "Claude Sonnet 4"
		baseModel.Description = "High-quality model for complex tasks"
		baseModel.ContextSize = 200000
		baseModel.CostPer1K = 0.15
	case modelID == "openai/gpt-4o":
		baseModel.Name = "GPT-4 Optimized"
		baseModel.Description = "Optimized GPT-4 model"
		baseModel.ContextSize = 128000
		baseModel.CostPer1K = 0.10
	case modelID == "meta/llama-3.1-8b-instruct:free":
		baseModel.Name = "Llama 3.1 8B Instruct (Free)"
		baseModel.Description = "Free Meta Llama model"
		baseModel.ContextSize = 8192
		baseModel.CostPer1K = 0.0

	// Gemini модели
	case modelID == "gemini-2.0-flash":
		baseModel.Name = "Gemini 2.0 Flash"
		baseModel.Description = "Latest fast Gemini model with multimodal capabilities"
		baseModel.ContextSize = 32768
		baseModel.CostPer1K = 0.05
	case modelID == "gemini-1.5-pro":
		baseModel.Name = "Gemini 1.5 Pro"
		baseModel.Description = "High-performance Gemini model"
		baseModel.ContextSize = 128000
		baseModel.CostPer1K = 0.12
	case modelID == "gemini-1.5-flash":
		baseModel.Name = "Gemini 1.5 Flash"
		baseModel.Description = "Fast and efficient Gemini model"
		baseModel.ContextSize = 32768
		baseModel.CostPer1K = 0.03

	default:
		baseModel.Description = "Model information not available"
		baseModel.ContextSize = 8192
		baseModel.CostPer1K = 0.0
	}

	return baseModel
}

func (h *ModelsHandler) getCurrentProvider(c *gin.Context) string {
	// Пытаемся получить текущий провайдер из конфигурации
	// В реальной ситуации это можно сделать через middleware или DI
	if provider, exists := c.Get("current_provider"); exists {
		if providerStr, ok := provider.(string); ok {
			return providerStr
		}
	}

	// Возвращаем значение по умолчанию
	return "unknown"
}

// POST /models/validate - валидация конфигурации провайдера
func (h *ModelsHandler) ValidateProviderConfig(c *gin.Context) {
	var req struct {
		Provider string                 `json:"provider" binding:"required"`
		Config   map[string]interface{} `json:"config" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "Invalid request format",
			Code:    "INVALID_REQUEST",
			Details: err.Error(),
		})
		return
	}

	err := h.registry.ValidateProviderConfig(req.Provider, req.Config)
	if err != nil {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "Provider configuration validation failed",
			Code:    "VALIDATION_FAILED",
			Details: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":  "Configuration is valid",
		"provider": req.Provider,
	})
}
