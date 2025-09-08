// internal/api/routes/routes.go
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
		c.Set("current_provider", cfg.LLM.Provider)
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
			// Получение информации о всех доступных провайдерах и моделях
			models.GET("", modelsHandler.GetAvailableModels)

			// Получение моделей конкретного провайдера
			models.GET("/:provider", modelsHandler.GetProviderModels)

			// Валидация конфигурации провайдера
			models.POST("/validate", modelsHandler.ValidateProviderConfig)
		}

		// Provider information endpoints
		providers := api.Group("/providers")
		{
			// Получение списка всех поддерживаемых провайдеров
			providers.GET("", func(c *gin.Context) {
				// Возвращаем список поддерживаемых провайдеров
				supportedProviders := []string{"openrouter", "gemini"}
				currentProvider := cfg.LLM.Provider

				c.JSON(200, gin.H{
					"current":   currentProvider,
					"supported": supportedProviders,
					"default":   "openrouter",
				})
			})

			// Получение информации о текущем провайдере
			providers.GET("/current", func(c *gin.Context) {
				c.JSON(200, gin.H{
					"provider": cfg.LLM.Provider,
					"model":    cfg.LLM.Model,
					"base_url": cfg.LLM.BaseURL,
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
						"provider": cfg.LLM.Provider,
						"model":    cfg.LLM.Model,
						"base_url": cfg.LLM.BaseURL,
						// НЕ включаем API ключ в ответ
					},
					"sources": configSources,
				})
			})

			// Получение рекомендуемых переменных окружения
			configep.GET("/env-vars", func(c *gin.Context) {
				provider := c.DefaultQuery("provider", cfg.LLM.Provider)
				envVars := config.GetProviderSpecificEnvVars(provider)

				c.JSON(200, gin.H{
					"provider": provider,
					"env_vars": envVars,
					"example": map[string]string{
						"openrouter": "export CHAT_LLM_LLM_API_KEY=your_openrouter_key",
						"gemini":     "export CHAT_LLM_GEMINI_API_KEY=your_gemini_key",
					}[provider],
				})
			})
		}
	}

	return r
}
