package middleware

import (
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ProviderInfoMiddleware добавляет информацию о провайдере в контекст
func ProviderInfoMiddleware(provider, model string, logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("current_provider", provider)
		c.Set("current_model", model)

		// Логируем использование провайдера для мониторинга
		logger.Debug("Request with provider info",
			zap.String("provider", provider),
			zap.String("model", model),
			zap.String("path", c.Request.URL.Path),
		)

		c.Next()
	}
}
