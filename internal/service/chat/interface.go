package chat

import (
	contextmgr "LLM_Chat/internal/service/context"
	"LLM_Chat/internal/storage/models"
	"context"
)

// ChatService определяет расширенный интерфейс для работы с чатами
type ChatService interface {
	ProcessMessage(ctx context.Context, req ProcessMessageRequest) (*ProcessMessageResponse, error)
	ProcessMessageStream(ctx context.Context, req ProcessMessageRequest) (<-chan StreamResponse, error)
	GetHistory(ctx context.Context, sessionID string, limit int) ([]models.Message, error)
	GetContextInfo(ctx context.Context, sessionID string) (*contextmgr.ContextInfo, error)
	DeleteSession(ctx context.Context, sessionID string) error
	TriggerCompression(ctx context.Context, sessionID string) (*CompressionResult, error)
}

// Verify interface implementation
var _ ChatService = (*Service)(nil)
