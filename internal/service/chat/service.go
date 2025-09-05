package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"LLM_Chat/internal/config"
	contextmgr "LLM_Chat/internal/service/context"
	"LLM_Chat/internal/storage/interfaces"
	"LLM_Chat/internal/storage/models"
	"LLM_Chat/pkg/llm"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type Service struct {
	messageStore   interfaces.MessageStore
	sessionStore   interfaces.SessionStore
	contextManager contextmgr.ContextManager
	llmClient      llm.LLMClient
	config         *config.ChatConfig
	logger         *zap.Logger
}

func NewService(
	messageStore interfaces.MessageStore,
	sessionStore interfaces.SessionStore,
	contextManager contextmgr.ContextManager,
	llmClient llm.LLMClient,
	config *config.ChatConfig,
	logger *zap.Logger,
) *Service {
	return &Service{
		messageStore:   messageStore,
		sessionStore:   sessionStore,
		contextManager: contextManager,
		llmClient:      llmClient,
		config:         config,
		logger:         logger,
	}
}

type ProcessMessageRequest struct {
	SessionID string
	Message   string
	UserID    string
}

type ProcessMessageResponse struct {
	MessageID      string
	Response       string
	SessionID      string
	TokensUsed     int
	Model          string
	ProcessingTime time.Duration
	ContextInfo    *ContextMetadata
}

type ContextMetadata struct {
	TotalMessages        int  `json:"total_messages"`
	ContextWindowUsed    int  `json:"context_window_used"`
	HasSummary           bool `json:"has_summary"`
	CompressionTriggered bool `json:"compression_triggered"`
	MessagesCompressed   int  `json:"messages_compressed,omitempty"`
}

type StreamResponse struct {
	Content     string
	Done        bool
	Error       error
	MessageID   string
	ContextInfo *ContextMetadata `json:"context_info,omitempty"`
}

// ProcessMessage обрабатывает сообщение пользователя с управлением контекстом
func (s *Service) ProcessMessage(ctx context.Context, req ProcessMessageRequest) (*ProcessMessageResponse, error) {
	startTime := time.Now()

	s.logger.Info("Processing message with context management",
		zap.String("session_id", req.SessionID),
		zap.String("user_id", req.UserID),
		zap.Int("message_length", len(req.Message)),
	)

	// 1. Валидация
	if err := ValidateProcessMessageRequest(req); err != nil {
		return nil, err
	}

	// 2. Создаём сессию если её нет
	if err := s.ensureSession(ctx, req.SessionID); err != nil {
		return nil, fmt.Errorf("failed to ensure session: %w", err)
	}

	// 3. Сохраняем сообщение пользователя
	userMessage := models.Message{
		ID:        uuid.New().String(),
		SessionID: req.SessionID,
		Role:      "user",
		Content:   req.Message,
		Timestamp: time.Now(),
	}

	if err := s.messageStore.SaveMessage(ctx, userMessage); err != nil {
		return nil, fmt.Errorf("failed to save user message: %w", err)
	}

	// 4. Строим контекст с помощью Context Manager
	contextReq := contextmgr.ContextRequest{
		SessionID:     req.SessionID,
		SystemPrompt:  s.getSystemPrompt(),
		IncludeSystem: true,
	}

	contextResp, err := s.contextManager.BuildContext(ctx, contextReq)
	if err != nil {
		return nil, fmt.Errorf("failed to build context: %w", err)
	}

	s.logger.Debug("Context built",
		zap.String("session_id", req.SessionID),
		zap.Int("total_messages", contextResp.TotalMessages),
		zap.Int("context_messages", len(contextResp.Messages)),
		zap.Bool("has_summary", contextResp.HasSummary),
		zap.Bool("compression_triggered", contextResp.SummaryUpdated),
	)

	// 5. Отправляем запрос к LLM
	llmResponse, err := s.llmClient.ChatCompletion(ctx, contextResp.Messages)
	if err != nil {
		return nil, fmt.Errorf("failed to get LLM response: %w", err)
	}

	if len(llmResponse.Choices) == 0 {
		return nil, fmt.Errorf("no choices in LLM response")
	}

	assistantContent := llmResponse.Choices[0].Message.Content

	// 6. Сохраняем ответ ассистента
	assistantMessage := models.Message{
		ID:        uuid.New().String(),
		SessionID: req.SessionID,
		Role:      "assistant",
		Content:   assistantContent,
		Timestamp: time.Now(),
		Metadata: models.Metadata{
			Tokens: llmResponse.Usage.TotalTokens,
			Model:  llmResponse.Model,
			Cost:   s.calculateCost(llmResponse.Usage.TotalTokens),
		},
	}

	if err := s.messageStore.SaveMessage(ctx, assistantMessage); err != nil {
		return nil, fmt.Errorf("failed to save assistant message: %w", err)
	}

	processingTime := time.Since(startTime)

	// 7. Формируем метаданные контекста
	contextMetadata := &ContextMetadata{
		TotalMessages:        contextResp.TotalMessages,
		ContextWindowUsed:    len(contextResp.Messages),
		HasSummary:           contextResp.HasSummary,
		CompressionTriggered: contextResp.SummaryUpdated,
	}

	if contextResp.CompressionInfo != nil && contextResp.CompressionInfo.Triggered {
		contextMetadata.MessagesCompressed = contextResp.CompressionInfo.MessagesCompressed
	}

	s.logger.Info("Message processed successfully with context",
		zap.String("session_id", req.SessionID),
		zap.String("assistant_message_id", assistantMessage.ID),
		zap.Int("tokens_used", llmResponse.Usage.TotalTokens),
		zap.Duration("processing_time", processingTime),
		zap.Bool("compression_triggered", contextMetadata.CompressionTriggered),
		zap.Int("total_messages", contextMetadata.TotalMessages),
	)

	return &ProcessMessageResponse{
		MessageID:      assistantMessage.ID,
		Response:       assistantContent,
		SessionID:      req.SessionID,
		TokensUsed:     llmResponse.Usage.TotalTokens,
		Model:          llmResponse.Model,
		ProcessingTime: processingTime,
		ContextInfo:    contextMetadata,
	}, nil
}

// ProcessMessageStream обрабатывает сообщение с потоковым ответом
func (s *Service) ProcessMessageStream(ctx context.Context, req ProcessMessageRequest) (<-chan StreamResponse, error) {
	s.logger.Info("Processing streaming message with context management",
		zap.String("session_id", req.SessionID),
		zap.String("user_id", req.UserID),
	)

	responseCh := make(chan StreamResponse, 100)

	go func() {
		defer close(responseCh)

		// 1. Валидация
		if err := ValidateProcessMessageRequest(req); err != nil {
			responseCh <- StreamResponse{Error: err}
			return
		}

		// 2. Создаём сессию если её нет
		if err := s.ensureSession(ctx, req.SessionID); err != nil {
			responseCh <- StreamResponse{Error: fmt.Errorf("failed to ensure session: %w", err)}
			return
		}

		// 3. Сохраняем сообщение пользователя
		userMessage := models.Message{
			ID:        uuid.New().String(),
			SessionID: req.SessionID,
			Role:      "user",
			Content:   req.Message,
			Timestamp: time.Now(),
		}

		if err := s.messageStore.SaveMessage(ctx, userMessage); err != nil {
			responseCh <- StreamResponse{Error: fmt.Errorf("failed to save user message: %w", err)}
			return
		}

		// 4. Строим контекст
		contextReq := contextmgr.ContextRequest{
			SessionID:     req.SessionID,
			SystemPrompt:  s.getSystemPrompt(),
			IncludeSystem: true,
		}

		contextResp, err := s.contextManager.BuildContext(ctx, contextReq)
		if err != nil {
			responseCh <- StreamResponse{Error: fmt.Errorf("failed to build context: %w", err)}
			return
		}

		// 5. Формируем метаданные контекста для отправки клиенту
		contextMetadata := &ContextMetadata{
			TotalMessages:        contextResp.TotalMessages,
			ContextWindowUsed:    len(contextResp.Messages),
			HasSummary:           contextResp.HasSummary,
			CompressionTriggered: contextResp.SummaryUpdated,
		}

		if contextResp.CompressionInfo != nil && contextResp.CompressionInfo.Triggered {
			contextMetadata.MessagesCompressed = contextResp.CompressionInfo.MessagesCompressed
		}

		// 6. Начинаем стриминговый запрос к LLM
		streamCh, err := s.llmClient.ChatCompletionStream(ctx, contextResp.Messages)
		if err != nil {
			responseCh <- StreamResponse{Error: fmt.Errorf("failed to start LLM stream: %w", err)}
			return
		}

		assistantMessageID := uuid.New().String()

		// Отправляем информацию о контексте в начале стрима
		responseCh <- StreamResponse{
			MessageID:   assistantMessageID,
			ContextInfo: contextMetadata,
		}

		// 7. Обрабатываем поток
		s.handleStreamResponseWithContext(ctx, req.SessionID, assistantMessageID, streamCh, responseCh, contextMetadata)
	}()

	return responseCh, nil
}

func (s *Service) handleStreamResponseWithContext(
	ctx context.Context,
	sessionID, assistantMessageID string,
	streamCh <-chan llm.StreamChunk,
	responseCh chan<- StreamResponse,
	contextMetadata *ContextMetadata,
) {
	var fullContent strings.Builder
	startTime := time.Now()

	for chunk := range streamCh {
		select {
		case <-ctx.Done():
			responseCh <- StreamResponse{Error: ctx.Err()}
			return
		default:
		}

		if chunk.Error != nil {
			responseCh <- StreamResponse{Error: chunk.Error}
			return
		}

		if chunk.Content != "" {
			fullContent.WriteString(chunk.Content)
			responseCh <- StreamResponse{
				Content:   chunk.Content,
				MessageID: assistantMessageID,
			}
		}

		if chunk.Done {
			// Сохраняем полный ответ ассистента
			assistantMessage := models.Message{
				ID:        assistantMessageID,
				SessionID: sessionID,
				Role:      "assistant",
				Content:   fullContent.String(),
				Timestamp: time.Now(),
				Metadata: models.Metadata{
					Model: "streamed",
				},
			}

			if err := s.messageStore.SaveMessage(ctx, assistantMessage); err != nil {
				s.logger.Error("Failed to save streamed message", zap.Error(err))
				responseCh <- StreamResponse{Error: err}
				return
			}

			s.logger.Info("Streaming message completed with context",
				zap.String("session_id", sessionID),
				zap.String("message_id", assistantMessageID),
				zap.Int("content_length", len(fullContent.String())),
				zap.Duration("duration", time.Since(startTime)),
				zap.Bool("compression_triggered", contextMetadata.CompressionTriggered),
			)

			responseCh <- StreamResponse{
				Done:      true,
				MessageID: assistantMessageID,
			}
			return
		}
	}
}

// GetContextInfo возвращает информацию о контексте сессии
func (s *Service) GetContextInfo(ctx context.Context, sessionID string) (*contextmgr.ContextInfo, error) {
	return s.contextManager.GetContextInfo(ctx, sessionID)
}

// DeleteSession удаляет сессию и очищает контекст
func (s *Service) DeleteSession(ctx context.Context, sessionID string) error {
	// Очищаем контекст (резюме и т.д.)
	if err := s.contextManager.CleanupSession(ctx, sessionID); err != nil {
		s.logger.Warn("Failed to cleanup context during session deletion",
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
	}

	// Удаляем сообщения
	if err := s.messageStore.DeleteSession(ctx, sessionID); err != nil {
		return fmt.Errorf("failed to delete messages: %w", err)
	}

	s.logger.Info("Session deleted with context cleanup",
		zap.String("session_id", sessionID))
	return nil
}

// TriggerCompression принудительно запускает сжатие контекста
func (s *Service) TriggerCompression(ctx context.Context, sessionID string) (*CompressionResult, error) {
	s.logger.Info("Manually triggering compression",
		zap.String("session_id", sessionID),
	)

	// Строим контекст, что может вызвать сжатие
	contextReq := contextmgr.ContextRequest{
		SessionID:     sessionID,
		SystemPrompt:  s.getSystemPrompt(),
		IncludeSystem: false, // Не нужен системный промпт для проверки
	}

	contextResp, err := s.contextManager.BuildContext(ctx, contextReq)
	if err != nil {
		return nil, fmt.Errorf("failed to build context for compression: %w", err)
	}

	result := &CompressionResult{
		SessionID:     sessionID,
		Triggered:     contextResp.SummaryUpdated,
		TotalMessages: contextResp.TotalMessages,
		ContextSize:   len(contextResp.Messages),
		HasSummary:    contextResp.HasSummary,
	}

	if contextResp.CompressionInfo != nil {
		result.MessagesCompressed = contextResp.CompressionInfo.MessagesCompressed
		result.AnchorsCreated = contextResp.CompressionInfo.AnchorsCreated
		result.TokensUsed = contextResp.CompressionInfo.TokensUsed
		result.Duration = contextResp.CompressionInfo.Duration
	}

	return result, nil
}

type CompressionResult struct {
	SessionID          string        `json:"session_id"`
	Triggered          bool          `json:"triggered"`
	TotalMessages      int           `json:"total_messages"`
	ContextSize        int           `json:"context_size"`
	HasSummary         bool          `json:"has_summary"`
	MessagesCompressed int           `json:"messages_compressed"`
	AnchorsCreated     int           `json:"anchors_created"`
	TokensUsed         int           `json:"tokens_used"`
	Duration           time.Duration `json:"duration"`
}

func (s *Service) getSystemPrompt() string {
	return `Ты полезный AI-ассистент. Отвечай на русском языке, если пользователь пишет на русском. 
Будь вежливым, информативным и помогай пользователю решать его задачи.
Если не знаешь ответа, честно скажи об этом.

Если в контексте есть резюме предыдущего разговора, учитывай его при формировании ответов, но не упоминай явно, что ты читаешь резюме.`
}

func (s *Service) ensureSession(ctx context.Context, sessionID string) error {
	_, err := s.sessionStore.GetSession(ctx, sessionID)
	if err != nil {
		return s.sessionStore.CreateSession(ctx, sessionID)
	}
	return nil
}

func (s *Service) calculateCost(tokens int) float64 {
	costPerToken := 0.0001
	return float64(tokens) * costPerToken
}

func (s *Service) GetHistory(ctx context.Context, sessionID string, limit int) ([]models.Message, error) {
	if limit <= 0 {
		limit = 50
	}

	messages, err := s.messageStore.GetMessages(ctx, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages: %w", err)
	}

	return messages, nil
}
