package summary

import (
	"LLM_Chat/internal/storage/models"
	"LLM_Chat/pkg/llm"
	"context"
)

// SummaryService определяет интерфейс для работы с резюме
type SummaryService interface {
	ShouldCreateSummary(ctx context.Context, sessionID string, messageCount int) (bool, string)
	CreateSummary(ctx context.Context, req SummaryRequest) (*SummaryResponse, error)
	UpdateSummary(ctx context.Context, sessionID string, newMessages []models.Message) (*SummaryResponse, error)
	GetSummary(ctx context.Context, sessionID string) (*models.Summary, error)
	GetContextForLLM(ctx context.Context, sessionID string, recentMessages []models.Message) ([]llm.Message, error)
	DeleteSummary(ctx context.Context, sessionID string) error
}

// Verify interface implementation
var _ SummaryService = (*Service)(nil)
