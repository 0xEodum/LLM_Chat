package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ModelsHandler struct {
	logger *zap.Logger
}

func NewModelsHandler(logger *zap.Logger) *ModelsHandler {
	return &ModelsHandler{
		logger: logger,
	}
}

type ModelInfo struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Provider    string  `json:"provider"`
	Description string  `json:"description"`
	ContextSize int     `json:"context_size"`
	CostPer1K   float64 `json:"cost_per_1k_tokens"`
}

type ModelsResponse struct {
	Models  []ModelInfo `json:"models"`
	Default string      `json:"default"`
}

// GET /models - получение списка доступных моделей
func (h *ModelsHandler) GetAvailableModels(c *gin.Context) {
	// TODO: В будущем это можно получать динамически из OpenRouter API
	models := []ModelInfo{
		{
			ID:          "google/gemma-3-27b-it:free",
			Name:        "Gemma 3 27B IT (Free)",
			Provider:    "Google",
			Description: "Free tier model with good performance",
			ContextSize: 8192,
			CostPer1K:   0.0,
		},
		{
			ID:          "anthropic/claude-sonnet-4",
			Name:        "Claude Sonnet 4",
			Provider:    "Anthropic",
			Description: "High-quality model for complex tasks",
			ContextSize: 200000,
			CostPer1K:   0.15,
		},
		{
			ID:          "openai/gpt-4o",
			Name:        "GPT-4 Optimized",
			Provider:    "OpenAI",
			Description: "Optimized GPT-4 model",
			ContextSize: 128000,
			CostPer1K:   0.10,
		},
	}

	c.JSON(http.StatusOK, ModelsResponse{
		Models:  models,
		Default: "google/gemma-3-27b-it:free",
	})
}
