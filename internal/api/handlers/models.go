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
	HasMCP      bool    `json:"has_mcp"`
}

type ProviderInfo struct {
	Name            string      `json:"name"`
	Description     string      `json:"description"`
	SupportedModels []ModelInfo `json:"supported_models"`
	RequiredConfig  []string    `json:"required_config"`
	Features        []string    `json:"features"`
}

type ModelsResponse struct {
	CurrentProvider    string         `json:"current_provider"`
	AvailableProviders []ProviderInfo `json:"available_providers"`
	SupportedProviders []string       `json:"supported_providers"`
	MCPInfo            MCPInfo        `json:"mcp_info"`
}

type MCPInfo struct {
	Enabled     bool   `json:"enabled"`
	Description string `json:"description"`
	ServerURL   string `json:"server_url,omitempty"`
}

// GET /models - получение информации о доступных провайдерах и моделях
func (h *ModelsHandler) GetAvailableModels(c *gin.Context) {
	// Получаем информацию о Gemini MCP провайдере
	providerInfos := h.registry.GetAvailableProviders()

	var availableProviders []ProviderInfo

	for _, info := range providerInfos {
		// Конвертируем модели в наш формат
		var models []ModelInfo
		for _, modelID := range info.SupportedModels {
			model := h.getGeminiModelDetails(modelID)
			models = append(models, model)
		}

		providerInfo := ProviderInfo{
			Name:            info.Name,
			Description:     info.Description,
			SupportedModels: models,
			RequiredConfig:  info.RequiredConfig,
			Features: []string{
				"Tool calling via MCP",
				"Multi-modal support",
				"Advanced reasoning",
				"Large context window",
			},
		}
		availableProviders = append(availableProviders, providerInfo)
	}

	// Получаем текущий провайдер из конфигурации
	currentProvider := h.getCurrentProvider(c)

	response := ModelsResponse{
		CurrentProvider:    currentProvider,
		AvailableProviders: availableProviders,
		SupportedProviders: []string{"gemini"},
		MCPInfo: MCPInfo{
			Enabled:     true,
			Description: "Model Context Protocol enables advanced tool integration and enhanced AI capabilities",
			ServerURL:   h.getMCPServerURL(c),
		},
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

	if providerName != "gemini" {
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error:   "provider not found",
			Code:    "PROVIDER_NOT_FOUND",
			Details: "Only 'gemini' provider is supported",
		})
		return
	}

	// Получаем информацию о Gemini MCP провайдере
	providerInfos := h.registry.GetAvailableProviders()
	targetProvider := providerInfos[0] // У нас только один провайдер

	// Конвертируем модели в детальный формат
	var models []ModelInfo
	for _, modelID := range targetProvider.SupportedModels {
		model := h.getGeminiModelDetails(modelID)
		models = append(models, model)
	}

	response := ProviderInfo{
		Name:            targetProvider.Name,
		Description:     targetProvider.Description,
		SupportedModels: models,
		RequiredConfig:  targetProvider.RequiredConfig,
		Features: []string{
			"Tool calling via MCP",
			"Multi-modal support",
			"Advanced reasoning",
			"Large context window",
		},
	}

	c.JSON(http.StatusOK, response)
}

func (h *ModelsHandler) getGeminiModelDetails(modelID string) ModelInfo {
	baseModel := ModelInfo{
		ID:       modelID,
		Name:     modelID,
		Provider: "gemini",
		HasMCP:   true,
	}

	// Детали для различных моделей Gemini
	switch modelID {
	case "gemini-2.5-flash":
		baseModel.Name = "Gemini 2.5 Flash"
		baseModel.Description = "Latest ultra-fast Gemini model with enhanced MCP capabilities"
		baseModel.ContextSize = 32768
		baseModel.CostPer1K = 0.04
	case "gemini-2.0-flash":
		baseModel.Name = "Gemini 2.0 Flash"
		baseModel.Description = "Fast Gemini model with multimodal capabilities and MCP support"
		baseModel.ContextSize = 32768
		baseModel.CostPer1K = 0.05
	case "gemini-1.5-pro":
		baseModel.Name = "Gemini 1.5 Pro"
		baseModel.Description = "High-performance Gemini model with extensive context and MCP integration"
		baseModel.ContextSize = 128000
		baseModel.CostPer1K = 0.12
	case "gemini-1.5-flash":
		baseModel.Name = "Gemini 1.5 Flash"
		baseModel.Description = "Efficient Gemini model optimized for speed with MCP tool support"
		baseModel.ContextSize = 32768
		baseModel.CostPer1K = 0.03
	default:
		baseModel.Description = "Gemini model with MCP support"
		baseModel.ContextSize = 32768
		baseModel.CostPer1K = 0.05
	}

	return baseModel
}

func (h *ModelsHandler) getCurrentProvider(c *gin.Context) string {
	// Пытаемся получить текущий провайдер из конфигурации
	if provider, exists := c.Get("current_provider"); exists {
		if providerStr, ok := provider.(string); ok {
			return providerStr
		}
	}

	// Возвращаем Gemini как единственный поддерживаемый провайдер
	return "gemini"
}

func (h *ModelsHandler) getMCPServerURL(c *gin.Context) string {
	// Можно добавить получение из конфигурации через контекст
	// Пока возвращаем значение по умолчанию
	return "http://localhost:8000/mcp"
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

	if req.Provider != "gemini" {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "Unsupported provider",
			Code:    "UNSUPPORTED_PROVIDER",
			Details: "Only 'gemini' provider is supported",
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
		"message":  "Gemini MCP configuration is valid",
		"provider": req.Provider,
		"features": []string{
			"MCP tool integration",
			"Advanced reasoning",
			"Multi-modal support",
		},
	})
}
