package summary

import (
	"context"
	"fmt"
	"strings"
	"time"

	"LLM_Chat/internal/storage/interfaces"
	"LLM_Chat/internal/storage/models"
	"LLM_Chat/pkg/llm"

	"go.uber.org/zap"
)

type Service struct {
	summaryStore interfaces.SummaryStore
	shrinkClient llm.LLMClient // Отдельный клиент для сжатия
	logger       *zap.Logger
	config       Config
}

type Config struct {
	MaxMessagesBeforeSummary int // Максимум сообщений до сжатия
	ContextWindowSize        int // Размер окна контекста
	AnchorsCount             int // Количество якорей для создания
	SummaryMaxLength         int // Максимальная длина резюме
	MinMessagesForSummary    int // Минимум сообщений для создания резюме
}

func DefaultConfig() Config {
	return Config{
		MaxMessagesBeforeSummary: 50,
		ContextWindowSize:        20,
		AnchorsCount:             5,
		SummaryMaxLength:         500,
		MinMessagesForSummary:    10,
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
	SessionID string
	Messages  []models.Message
	Reason    string // Причина создания резюме
}

type SummaryResponse struct {
	SessionID    string
	Anchors      []string
	BriefSummary string
	TokensUsed   int
	Compressed   int // Количество сжатых сообщений
}

// ShouldCreateSummary определяет, нужно ли создавать резюме
func (s *Service) ShouldCreateSummary(ctx context.Context, sessionID string, messageCount int) (bool, string) {
	// Проверяем текущее резюме
	summary, err := s.summaryStore.GetSummary(ctx, sessionID)
	if err != nil {
		// Нет резюме, проверяем по общему количеству
		if messageCount >= s.config.MaxMessagesBeforeSummary {
			return true, "initial_summary"
		}
		return false, ""
	}

	// Есть резюме, проверяем новые сообщения с момента последнего обновления
	// TODO: Более точная логика на основе времени последнего обновления
	timeSinceUpdate := time.Since(summary.UpdatedAt)

	if messageCount >= s.config.MaxMessagesBeforeSummary && timeSinceUpdate > 10*time.Minute {
		return true, "update_summary"
	}

	return false, ""
}

// CreateSummary создаёт резюме и якоря для сессии
func (s *Service) CreateSummary(ctx context.Context, req SummaryRequest) (*SummaryResponse, error) {
	startTime := time.Now()

	s.logger.Info("Creating summary",
		zap.String("session_id", req.SessionID),
		zap.Int("messages_count", len(req.Messages)),
		zap.String("reason", req.Reason),
	)

	if len(req.Messages) < s.config.MinMessagesForSummary {
		return nil, fmt.Errorf("not enough messages for summary: %d < %d",
			len(req.Messages), s.config.MinMessagesForSummary)
	}

	// 1. Создаём якоря (ключевые моменты разговора)
	anchors, err := s.createAnchors(ctx, req.Messages)
	if err != nil {
		return nil, fmt.Errorf("failed to create anchors: %w", err)
	}

	// 2. Создаём краткое резюме
	briefSummary, tokensUsed, err := s.createBriefSummary(ctx, req.Messages, anchors)
	if err != nil {
		return nil, fmt.Errorf("failed to create brief summary: %w", err)
	}

	// 3. Сохраняем резюме
	summary := models.Summary{
		SessionID:    req.SessionID,
		Anchors:      anchors,
		BriefSummary: briefSummary,
		UpdatedAt:    time.Now(),
	}

	if err := s.summaryStore.SaveSummary(ctx, summary); err != nil {
		return nil, fmt.Errorf("failed to save summary: %w", err)
	}

	duration := time.Since(startTime)

	s.logger.Info("Summary created successfully",
		zap.String("session_id", req.SessionID),
		zap.Int("anchors_count", len(anchors)),
		zap.Int("summary_length", len(briefSummary)),
		zap.Int("tokens_used", tokensUsed),
		zap.Int("compressed_messages", len(req.Messages)),
		zap.Duration("duration", duration),
	)

	return &SummaryResponse{
		SessionID:    req.SessionID,
		Anchors:      anchors,
		BriefSummary: briefSummary,
		TokensUsed:   tokensUsed,
		Compressed:   len(req.Messages),
	}, nil
}

// createAnchors создаёт ключевые якоря из истории сообщений
func (s *Service) createAnchors(ctx context.Context, messages []models.Message) ([]string, error) {
	// Формируем промпт для создания якорей
	systemPrompt := `Ты эксперт по анализу диалогов. Твоя задача - выделить ключевые моменты из разговора в виде коротких якорей.

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

	systemPrompt = fmt.Sprintf(systemPrompt, s.config.AnchorsCount)

	// Формируем контент диалога
	var dialogBuilder strings.Builder
	dialogBuilder.WriteString("Диалог для анализа:\n\n")

	for _, msg := range messages {
		role := "Пользователь"
		if msg.Role == "assistant" {
			role = "Ассистент"
		}
		dialogBuilder.WriteString(fmt.Sprintf("%s: %s\n", role, msg.Content))
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

	s.logger.Debug("Created anchors",
		zap.String("anchors_raw", anchorsText),
		zap.Strings("anchors_parsed", anchors),
	)

	return anchors, nil
}

// createBriefSummary создаёт краткое резюме диалога
func (s *Service) createBriefSummary(ctx context.Context, messages []models.Message, anchors []string) (string, int, error) {
	systemPrompt := `Ты эксперт по созданию кратких резюме диалогов. Создай краткое резюме разговора.

Требования:
1. Резюме должно быть максимум %d символов
2. Используй тот же язык, что и в диалоге
3. Отражай основные темы и выводы
4. Будь конкретным и информативным
5. Используй предоставленные якоря как ориентир

Якоря для ориентира: %s

Отвечай только текстом резюме, без дополнительных комментариев.`

	anchorsStr := strings.Join(anchors, ", ")
	systemPrompt = fmt.Sprintf(systemPrompt, s.config.SummaryMaxLength, anchorsStr)

	// Формируем контент диалога (берём каждое N-ное сообщение для краткости)
	var dialogBuilder strings.Builder
	dialogBuilder.WriteString("Диалог для резюмирования:\n\n")

	step := 1
	if len(messages) > 20 {
		step = len(messages) / 20 // Берём примерно 20 сообщений
	}

	for i := 0; i < len(messages); i += step {
		msg := messages[i]
		role := "Пользователь"
		if msg.Role == "assistant" {
			role = "Ассистент"
		}
		dialogBuilder.WriteString(fmt.Sprintf("%s: %s\n", role, msg.Content))
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

	return summary, response.Usage.TotalTokens, nil
}

// GetSummary получает существующее резюме для сессии
func (s *Service) GetSummary(ctx context.Context, sessionID string) (*models.Summary, error) {
	return s.summaryStore.GetSummary(ctx, sessionID)
}

// UpdateSummary обновляет существующее резюме с новыми сообщениями
func (s *Service) UpdateSummary(ctx context.Context, sessionID string, newMessages []models.Message) (*SummaryResponse, error) {
	// Получаем существующее резюме
	existingSummary, err := s.summaryStore.GetSummary(ctx, sessionID)
	if err != nil {
		// Нет существующего резюме, создаём новое
		return s.CreateSummary(ctx, SummaryRequest{
			SessionID: sessionID,
			Messages:  newMessages,
			Reason:    "first_summary",
		})
	}

	// Логируем информацию о существующем резюме
	s.logger.Debug("Updating existing summary",
		zap.String("session_id", sessionID),
		zap.Int("existing_anchors", len(existingSummary.Anchors)),
		zap.Int("existing_summary_length", len(existingSummary.BriefSummary)),
		zap.Time("last_updated", existingSummary.UpdatedAt),
		zap.Int("new_messages", len(newMessages)),
	)

	// Создаём обновленное резюме с новыми сообщениями
	req := SummaryRequest{
		SessionID: sessionID,
		Messages:  newMessages,
		Reason:    "update_existing",
	}

	return s.CreateSummary(ctx, req)
}

// GetContextForLLM формирует контекст для отправки в основной LLM
func (s *Service) GetContextForLLM(ctx context.Context, sessionID string, recentMessages []models.Message) ([]llm.Message, error) {
	var context []llm.Message

	// 1. Получаем резюме если есть
	summary, err := s.summaryStore.GetSummary(ctx, sessionID)
	if err == nil && summary != nil {
		// Добавляем резюме как системное сообщение
		summaryText := s.formatSummaryForContext(summary)
		context = append(context, llm.Message{
			Role:    "system",
			Content: summaryText,
		})

		s.logger.Debug("Added summary to context",
			zap.String("session_id", sessionID),
			zap.Int("anchors_count", len(summary.Anchors)),
			zap.Int("summary_length", len(summary.BriefSummary)),
		)
	}

	// 2. Добавляем недавние сообщения
	recentLLMMessages := llm.ConvertToLLMMessages(recentMessages)
	context = append(context, recentLLMMessages...)

	return context, nil
}

// formatSummaryForContext форматирует резюме для использования в контексте
func (s *Service) formatSummaryForContext(summary *models.Summary) string {
	var builder strings.Builder

	builder.WriteString("Контекст предыдущего разговора:\n\n")

	if len(summary.Anchors) > 0 {
		builder.WriteString("Ключевые темы:\n")
		for _, anchor := range summary.Anchors {
			builder.WriteString(fmt.Sprintf("- %s\n", anchor))
		}
		builder.WriteString("\n")
	}

	if summary.BriefSummary != "" {
		builder.WriteString("Краткое резюме:\n")
		builder.WriteString(summary.BriefSummary)
		builder.WriteString("\n\n")
	}

	builder.WriteString("Продолжай диалог, учитывая этот контекст.")

	return builder.String()
}

// DeleteSummary удаляет резюме для сессии
func (s *Service) DeleteSummary(ctx context.Context, sessionID string) error {
	return s.summaryStore.DeleteSummary(ctx, sessionID)
}
