package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"LLM_Chat/internal/service/chat"
	contextmgr "LLM_Chat/internal/service/context"
	"LLM_Chat/internal/storage/interfaces"
	"LLM_Chat/internal/storage/models"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ChatHandler struct {
	chatService  chat.ChatService
	sessionStore interfaces.SessionStore
	logger       *zap.Logger
}

func NewChatHandler(
	chatService chat.ChatService,
	sessionStore interfaces.SessionStore,
	logger *zap.Logger,
) *ChatHandler {
	return &ChatHandler{
		chatService:  chatService,
		sessionStore: sessionStore,
		logger:       logger,
	}
}

type ChatRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	Message   string `json:"message" binding:"required"`
	Stream    bool   `json:"stream,omitempty"`
	UserID    string `json:"user_id,omitempty"`
}

type ChatResponse struct {
	MessageID      string                `json:"message_id"`
	Response       string                `json:"response"`
	SessionID      string                `json:"session_id"`
	TokensUsed     int                   `json:"tokens_used"`
	Model          string                `json:"model"`
	ProcessingTime string                `json:"processing_time"`
	Cost           float64               `json:"cost,omitempty"`
	ContextInfo    *chat.ContextMetadata `json:"context_info,omitempty"`
}

type HistoryResponse struct {
	SessionID string           `json:"session_id"`
	Messages  []models.Message `json:"messages"`
	Total     int              `json:"total"`
}

type SessionResponse struct {
	SessionID   string                  `json:"session_id"`
	Session     *models.ChatSession     `json:"session"`
	ContextInfo *contextmgr.ContextInfo `json:"context_info,omitempty"`
}

type ErrorResponse struct {
	Error   string `json:"error"`
	Code    string `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}

// POST /chat - основной эндпоинт для отправки сообщений
func (h *ChatHandler) SendMessage(c *gin.Context) {
	var req ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.logger.Error("Invalid request", zap.Error(err))
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "Invalid request format",
			Code:    "INVALID_REQUEST",
			Details: err.Error(),
		})
		return
	}

	// Валидация запроса
	if err := chat.ValidateProcessMessageRequest(chat.ProcessMessageRequest{
		SessionID: req.SessionID,
		Message:   req.Message,
		UserID:    req.UserID,
	}); err != nil {
		h.logger.Error("Request validation failed", zap.Error(err))
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "Validation failed",
			Code:    "VALIDATION_ERROR",
			Details: err.Error(),
		})
		return
	}

	// Определяем, нужен ли стриминг
	if req.Stream {
		h.handleStreamingMessage(c, req)
		return
	}

	h.handleRegularMessage(c, req)
}

func (h *ChatHandler) handleRegularMessage(c *gin.Context, req ChatRequest) {
	serviceReq := chat.ProcessMessageRequest{
		SessionID: req.SessionID,
		Message:   req.Message,
		UserID:    req.UserID,
	}

	resp, err := h.chatService.ProcessMessage(c.Request.Context(), serviceReq)
	if err != nil {
		h.logger.Error("Failed to process message",
			zap.Error(err),
			zap.String("session_id", req.SessionID),
		)

		statusCode := http.StatusInternalServerError
		errorCode := "PROCESSING_ERROR"

		if strings.Contains(err.Error(), "context") {
			statusCode = http.StatusRequestTimeout
			errorCode = "TIMEOUT"
		} else if strings.Contains(err.Error(), "API") {
			statusCode = http.StatusBadGateway
			errorCode = "LLM_API_ERROR"
		}

		c.JSON(statusCode, ErrorResponse{
			Error:   "Failed to process message",
			Code:    errorCode,
			Details: err.Error(),
		})
		return
	}

	h.logger.Info("Message processed successfully",
		zap.String("session_id", req.SessionID),
		zap.String("message_id", resp.MessageID),
		zap.Int("tokens_used", resp.TokensUsed),
		zap.Duration("processing_time", resp.ProcessingTime),
		zap.Bool("compression_triggered", resp.ContextInfo.CompressionTriggered),
	)

	c.JSON(http.StatusOK, ChatResponse{
		MessageID:      resp.MessageID,
		Response:       resp.Response,
		SessionID:      resp.SessionID,
		TokensUsed:     resp.TokensUsed,
		Model:          resp.Model,
		ProcessingTime: resp.ProcessingTime.String(),
		ContextInfo:    resp.ContextInfo,
	})
}

func (h *ChatHandler) handleStreamingMessage(c *gin.Context, req ChatRequest) {
	// Настройка Server-Sent Events
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Headers", "Cache-Control")

	serviceReq := chat.ProcessMessageRequest{
		SessionID: req.SessionID,
		Message:   req.Message,
		UserID:    req.UserID,
	}

	streamCh, err := h.chatService.ProcessMessageStream(c.Request.Context(), serviceReq)
	if err != nil {
		h.logger.Error("Failed to start streaming", zap.Error(err))
		h.writeSSEError(c, "Failed to start streaming", err.Error())
		return
	}

	// Обрабатываем поток
	var contextInfoSent bool
	for streamResp := range streamCh {
		if streamResp.Error != nil {
			h.logger.Error("Stream error", zap.Error(streamResp.Error))
			h.writeSSEError(c, "Stream error", streamResp.Error.Error())
			return
		}

		// Отправляем контекстную информацию в начале (только один раз)
		if streamResp.ContextInfo != nil && !contextInfoSent {
			h.writeSSEEvent(c, "context", map[string]interface{}{
				"session_id":   req.SessionID,
				"message_id":   streamResp.MessageID,
				"context_info": streamResp.ContextInfo,
			})
			contextInfoSent = true
		}

		if streamResp.Content != "" {
			h.writeSSEEvent(c, "content", map[string]interface{}{
				"content":    streamResp.Content,
				"message_id": streamResp.MessageID,
			})
		}

		if streamResp.Done {
			h.writeSSEEvent(c, "done", map[string]interface{}{
				"message_id": streamResp.MessageID,
			})
			return
		}

		// Принудительно отправляем данные клиенту
		if flusher, ok := c.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func (h *ChatHandler) writeSSEEvent(c *gin.Context, eventType string, data interface{}) {
	c.SSEvent(eventType, data)
}

func (h *ChatHandler) writeSSEError(c *gin.Context, message, details string) {
	c.SSEvent("error", map[string]interface{}{
		"error":   message,
		"details": details,
	})
}

// GET /chat/:session_id/history - получение истории сообщений
func (h *ChatHandler) GetHistory(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: "session_id is required",
			Code:  "MISSING_SESSION_ID",
		})
		return
	}

	limitStr := c.DefaultQuery("limit", "50")
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit < 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	messages, err := h.chatService.GetHistory(c.Request.Context(), sessionID, limit)
	if err != nil {
		h.logger.Error("Failed to get messages",
			zap.Error(err),
			zap.String("session_id", sessionID),
		)
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error:   "Failed to get messages",
			Code:    "HISTORY_ERROR",
			Details: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, HistoryResponse{
		SessionID: sessionID,
		Messages:  messages,
		Total:     len(messages),
	})
}

// GET /chat/:session_id - получение информации о сессии
func (h *ChatHandler) GetSession(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: "session_id is required",
			Code:  "MISSING_SESSION_ID",
		})
		return
	}

	session, err := h.sessionStore.GetSession(c.Request.Context(), sessionID)
	if err != nil {
		h.logger.Error("Failed to get session",
			zap.Error(err),
			zap.String("session_id", sessionID),
		)
		c.JSON(http.StatusNotFound, ErrorResponse{
			Error: "Session not found",
			Code:  "SESSION_NOT_FOUND",
		})
		return
	}

	// Получаем информацию о контексте
	contextInfo, err := h.chatService.GetContextInfo(c.Request.Context(), sessionID)
	if err != nil {
		h.logger.Warn("Failed to get context info",
			zap.Error(err),
			zap.String("session_id", sessionID),
		)
		// Не возвращаем ошибку, просто не включаем контекстную информацию
	}

	c.JSON(http.StatusOK, SessionResponse{
		SessionID:   sessionID,
		Session:     session,
		ContextInfo: contextInfo,
	})
}

// GET /chat/:session_id/context - получение информации о контексте
func (h *ChatHandler) GetContextInfo(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: "session_id is required",
			Code:  "MISSING_SESSION_ID",
		})
		return
	}

	contextInfo, err := h.chatService.GetContextInfo(c.Request.Context(), sessionID)
	if err != nil {
		h.logger.Error("Failed to get context info",
			zap.Error(err),
			zap.String("session_id", sessionID),
		)
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error:   "Failed to get context info",
			Code:    "CONTEXT_ERROR",
			Details: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, contextInfo)
}

// POST /chat/:session_id/compress - принудительное сжатие контекста
func (h *ChatHandler) TriggerCompression(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: "session_id is required",
			Code:  "MISSING_SESSION_ID",
		})
		return
	}

	result, err := h.chatService.TriggerCompression(c.Request.Context(), sessionID)
	if err != nil {
		h.logger.Error("Failed to trigger compression",
			zap.Error(err),
			zap.String("session_id", sessionID),
		)
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error:   "Failed to trigger compression",
			Code:    "COMPRESSION_ERROR",
			Details: err.Error(),
		})
		return
	}

	h.logger.Info("Compression triggered manually",
		zap.String("session_id", sessionID),
		zap.Bool("triggered", result.Triggered),
		zap.Int("messages_compressed", result.MessagesCompressed),
	)

	c.JSON(http.StatusOK, gin.H{
		"message": "Compression check completed",
		"result":  result,
	})
}

// DELETE /chat/:session_id - удаление сессии
func (h *ChatHandler) DeleteSession(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: "session_id is required",
			Code:  "MISSING_SESSION_ID",
		})
		return
	}

	if err := h.chatService.DeleteSession(c.Request.Context(), sessionID); err != nil {
		h.logger.Error("Failed to delete session",
			zap.Error(err),
			zap.String("session_id", sessionID),
		)
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error:   "Failed to delete session",
			Code:    "DELETE_ERROR",
			Details: err.Error(),
		})
		return
	}

	h.logger.Info("Session deleted with context cleanup", zap.String("session_id", sessionID))
	c.JSON(http.StatusOK, gin.H{
		"message":    "Session deleted successfully",
		"session_id": sessionID,
	})
}

// POST /chat/:session_id/clear - очистка истории сессии
func (h *ChatHandler) ClearSession(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, ErrorResponse{
			Error: "session_id is required",
			Code:  "MISSING_SESSION_ID",
		})
		return
	}

	if err := h.chatService.DeleteSession(c.Request.Context(), sessionID); err != nil {
		h.logger.Error("Failed to clear session",
			zap.Error(err),
			zap.String("session_id", sessionID),
		)
		c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error:   "Failed to clear session",
			Code:    "CLEAR_ERROR",
			Details: err.Error(),
		})
		return
	}

	h.logger.Info("Session cleared with context cleanup", zap.String("session_id", sessionID))
	c.JSON(http.StatusOK, gin.H{
		"message":    "Session cleared successfully",
		"session_id": sessionID,
	})
}
