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

		// Models endpoints
		api.GET("/models", modelsHandler.GetAvailableModels)
	}

	return r
}
