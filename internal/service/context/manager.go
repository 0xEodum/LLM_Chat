package context

import (
	"context"
	"fmt"
	"time"

	"LLM_Chat/internal/service/summary"
	"LLM_Chat/internal/storage/interfaces"
	"LLM_Chat/internal/storage/models"
	"LLM_Chat/pkg/llm"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type Manager struct {
	messageStore   interfaces.ExtendedMessageStore
	summaryService summary.SummaryService
	logger         *zap.Logger
	config         Config
}

type Config struct {
	ContextWindowSize         int     // Размер контекстного окна для LLM
	MaxMessagesBeforeCompress int     // Максимум сообщений до сжатия
	MinMessagesInWindow       int     // Минимум сообщений в окне
	MessageCompressionRatio   float64 // Коэффициент для сжатия сообщений (30%)
	SummaryCompressionRatio   float64 // Коэффициент для сжатия резюме (80%)
}

func DefaultConfig() Config {
	return Config{
		ContextWindowSize:         20,
		MaxMessagesBeforeCompress: 50,
		MinMessagesInWindow:       5,
		MessageCompressionRatio:   0.3, // 30% от окна контекста
		SummaryCompressionRatio:   0.8, // 80% от окна контекста
	}
}

func NewManager(
	messageStore interfaces.ExtendedMessageStore,
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
	Triggered           bool
	Reason              string
	Level               int // 1 = message compression, 2 = summary compression
	MessagesCompressed  int
	SummariesCompressed int
	AnchorsCreated      int
	TokensUsed          int
	Duration            time.Duration
}

// BuildContext строит контекст для отправки в LLM с многоуровневым сжатием
func (m *Manager) BuildContext(ctx context.Context, req ContextRequest) (*ContextResponse, error) {
	startTime := time.Now()

	m.logger.Debug("Building context with multi-level compression",
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

	m.logger.Debug("Session statistics",
		zap.String("session_id", req.SessionID),
		zap.Int("total_messages", totalCount),
	)

	// 2. Проверяем необходимость сжатия (двухуровневая проверка)
	compressionInfo, err := m.checkAndCompress(ctx, req.SessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to check compression: %w", err)
	}
	response.CompressionInfo = compressionInfo

	// 3. Собираем финальный контекст для LLM
	contextMessages, hasSummary, err := m.buildLLMContext(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to build LLM context: %w", err)
	}

	response.Messages = contextMessages
	response.HasSummary = hasSummary
	response.SummaryUpdated = compressionInfo.Triggered

	duration := time.Since(startTime)
	m.logger.Info("Context built with multi-level compression",
		zap.String("session_id", req.SessionID),
		zap.Int("total_messages", totalCount),
		zap.Int("context_messages", len(contextMessages)),
		zap.Bool("has_summary", hasSummary),
		zap.Bool("compression_triggered", compressionInfo.Triggered),
		zap.Int("compression_level", compressionInfo.Level),
		zap.Duration("duration", duration),
	)

	return response, nil
}

// checkAndCompress проверяет необходимость сжатия на обоих уровнях
func (m *Manager) checkAndCompress(ctx context.Context, sessionID string) (*CompressionInfo, error) {
	info := &CompressionInfo{}

	// Получаем текущее состояние контекста
	activeMessages, err := m.messageStore.GetActiveMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get active messages: %w", err)
	}

	activeSummaries, err := m.messageStore.GetActiveSummaries(ctx, sessionID, 1) // level 1 summaries
	if err != nil {
		return nil, fmt.Errorf("failed to get active summaries: %w", err)
	}

	bulkSummaries, err := m.messageStore.GetSummariesByLevel(ctx, sessionID, 2) // level 2 summaries
	if err != nil {
		return nil, fmt.Errorf("failed to get bulk summaries: %w", err)
	}

	// Проверяем сжатие второго уровня (summaries -> bulk summaries)
	summaryCompressionRatio := float64(len(activeSummaries)) / float64(m.config.ContextWindowSize)
	if len(activeSummaries) > 0 && summaryCompressionRatio > m.config.SummaryCompressionRatio {
		m.logger.Info("Triggering level 2 compression (summaries -> bulk summary)",
			zap.String("session_id", sessionID),
			zap.Int("active_summaries", len(activeSummaries)),
			zap.Float64("compression_ratio", summaryCompressionRatio),
		)

		compressionResult, err := m.compressSummaries(ctx, sessionID, activeSummaries)
		if err != nil {
			return nil, fmt.Errorf("failed to compress summaries: %w", err)
		}

		info.Triggered = true
		info.Reason = "summary_compression"
		info.Level = 2
		info.SummariesCompressed = compressionResult.SummariesCompressed
		//info.AnchorsCreated = compressionResult.AnchorsCreated
		info.TokensUsed = compressionResult.TokensUsed
		info.Duration = compressionResult.Duration

		return info, nil
	}

	// Проверяем сжатие первого уровня (messages -> summaries)
	messageCompressionRatio := float64(len(activeMessages)) / float64(m.config.ContextWindowSize)
	if len(activeMessages) > 0 && messageCompressionRatio > m.config.MessageCompressionRatio {
		m.logger.Info("Triggering level 1 compression (messages -> summary)",
			zap.String("session_id", sessionID),
			zap.Int("active_messages", len(activeMessages)),
			zap.Float64("compression_ratio", messageCompressionRatio),
		)

		compressionResult, err := m.compressMessages(ctx, sessionID, activeMessages)
		if err != nil {
			return nil, fmt.Errorf("failed to compress messages: %w", err)
		}

		info.Triggered = true
		info.Reason = "message_compression"
		info.Level = 1
		info.MessagesCompressed = compressionResult.MessagesCompressed
		//info.AnchorsCreated = compressionResult.AnchorsCreated
		info.TokensUsed = compressionResult.TokensUsed
		info.Duration = compressionResult.Duration

		return info, nil
	}

	m.logger.Debug("No compression needed",
		zap.String("session_id", sessionID),
		zap.Int("active_messages", len(activeMessages)),
		zap.Int("active_summaries", len(activeSummaries)),
		zap.Int("bulk_summaries", len(bulkSummaries)),
		zap.Float64("message_ratio", messageCompressionRatio),
		zap.Float64("summary_ratio", summaryCompressionRatio),
	)

	return info, nil
}

// compressMessages сжимает обычные сообщения в резюме первого уровня
func (m *Manager) compressMessages(ctx context.Context, sessionID string, messages []models.Message) (*summary.SummaryResponse, error) {
	startTime := time.Now()

	// Оставляем последние сообщения несжатыми
	keepCount := int(float64(m.config.ContextWindowSize) * (1.0 - m.config.MessageCompressionRatio))
	if keepCount < m.config.MinMessagesInWindow {
		keepCount = m.config.MinMessagesInWindow
	}

	if len(messages) <= keepCount {
		return &summary.SummaryResponse{}, nil // Недостаточно сообщений для сжатия
	}

	messagesToCompress := messages[:len(messages)-keepCount]

	m.logger.Info("Compressing messages to summary",
		zap.String("session_id", sessionID),
		zap.Int("total_messages", len(messages)),
		zap.Int("compress_count", len(messagesToCompress)),
		zap.Int("keep_count", keepCount),
	)

	// Создаем резюме через SummaryService
	summaryReq := summary.SummaryRequest{
		SessionID:    sessionID,
		Messages:     messagesToCompress,
		Reason:       "message_compression",
		SummaryLevel: 1, // Regular summary
	}

	summaryResp, err := m.summaryService.CreateSummary(ctx, summaryReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create message summary: %w", err)
	}

	// Создаем summary message для хранения в БД
	summaryMessage := models.NewSummaryMessage(sessionID, summaryResp.BriefSummary, 1)
	summaryMessage.ID = uuid.New().String()

	if err := m.messageStore.SaveMessage(ctx, summaryMessage); err != nil {
		return nil, fmt.Errorf("failed to save summary message: %w", err)
	}

	// Помечаем исходные сообщения как сжатые
	messageIDs := make([]string, len(messagesToCompress))
	for i, msg := range messagesToCompress {
		messageIDs[i] = msg.ID
	}

	if err := m.messageStore.MarkMessagesAsCompressed(ctx, messageIDs, summaryResp.SummaryID); err != nil {
		return nil, fmt.Errorf("failed to mark messages as compressed: %w", err)
	}

	summaryResp.Duration = time.Since(startTime)

	m.logger.Info("Message compression completed",
		zap.String("session_id", sessionID),
		zap.Int("messages_compressed", len(messagesToCompress)),
		zap.String("summary_id", summaryResp.SummaryID),
		zap.Duration("duration", summaryResp.Duration),
	)

	return summaryResp, nil
}

// compressSummaries сжимает резюме первого уровня в bulk summary
func (m *Manager) compressSummaries(ctx context.Context, sessionID string, summaries []models.Summary) (*summary.SummaryResponse, error) {
	startTime := time.Now()

	// Оставляем последние резюме несжатыми
	keepCount := int(float64(m.config.ContextWindowSize) * (1.0 - m.config.SummaryCompressionRatio))
	if keepCount < 2 { // Минимум 2 резюме оставляем
		keepCount = 2
	}

	if len(summaries) <= keepCount {
		return &summary.SummaryResponse{}, nil
	}

	summariesToCompress := summaries[:len(summaries)-keepCount]

	m.logger.Info("Compressing summaries to bulk summary",
		zap.String("session_id", sessionID),
		zap.Int("total_summaries", len(summaries)),
		zap.Int("compress_count", len(summariesToCompress)),
		zap.Int("keep_count", keepCount),
	)

	// Конвертируем summaries в messages для SummaryService
	summaryMessages := make([]models.Message, len(summariesToCompress))
	for i, summary := range summariesToCompress {
		summaryMessages[i] = models.NewSummaryMessage(sessionID, summary.SummaryText, 1)
		summaryMessages[i].ID = summary.ID
		summaryMessages[i].Timestamp = summary.UpdatedAt
	}

	// Создаем bulk summary
	summaryReq := summary.SummaryRequest{
		SessionID:    sessionID,
		Messages:     summaryMessages,
		Reason:       "summary_compression",
		SummaryLevel: 2, // Bulk summary
	}

	summaryResp, err := m.summaryService.CreateSummary(ctx, summaryReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create bulk summary: %w", err)
	}

	// Создаем bulk summary message для хранения в БД
	bulkSummaryMessage := models.NewSummaryMessage(sessionID, summaryResp.BriefSummary, 2)
	bulkSummaryMessage.ID = uuid.New().String()

	if err := m.messageStore.SaveMessage(ctx, bulkSummaryMessage); err != nil {
		return nil, fmt.Errorf("failed to save bulk summary message: %w", err)
	}

	// Помечаем исходные резюме как сжатые
	summaryIDs := make([]string, len(summariesToCompress))
	for i, summary := range summariesToCompress {
		summaryIDs[i] = summary.ID
	}

	if err := m.messageStore.MarkSummariesAsCompressed(ctx, summaryIDs, summaryResp.SummaryID); err != nil {
		return nil, fmt.Errorf("failed to mark summaries as compressed: %w", err)
	}

	summaryResp.SummariesCompressed = len(summariesToCompress)
	summaryResp.Duration = time.Since(startTime)

	m.logger.Info("Summary compression completed",
		zap.String("session_id", sessionID),
		zap.Int("summaries_compressed", len(summariesToCompress)),
		zap.String("bulk_summary_id", summaryResp.SummaryID),
		zap.Duration("duration", summaryResp.Duration),
	)

	return summaryResp, nil
}

// buildLLMContext строит финальный контекст для отправки в LLM
func (m *Manager) buildLLMContext(ctx context.Context, req ContextRequest) ([]llm.Message, bool, error) {
	var contextMessages []llm.Message
	hasSummary := false

	// 1. Добавляем системный промпт если нужно
	if req.IncludeSystem && req.SystemPrompt != "" {
		contextMessages = append(contextMessages, llm.Message{
			Role:    "system",
			Content: req.SystemPrompt,
		})
	}

	// 2. Получаем bulk summaries (уровень 2) - всегда включаем все
	bulkSummaries, err := m.messageStore.GetSummariesByLevel(ctx, req.SessionID, 2)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get bulk summaries: %w", err)
	}

	for _, summary := range bulkSummaries {
		contextMessages = append(contextMessages, llm.Message{
			Role:    "assistant", // Резюме от ассистента
			Content: summary.SummaryText,
		})
		hasSummary = true
	}

	// 3. Получаем активные обычные summaries (уровень 1) - не сжатые в bulk
	activeSummaries, err := m.messageStore.GetActiveSummaries(ctx, req.SessionID, 1)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get active summaries: %w", err)
	}

	for _, summary := range activeSummaries {
		contextMessages = append(contextMessages, llm.Message{
			Role:    "assistant",
			Content: summary.SummaryText,
		})
		hasSummary = true
	}

	// 4. Получаем активные обычные сообщения - не сжатые в summaries
	activeMessages, err := m.messageStore.GetActiveMessages(ctx, req.SessionID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get active messages: %w", err)
	}

	for _, msg := range activeMessages {
		contextMessages = append(contextMessages, llm.Message{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	// 5. Обрезаем контекст до максимального размера если необходимо
	contextMessages = m.trimContext(contextMessages, req.IncludeSystem)

	m.logger.Debug("LLM context assembled",
		zap.String("session_id", req.SessionID),
		zap.Int("bulk_summaries", len(bulkSummaries)),
		zap.Int("active_summaries", len(activeSummaries)),
		zap.Int("active_messages", len(activeMessages)),
		zap.Int("total_context_messages", len(contextMessages)),
		zap.Bool("has_summary", hasSummary),
	)

	return contextMessages, hasSummary, nil
}

// trimContext обрезает контекст до максимального размера
func (m *Manager) trimContext(messages []llm.Message, preserveSystem bool) []llm.Message {
	if len(messages) <= m.config.ContextWindowSize {
		return messages
	}

	// Сохраняем системные сообщения если нужно
	var systemMessages []llm.Message
	var regularMessages []llm.Message

	for _, msg := range messages {
		if msg.Role == "system" && preserveSystem {
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

	m.logger.Debug("Context trimmed",
		zap.Int("original_size", len(messages)),
		zap.Int("trimmed_size", len(result)),
		zap.Int("system_messages", len(systemMessages)),
		zap.Int("regular_messages", len(regularMessages)),
	)

	return result
}

// GetContextInfo возвращает детальную информацию о текущем контексте
func (m *Manager) GetContextInfo(ctx context.Context, sessionID string) (*ContextInfo, error) {
	totalCount, err := m.messageStore.GetMessageCount(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message count: %w", err)
	}

	// Получаем информацию по уровням
	activeMessages, err := m.messageStore.GetActiveMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get active messages: %w", err)
	}

	activeSummaries, err := m.messageStore.GetActiveSummaries(ctx, sessionID, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to get active summaries: %w", err)
	}

	bulkSummaries, err := m.messageStore.GetSummariesByLevel(ctx, sessionID, 2)
	if err != nil {
		return nil, fmt.Errorf("failed to get bulk summaries: %w", err)
	}

	// Определяем, нужно ли сжатие
	messageRatio := float64(len(activeMessages)) / float64(m.config.ContextWindowSize)
	summaryRatio := float64(len(activeSummaries)) / float64(m.config.ContextWindowSize)

	var shouldCompress bool
	var compressionReason string
	var compressionLevel int

	if len(activeSummaries) > 0 && summaryRatio > m.config.SummaryCompressionRatio {
		shouldCompress = true
		compressionReason = "summary_compression"
		compressionLevel = 2
	} else if len(activeMessages) > 0 && messageRatio > m.config.MessageCompressionRatio {
		shouldCompress = true
		compressionReason = "message_compression"
		compressionLevel = 1
	}

	return &ContextInfo{
		SessionID:         sessionID,
		TotalMessages:     totalCount,
		ActiveMessages:    len(activeMessages),
		ActiveSummaries:   len(activeSummaries),
		BulkSummaries:     len(bulkSummaries),
		ContextWindowSize: m.config.ContextWindowSize,
		MaxBeforeCompress: m.config.MaxMessagesBeforeCompress,
		ShouldCompress:    shouldCompress,
		CompressionReason: compressionReason,
		CompressionLevel:  compressionLevel,
		MessageRatio:      messageRatio,
		SummaryRatio:      summaryRatio,
	}, nil
}

type ContextInfo struct {
	SessionID         string  `json:"session_id"`
	TotalMessages     int     `json:"total_messages"`
	ActiveMessages    int     `json:"active_messages"`
	ActiveSummaries   int     `json:"active_summaries"`
	BulkSummaries     int     `json:"bulk_summaries"`
	ContextWindowSize int     `json:"context_window_size"`
	MaxBeforeCompress int     `json:"max_before_compress"`
	ShouldCompress    bool    `json:"should_compress"`
	CompressionReason string  `json:"compression_reason,omitempty"`
	CompressionLevel  int     `json:"compression_level,omitempty"`
	MessageRatio      float64 `json:"message_ratio"`
	SummaryRatio      float64 `json:"summary_ratio"`
}

// CleanupSession очищает контекст сессии
func (m *Manager) CleanupSession(ctx context.Context, sessionID string) error {
	// Удаляем все резюме и сообщения (каскадное удаление через FK)
	if err := m.messageStore.DeleteSession(ctx, sessionID); err != nil {
		m.logger.Warn("Failed to delete session during cleanup",
			zap.String("session_id", sessionID),
			zap.Error(err),
		)
		return err
	}

	m.logger.Info("Session cleanup completed", zap.String("session_id", sessionID))
	return nil
}
