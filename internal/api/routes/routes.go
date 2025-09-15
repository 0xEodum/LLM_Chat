package routes

import (
	"LLM_Chat/internal/api/handlers"
	"LLM_Chat/internal/api/middleware"
	"LLM_Chat/internal/config"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func SetupRoutes(
	cfg *config.Config,
	logger *zap.Logger,
	chatHandler *handlers.ChatHandler,
	summaryHandler *handlers.SummaryHandler,
	healthHandler *handlers.HealthHandler,
	modelsHandler *handlers.ModelsHandler,
) *gin.Engine {

	// Настройка Gin mode
	if cfg.Logging.Level == "debug" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()

	// Middleware
	r.Use(gin.Recovery())
	r.Use(middleware.CORSMiddleware())
	r.Use(middleware.LoggingMiddleware(logger))
	r.Use(middleware.TimeoutMiddleware(cfg.Server.ReadTimeout))

	// Добавляем информацию о текущем провайдере в контекст
	r.Use(func(c *gin.Context) {
		c.Set("current_provider", "gemini")
		c.Set("mcp_enabled", true)
		c.Set("mcp_server_url", cfg.MCP.ServerURL)
		c.Next()
	})

	// Health check
	r.GET("/health", healthHandler.Check)

	// API routes
	api := r.Group("/api/v1")
	{
		// Chat endpoints
		chat := api.Group("/chat")
		{
			// Основные операции с чатом
			chat.POST("", chatHandler.SendMessage)

			// Операции с сессиями
			chat.GET("/:session_id", chatHandler.GetSession)
			chat.DELETE("/:session_id", chatHandler.DeleteSession)
			chat.POST("/:session_id/clear", chatHandler.ClearSession)

			// История сообщений
			chat.GET("/:session_id/history", chatHandler.GetHistory)

			// Управление контекстом
			chat.GET("/:session_id/context", chatHandler.GetContextInfo)
			chat.POST("/:session_id/compress", chatHandler.TriggerCompression)

			// Операции с резюме
			chat.GET("/:session_id/summary", summaryHandler.GetSummary)
			chat.DELETE("/:session_id/summary", summaryHandler.DeleteSummary)
		}

		// Models and Providers endpoints
		models := api.Group("/models")
		{
			// Получение информации о доступных моделях
			models.GET("", modelsHandler.GetAvailableModels)

			// Получение моделей Gemini провайдера
			models.GET("/gemini", modelsHandler.GetProviderModels)

			// Валидация конфигурации провайдера
			models.POST("/validate", modelsHandler.ValidateProviderConfig)
		}

		// Provider information endpoints
		providers := api.Group("/providers")
		{
			// Получение информации о поддерживаемых провайдерах
			providers.GET("", func(c *gin.Context) {
				c.JSON(200, gin.H{
					"current":   "gemini",
					"supported": []string{"gemini"},
					"default":   "gemini",
					"features": map[string]interface{}{
						"mcp_enabled":   true,
						"tool_calling":  true,
						"multimodal":    true,
						"large_context": true,
					},
				})
			})

			// Получение информации о текущем провайдере
			providers.GET("/current", func(c *gin.Context) {
				c.JSON(200, gin.H{
					"provider":    "gemini",
					"model":       cfg.LLM.Model,
					"description": "Google Gemini with MCP tool integration",
					"mcp": gin.H{
						"enabled":            true,
						"server_url":         cfg.MCP.ServerURL,
						"system_prompt_path": cfg.MCP.SystemPromptPath,
						"max_iterations":     cfg.MCP.MaxIterations,
					},
				})
			})
		}

		// MCP specific endpoints
		mcp := api.Group("/mcp")
		{
			// Получение информации о MCP сервере
			mcp.GET("/info", func(c *gin.Context) {
				c.JSON(200, gin.H{
					"enabled":            true,
					"server_url":         cfg.MCP.ServerURL,
					"system_prompt_path": cfg.MCP.SystemPromptPath,
					"max_iterations":     cfg.MCP.MaxIterations,
					"description":        "Model Context Protocol integration for enhanced AI capabilities",
				})
			})

			// Проверка статуса MCP соединения
			mcp.GET("/status", func(c *gin.Context) {
				// Здесь можно добавить проверку доступности MCP сервера
				c.JSON(200, gin.H{
					"status":     "configured", // можно расширить до проверки связи
					"server_url": cfg.MCP.ServerURL,
					"message":    "MCP server is configured and ready",
				})
			})
		}

		// Config endpoints (для отладки и мониторинга)
		configep := api.Group("/config")
		{
			// Получение информации о конфигурации (без секретов)
			configep.GET("/info", func(c *gin.Context) {
				configSources := config.GetConfigSource(cfg)

				c.JSON(200, gin.H{
					"server": gin.H{
						"host": cfg.Server.Host,
						"port": cfg.Server.Port,
					},
					"chat": gin.H{
						"max_messages_per_session": cfg.Chat.MaxMessagesPerSession,
						"context_window_size":      cfg.Chat.ContextWindowSize,
					},
					"llm": gin.H{
						"provider": "gemini",
						"model":    cfg.LLM.Model,
						// НЕ включаем API ключ в ответ
					},
					"mcp": gin.H{
						"server_url":         cfg.MCP.ServerURL,
						"system_prompt_path": cfg.MCP.SystemPromptPath,
						"max_iterations":     cfg.MCP.MaxIterations,
					},
					"sources": configSources,
				})
			})

			// Получение рекомендуемых переменных окружения
			configep.GET("/env-vars", func(c *gin.Context) {
				geminiEnvVars := config.GetGeminiEnvVars()
				mcpEnvVars := config.GetMCPEnvVars()

				c.JSON(200, gin.H{
					"provider":        "gemini",
					"gemini_env_vars": geminiEnvVars,
					"mcp_env_vars":    mcpEnvVars,
					"examples": gin.H{
						"gemini_api_key": "export CHAT_LLM_GEMINI_API_KEY=your_gemini_key",
						"mcp_server":     "export CHAT_LLM_MCP_SERVER_URL=http://localhost:8000/mcp",
						"system_prompt":  "export CHAT_LLM_MCP_SYSTEM_PROMPT_PATH=./system_prompt.txt",
					},
				})
			})
		}
	}

	return r
}
