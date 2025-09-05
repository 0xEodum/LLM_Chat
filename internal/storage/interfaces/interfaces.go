package interfaces

import (
	"LLM_Chat/internal/storage/models"
	"context"
)

type MessageStore interface {
	SaveMessage(ctx context.Context, msg models.Message) error
	GetMessages(ctx context.Context, sessionID string, limit int) ([]models.Message, error)
	GetMessageCount(ctx context.Context, sessionID string) (int, error)
	DeleteSession(ctx context.Context, sessionID string) error
}

type SummaryStore interface {
	GetSummary(ctx context.Context, sessionID string) (*models.Summary, error)
	SaveSummary(ctx context.Context, summary models.Summary) error
	DeleteSummary(ctx context.Context, sessionID string) error
}

type SessionStore interface {
	CreateSession(ctx context.Context, sessionID string) error
	GetSession(ctx context.Context, sessionID string) (*models.ChatSession, error)
	UpdateSession(ctx context.Context, sessionID string) error
	DeleteSession(ctx context.Context, sessionID string) error
}
