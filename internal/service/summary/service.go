package summary

import (
	"context"
	"fmt"
	"strings"
	"time"

	"LLM_Chat/internal/storage/interfaces"
	"LLM_Chat/internal/storage/models"
	"LLM_Chat/pkg/llm"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type Service struct {
	summaryStore interfaces.SummaryStore
	shrinkClient llm.LLMClient // Отдельный клиент для сжатия
	logger       *zap.Logger
	config       Config
}

type Config struct {
	MaxMessagesBeforeSummary int // Максимум сообщений до сжатия (deprecated, используется в Context Manager)
	ContextWindowSize        int // Размер окна контекста
	AnchorsCount             int // Количество якорей для создания
	SummaryMaxLength         int // Максимальная длина резюме
	MinMessagesForSummary    int // Минимум сообщений для создания резюме
}

func DefaultConfig() Config {
	return Config{
		MaxMessagesBeforeSummary: 50, // Deprecated
		ContextWindowSize:        20,
		AnchorsCount:             5,
		SummaryMaxLength:         500,
		MinMessagesForSummary:    3, // Минимум для работы с многоуровневым сжатием
	}
}

func NewService(
	summaryStore interfaces.SummaryStore,
	shrinkClient llm.LLMClient,
	config Config,
	logger *zap.Logger,
) *Service {
	return &Service{
		summaryStore: summaryStore,
		shrinkClient: shrinkClient,
		config:       config,
		logger:       logger,
	}
}

type SummaryRequest struct {
	SessionID    string
	Messages     []models.Message
	Reason       string // Причина создания резюме
	SummaryLevel int    // 1 = regular summary, 2 = bulk summary
}

type SummaryResponse struct {
	SessionID           string
	SummaryID           string // ID созданного резюме
	Anchors             []string
	BriefSummary        string
	SummaryLevel        int
	TokensUsed          int
	MessagesCompressed  int // Количество сжатых сообщений
	SummariesCompressed int // Количество сжатых резюме (для bulk summaries)
	Duration            time.Duration
}

// CreateSummary создаёт резюме указанного уровня
func (s *Service) CreateSummary(ctx context.Context, req SummaryRequest) (*SummaryResponse, error) {
	startTime := time.Now()

	s.logger.Info("Creating multi-level summary",
		zap.String("session_id", req.SessionID),
		zap.Int("messages_count", len(req.Messages)),
		zap.String("reason", req.Reason),
		zap.Int("summary_level", req.SummaryLevel),
	)

	if len(req.Messages) < s.config.MinMessagesForSummary {
		return nil, fmt.Errorf("not enough messages for summary: %d < %d",
			len(req.Messages), s.config.MinMessagesForSummary)
	}

	// Validate summary level
	if req.SummaryLevel < 1 || req.SummaryLevel > 2 {
		return nil, fmt.Errorf("invalid summary level: %d (must be 1 or 2)", req.SummaryLevel)
	}

	// 1. Создаём якоря (ключевые моменты)
	anchors, err := s.createAnchors(ctx, req.Messages, req.SummaryLevel)
	if err != nil {
		return nil, fmt.Errorf("failed to create anchors: %w", err)
	}

	// 2. Создаём краткое резюме
	briefSummary, tokensUsed, err := s.createBriefSummary(ctx, req.Messages, anchors, req.SummaryLevel)
	if err != nil {
		return nil, fmt.Errorf("failed to create brief summary: %w", err)
	}

	// 3. Определяем границы сжатия
	var coversFromID, coversToID string
	if len(req.Messages) > 0 {
		coversFromID = req.Messages[0].ID
		coversToID = req.Messages[len(req.Messages)-1].ID
	}

	// 4. Сохраняем резюме в БД
	summaryID := uuid.New().String()
	summary := models.Summary{
		ID:                  summaryID,
		SessionID:           req.SessionID,
		SummaryText:         briefSummary,
		Anchors:             anchors,
		SummaryLevel:        req.SummaryLevel,
		CoversFromMessageID: coversFromID,
		CoversToMessageID:   coversToID,
		MessageCount:        len(req.Messages),
		TokensUsed:          tokensUsed,
		UpdatedAt:           time.Now(),
	}

	if err := s.summaryStore.SaveSummary(ctx, summary); err != nil {
		return nil, fmt.Errorf("failed to save summary: %w", err)
	}

	duration := time.Since(startTime)

	s.logger.Info("Multi-level summary created successfully",
		zap.String("session_id", req.SessionID),
		zap.String("summary_id", summaryID),
		zap.Int("summary_level", req.SummaryLevel),
		zap.Int("anchors_count", len(anchors)),
		zap.Int("summary_length", len(briefSummary)),
		zap.Int("tokens_used", tokensUsed),
		zap.Int("compressed_items", len(req.Messages)),
		zap.Duration("duration", duration),
	)

	response := &SummaryResponse{
		SessionID:    req.SessionID,
		SummaryID:    summaryID,
		Anchors:      anchors,
		BriefSummary: briefSummary,
		SummaryLevel: req.SummaryLevel,
		TokensUsed:   tokensUsed,
		Duration:     duration,
	}

	// Устанавливаем соответствующие поля в зависимости от уровня
	if req.SummaryLevel == 1 {
		response.MessagesCompressed = len(req.Messages)
	} else if req.SummaryLevel == 2 {
		response.SummariesCompressed = len(req.Messages)
	}

	return response, nil
}

// createAnchors создаёт ключевые якоря из истории сообщений/резюме
func (s *Service) createAnchors(ctx context.Context, messages []models.Message, summaryLevel int) ([]string, error) {
	// Формируем промпт для создания якорей в зависимости от уровня
	var systemPrompt string
	if summaryLevel == 2 {
		systemPrompt = `Ты эксперт по анализу диалогов. Твоя задача - выделить ключевые моменты из набора резюме в виде коротких якорей.

Якорь - это краткая фраза (3-7 слов), которая отражает важную тему или группу тем из резюме.

Правила:
1. Создай ровно %d якорей
2. Каждый якорь должен быть коротким и информативным
3. Якоря должны отражать основные темы из всех резюме
4. Используй тот же язык, что и в резюме
5. Сконцентрируйся на самых важных и общих темах
6. Отвечай только списком якорей, по одному на строке, без нумерации

Пример хороших якорей для bulk summary:
- "Обсуждение технических решений"
- "Карьерное планирование"
- "Анализ проектных задач"
- "Рекомендации и советы"`
	} else {
		systemPrompt = `Ты эксперт по анализу диалогов. Твоя задача - выделить ключевые моменты из разговора в виде коротких якорей.

Якорь - это краткая фраза (3-7 слов), которая отражает важную тему или поворотный момент в разговоре.

Правила:
1. Создай ровно %d якорей
2. Каждый якорь должен быть коротким и информативным
3. Якоря должны отражать основные темы и важные моменты
4. Используй тот же язык, что и в диалоге
5. Отвечай только списком якорей, по одному на строке, без нумерации

Пример хороших якорей:
- "Обсуждение карьерных планов"
- "Проблемы с проектом"
- "Рекомендации по книгам"
- "Планы на выходные"`
	}

	systemPrompt = fmt.Sprintf(systemPrompt, s.config.AnchorsCount)

	// Формируем контент в зависимости от уровня
	var dialogBuilder strings.Builder
	if summaryLevel == 2 {
		dialogBuilder.WriteString("Резюме для анализа:\n\n")
		for i, msg := range messages {
			dialogBuilder.WriteString(fmt.Sprintf("Резюме %d: %s\n\n", i+1, msg.Content))
		}
	} else {
		dialogBuilder.WriteString("Диалог для анализа:\n\n")
		for _, msg := range messages {
			role := s.getRoleDisplayName(msg.Role)
			dialogBuilder.WriteString(fmt.Sprintf("%s: %s\n", role, msg.Content))
		}
	}

	llmMessages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: dialogBuilder.String()},
	}

	response, err := s.shrinkClient.ChatCompletion(ctx, llmMessages)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}

	if len(response.Choices) == 0 {
		return nil, fmt.Errorf("no response from LLM")
	}

	// Парсим якоря из ответа
	anchorsText := strings.TrimSpace(response.Choices[0].Message.Content)
	anchorLines := strings.Split(anchorsText, "\n")

	var anchors []string
	for _, line := range anchorLines {
		anchor := strings.TrimSpace(line)
		anchor = strings.TrimPrefix(anchor, "-")
		anchor = strings.TrimPrefix(anchor, "•")
		anchor = strings.TrimSpace(anchor)

		if anchor != "" && len(anchor) > 3 {
			anchors = append(anchors, anchor)
		}
	}

	// Ограничиваем количество якорей
	if len(anchors) > s.config.AnchorsCount {
		anchors = anchors[:s.config.AnchorsCount]
	}

	s.logger.Debug("Created anchors for multi-level summary",
		zap.Int("summary_level", summaryLevel),
		zap.String("anchors_raw", anchorsText),
		zap.Strings("anchors_parsed", anchors),
	)

	return anchors, nil
}

// createBriefSummary создаёт краткое резюме в зависимости от уровня
func (s *Service) createBriefSummary(ctx context.Context, messages []models.Message, anchors []string, summaryLevel int) (string, int, error) {
	var systemPrompt string
	if summaryLevel == 2 {
		systemPrompt = `Ты эксперт по созданию кратких резюме. Создай краткое резюме из набора резюме диалогов.

Требования:
1. Резюме должно быть максимум %d символов
2. Используй тот же язык, что и в исходных резюме
3. Отражай основные темы и выводы из всех резюме
4. Будь конкретным и информативным
5. Создай обобщенное резюме, которое покрывает все важные аспекты
6. Используй предоставленные якоря как ориентир

Якоря для ориентира: %s

Отвечай только текстом резюме, без дополнительных комментариев.`
	} else {
		systemPrompt = `Ты эксперт по созданию кратких резюме диалогов. Создай краткое резюме разговора.

Требования:
1. Резюме должно быть максимум %d символов
2. Используй тот же язык, что и в диалоге
3. Отражай основные темы и выводы
4. Будь конкретным и информативным
5. Включи важные детали и решения
6. Используй предоставленные якоря как ориентир

Якоря для ориентира: %s

Отвечай только текстом резюме, без дополнительных комментариев.`
	}

	anchorsStr := strings.Join(anchors, ", ")
	systemPrompt = fmt.Sprintf(systemPrompt, s.config.SummaryMaxLength, anchorsStr)

	// Формируем контент для резюмирования
	var dialogBuilder strings.Builder
	if summaryLevel == 2 {
		dialogBuilder.WriteString("Резюме для объединения:\n\n")
		for i, msg := range messages {
			dialogBuilder.WriteString(fmt.Sprintf("Резюме %d:\n%s\n\n", i+1, msg.Content))
		}
	} else {
		dialogBuilder.WriteString("Диалог для резюмирования:\n\n")

		// Для первого уровня можем пропускать сообщения если их слишком много
		step := 1
		if len(messages) > 20 {
			step = len(messages) / 20 // Берём примерно 20 сообщений
		}

		for i := 0; i < len(messages); i += step {
			msg := messages[i]
			role := s.getRoleDisplayName(msg.Role)
			dialogBuilder.WriteString(fmt.Sprintf("%s: %s\n", role, msg.Content))
		}
	}

	llmMessages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: dialogBuilder.String()},
	}

	response, err := s.shrinkClient.ChatCompletion(ctx, llmMessages)
	if err != nil {
		return "", 0, fmt.Errorf("LLM request failed: %w", err)
	}

	if len(response.Choices) == 0 {
		return "", 0, fmt.Errorf("no response from LLM")
	}

	summary := strings.TrimSpace(response.Choices[0].Message.Content)

	// Ограничиваем длину резюме
	if len(summary) > s.config.SummaryMaxLength {
		summary = summary[:s.config.SummaryMaxLength-3] + "..."
	}

	s.logger.Debug("Created brief summary",
		zap.Int("summary_level", summaryLevel),
		zap.Int("summary_length", len(summary)),
		zap.Int("tokens_used", response.Usage.TotalTokens),
	)

	return summary, response.Usage.TotalTokens, nil
}

// getRoleDisplayName возвращает отображаемое имя роли
func (s *Service) getRoleDisplayName(role string) string {
	switch role {
	case "user":
		return "Пользователь"
	case "assistant":
		return "Ассистент"
	case "tool":
		return "Инструмент"
	case "system":
		return "Система"
	default:
		return "Участник"
	}
}

// ShouldCreateSummary определяет, нужно ли создавать резюме (deprecated, используется Context Manager)
func (s *Service) ShouldCreateSummary(ctx context.Context, sessionID string, messageCount int) (bool, string) {
	s.logger.Warn("ShouldCreateSummary is deprecated, use Context Manager instead",
		zap.String("session_id", sessionID),
		zap.Int("message_count", messageCount),
	)

	// Простая логика для обратной совместимости
	if messageCount >= s.config.MaxMessagesBeforeSummary {
		return true, "message_count_threshold"
	}
	return false, ""
}

// GetSummary получает существующее резюме для сессии
func (s *Service) GetSummary(ctx context.Context, sessionID string) (*models.Summary, error) {
	return s.summaryStore.GetSummary(ctx, sessionID)
}

// UpdateSummary обновляет существующее резюме с новыми сообщениями (deprecated)
func (s *Service) UpdateSummary(ctx context.Context, sessionID string, newMessages []models.Message) (*SummaryResponse, error) {
	s.logger.Warn("UpdateSummary is deprecated, use CreateSummary with Context Manager instead",
		zap.String("session_id", sessionID),
		zap.Int("new_messages", len(newMessages)),
	)

	// Создаём новое резюме для обратной совместимости
	return s.CreateSummary(ctx, SummaryRequest{
		SessionID:    sessionID,
		Messages:     newMessages,
		Reason:       "update_deprecated",
		SummaryLevel: 1,
	})
}

// GetContextForLLM формирует контекст для отправки в основной LLM (deprecated)
func (s *Service) GetContextForLLM(ctx context.Context, sessionID string, recentMessages []models.Message) ([]llm.Message, error) {
	s.logger.Warn("GetContextForLLM is deprecated, use Context Manager instead",
		zap.String("session_id", sessionID),
		zap.Int("recent_messages", len(recentMessages)),
	)

	// Простая логика для обратной совместимости
	var context []llm.Message

	// Пробуем получить резюме
	summary, err := s.summaryStore.GetSummary(ctx, sessionID)
	if err == nil && summary != nil {
		summaryText := s.formatSummaryForContext(summary)
		context = append(context, llm.Message{
			Role:    "system",
			Content: summaryText,
		})
	}

	// Добавляем недавние сообщения
	recentLLMMessages := llm.ConvertToLLMMessages(recentMessages)
	context = append(context, recentLLMMessages...)

	return context, nil
}

// formatSummaryForContext форматирует резюме для использования в контексте
func (s *Service) formatSummaryForContext(summary *models.Summary) string {
	var builder strings.Builder

	levelName := "резюме"
	if summary.SummaryLevel == 2 {
		levelName = "обобщенное резюме"
	}

	builder.WriteString(fmt.Sprintf("Контекст предыдущего разговора (%s):\n\n", levelName))

	if len(summary.Anchors) > 0 {
		builder.WriteString("Ключевые темы:\n")
		for _, anchor := range summary.Anchors {
			builder.WriteString(fmt.Sprintf("- %s\n", anchor))
		}
		builder.WriteString("\n")
	}

	if summary.SummaryText != "" {
		if summary.SummaryLevel == 2 {
			builder.WriteString("Краткое обобщение диалогов:\n")
		} else {
			builder.WriteString("Краткое резюме:\n")
		}
		builder.WriteString(summary.SummaryText)
		builder.WriteString("\n\n")
	}

	builder.WriteString("Продолжай диалог, учитывая этот контекст.")

	return builder.String()
}

// DeleteSummary удаляет резюме для сессии
func (s *Service) DeleteSummary(ctx context.Context, sessionID string) error {
	return s.summaryStore.DeleteSummary(ctx, sessionID)
}
