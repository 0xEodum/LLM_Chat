package context

import (
	"context"
	"fmt"
	"strings"
	"time"

	"LLM_Chat/internal/service/summary"
	"LLM_Chat/internal/storage/interfaces"
	"LLM_Chat/internal/storage/models"
	"LLM_Chat/pkg/llm"

	"go.uber.org/zap"
)

type Manager struct {
	messageStore   interfaces.MessageStore
	summaryService summary.SummaryService
	logger         *zap.Logger
	config         Config
}

type Config struct {
	ContextWindowSize         int     // Размер контекстного окна для LLM
	MaxMessagesBeforeCompress int     // Максимум сообщений до сжатия
	MinMessagesInWindow       int     // Минимум сообщений в окне
	SummaryTriggerRatio       float64 // Коэффициент для триггера сжатия
}

func DefaultConfig() Config {
	return Config{
		ContextWindowSize:         20,
		MaxMessagesBeforeCompress: 50,
		MinMessagesInWindow:       5,
		SummaryTriggerRatio:       0.8, // 80% от максимума
	}
}

func NewManager(
	messageStore interfaces.MessageStore,
	summaryService summary.SummaryService,
	config Config,
	logger *zap.Logger,
) *Manager {
	return &Manager{
		messageStore:   messageStore,
		summaryService: summaryService,
		config:         config,
		logger:         logger,
	}
}

type ContextRequest struct {
	SessionID     string
	SystemPrompt  string
	IncludeSystem bool
}

type ContextResponse struct {
	Messages        []llm.Message
	TotalMessages   int
	WindowSize      int
	HasSummary      bool
	SummaryUpdated  bool
	CompressionInfo *CompressionInfo
}

type CompressionInfo struct {
	Triggered          bool
	Reason             string
	MessagesCompressed int
	AnchorsCreated     int
	TokensUsed         int
	Duration           time.Duration
}

// BuildContext строит контекст для отправки в LLM
func (m *Manager) BuildContext(ctx context.Context, req ContextRequest) (*ContextResponse, error) {
	startTime := time.Now()

	m.logger.Debug("Building context",
		zap.String("session_id", req.SessionID),
		zap.Int("context_window_size", m.config.ContextWindowSize),
	)

	response := &ContextResponse{
		WindowSize: m.config.ContextWindowSize,
	}

	// 1. Получаем общее количество сообщений
	totalCount, err := m.messageStore.GetMessageCount(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message count: %w", err)
	}
	response.TotalMessages = totalCount

	m.logger.Debug("Session message count",
		zap.String("session_id", req.SessionID),
		zap.Int("total_messages", totalCount),
	)

	// 2. Проверяем, нужно ли сжатие
	compressionInfo, err := m.checkAndCompress(ctx, req.SessionID, totalCount)
	if err != nil {
		return nil, fmt.Errorf("failed to check compression: %w", err)
	}
	response.CompressionInfo = compressionInfo

	// 3. Получаем недавние сообщения
	recentMessages, err := m.messageStore.GetMessages(ctx, req.SessionID, m.config.ContextWindowSize)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent messages: %w", err)
	}

	// 4. Строим контекст с учётом резюме
	contextMessages, hasSummary, err := m.buildLLMContext(ctx, req, recentMessages)
	if err != nil {
		return nil, fmt.Errorf("failed to build LLM context: %w", err)
	}

	response.Messages = contextMessages
	response.HasSummary = hasSummary
	response.SummaryUpdated = compressionInfo.Triggered

	duration := time.Since(startTime)
	m.logger.Info("Context built",
		zap.String("session_id", req.SessionID),
		zap.Int("total_messages", totalCount),
		zap.Int("context_messages", len(contextMessages)),
		zap.Bool("has_summary", hasSummary),
		zap.Bool("compression_triggered", compressionInfo.Triggered),
		zap.Duration("duration", duration),
	)

	return response, nil
}

// checkAndCompress проверяет необходимость сжатия и выполняет его
func (m *Manager) checkAndCompress(ctx context.Context, sessionID string, messageCount int) (*CompressionInfo, error) {
	info := &CompressionInfo{}

	// Проверяем, нужно ли сжатие
	shouldCompress, reason := m.summaryService.ShouldCreateSummary(ctx, sessionID, messageCount)
	if !shouldCompress {
		return info, nil
	}

	startTime := time.Now()
	info.Triggered = true
	info.Reason = reason

	m.logger.Info("Triggering compression",
		zap.String("session_id", sessionID),
		zap.String("reason", reason),
		zap.Int("message_count", messageCount),
	)

	// Получаем сообщения для сжатия
	// Берём все сообщения кроме последних N (которые останутся в контексте)
	keepInWindow := m.config.ContextWindowSize / 2 // Оставляем половину окна
	messagesToCompress := messageCount - keepInWindow

	if messagesToCompress <= 0 {
		m.logger.Debug("Not enough messages to compress",
			zap.Int("total", messageCount),
			zap.Int("keep_in_window", keepInWindow),
		)
		return info, nil
	}

	// Получаем все сообщения для анализа
	allMessages, err := m.messageStore.GetMessages(ctx, sessionID, messageCount)
	if err != nil {
		return nil, fmt.Errorf("failed to get messages for compression: %w", err)
	}

	// Берём сообщения для сжатия (все кроме последних)
	var messagesForSummary []models.Message
	if len(allMessages) > keepInWindow {
		messagesForSummary = allMessages[:len(allMessages)-keepInWindow]
	}

	if len(messagesForSummary) == 0 {
		return info, nil
	}

	// Создаём или обновляем резюме
	summaryReq := summary.SummaryRequest{
		SessionID: sessionID,
		Messages:  messagesForSummary,
		Reason:    reason,
	}

	summaryResp, err := m.summaryService.CreateSummary(ctx, summaryReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create summary: %w", err)
	}

	info.MessagesCompressed = summaryResp.Compressed
	info.AnchorsCreated = len(summaryResp.Anchors)
	info.TokensUsed = summaryResp.TokensUsed
	info.Duration = time.Since(startTime)

	m.logger.Info("Compression completed",
		zap.String("session_id", sessionID),
		zap.Int("messages_compressed", info.MessagesCompressed),
		zap.Int("anchors_created", info.AnchorsCreated),
		zap.Int("tokens_used", info.TokensUsed),
		zap.Duration("duration", info.Duration),
	)

	return info, nil
}

// buildLLMContext строит финальный контекст для LLM
func (m *Manager) buildLLMContext(ctx context.Context, req ContextRequest, recentMessages []models.Message) ([]llm.Message, bool, error) {
	var contextMessages []llm.Message
	hasSummary := false

	// 1. Добавляем системный промпт если нужно
	if req.IncludeSystem && req.SystemPrompt != "" {
		contextMessages = append(contextMessages, llm.Message{
			Role:    "system",
			Content: req.SystemPrompt,
		})
	}

	// 2. Получаем контекст с резюме от SummaryService
	summaryContext, err := m.summaryService.GetContextForLLM(ctx, req.SessionID, recentMessages)
	if err != nil {
		// Если нет резюме, просто логируем и продолжаем
		m.logger.Debug("No summary available",
			zap.String("session_id", req.SessionID),
			zap.Error(err),
		)
	} else {
		// Проверяем, есть ли резюме в контексте
		for _, msg := range summaryContext {
			if msg.Role == "system" && strings.Contains(msg.Content, "Контекст предыдущего разговора") {
				hasSummary = true
			}
		}
		contextMessages = append(contextMessages, summaryContext...)
	}

	// 3. Если резюме не было добавлено, добавляем недавние сообщения напрямую
	if !hasSummary {
		recentLLMMessages := llm.ConvertToLLMMessages(recentMessages)
		contextMessages = append(contextMessages, recentLLMMessages...)
	}

	// 4. Обрезаем контекст до максимального размера
	contextMessages = m.trimContext(contextMessages)

	return contextMessages, hasSummary, nil
}

// trimContext обрезает контекст до максимального размера
func (m *Manager) trimContext(messages []llm.Message) []llm.Message {
	if len(messages) <= m.config.ContextWindowSize {
		return messages
	}

	// Сохраняем системные сообщения
	var systemMessages []llm.Message
	var regularMessages []llm.Message

	for _, msg := range messages {
		if msg.Role == "system" {
			systemMessages = append(systemMessages, msg)
		} else {
			regularMessages = append(regularMessages, msg)
		}
	}

	// Берём последние сообщения, учитывая место для системных
	availableSlots := m.config.ContextWindowSize - len(systemMessages)
	if availableSlots <= 0 {
		return systemMessages // Только системные сообщения
	}

	if len(regularMessages) > availableSlots {
		regularMessages = regularMessages[len(regularMessages)-availableSlots:]
	}

	// Объединяем системные и обрезанные обычные сообщения
	result := make([]llm.Message, 0, len(systemMessages)+len(regularMessages))
	result = append(result, systemMessages...)
	result = append(result, regularMessages...)

	return result
}

// GetContextInfo возвращает информацию о текущем контексте
func (m *Manager) GetContextInfo(ctx context.Context, sessionID string) (*ContextInfo, error) {
	totalCount, err := m.messageStore.GetMessageCount(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message count: %w", err)
	}

	summary, err := m.summaryService.GetSummary(ctx, sessionID)
	hasSummary := err == nil && summary != nil

	var summaryInfo *SummaryInfo
	if hasSummary {
		summaryInfo = &SummaryInfo{
			AnchorsCount:  len(summary.Anchors),
			SummaryLength: len(summary.BriefSummary),
			LastUpdated:   summary.UpdatedAt,
		}
	}

	shouldCompress, reason := m.summaryService.ShouldCreateSummary(ctx, sessionID, totalCount)

	return &ContextInfo{
		SessionID:         sessionID,
		TotalMessages:     totalCount,
		ContextWindowSize: m.config.ContextWindowSize,
		MaxBeforeCompress: m.config.MaxMessagesBeforeCompress,
		HasSummary:        hasSummary,
		SummaryInfo:       summaryInfo,
		ShouldCompress:    shouldCompress,
		CompressionReason: reason,
		CompressionRatio:  float64(totalCount) / float64(m.config.MaxMessagesBeforeCompress),
	}, nil
}

type ContextInfo struct {
	SessionID         string       `json:"session_id"`
	TotalMessages     int          `json:"total_messages"`
	ContextWindowSize int          `json:"context_window_size"`
	MaxBeforeCompress int          `json:"max_before_compress"`
	HasSummary        bool         `json:"has_summary"`
	SummaryInfo       *SummaryInfo `json:"summary_info,omitempty"`
	ShouldCompress    bool         `json:"should_compress"`
	CompressionReason string       `json:"compression_reason,omitempty"`
	CompressionRatio  float64      `json:"compression_ratio"`
}

type SummaryInfo struct {
	AnchorsCount  int       `json:"anchors_count"`
	SummaryLength int       `json:"summary_length"`
	LastUpdated   time.Time `json:"last_updated"`
}

// CleanupSession очищает контекст сессии
func (m *Manager) CleanupSession(ctx context.Context, sessionID string) error {
	// Удаляем резюме
	if err := m.summaryService.DeleteSummary(ctx, sessionID); err != nil {
		m.logger.Warn("Failed to delete summary during cleanup",
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
	}

	return nil
}
