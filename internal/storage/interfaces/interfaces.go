package interfaces

import (
	"LLM_Chat/internal/storage/models"
	"context"
)

type MessageStore interface {
	// Basic message operations
	SaveMessage(ctx context.Context, msg models.Message) error
	GetMessages(ctx context.Context, sessionID string, limit int) ([]models.Message, error)
	GetMessageCount(ctx context.Context, sessionID string) (int, error)
	DeleteSession(ctx context.Context, sessionID string) error

	// UI-specific operations (returns all regular messages for display)
	GetMessagesForUI(ctx context.Context, sessionID string) ([]models.Message, error)

	// LLM-specific operations (returns uncompressed messages)
	GetActiveMessages(ctx context.Context, sessionID string) ([]models.Message, error)

	// Compression operations
	MarkMessagesAsCompressed(ctx context.Context, messageIDs []string, summaryID string) error
}

type SummaryStore interface {
	// Basic summary operations
	GetSummary(ctx context.Context, sessionID string) (*models.Summary, error)
	SaveSummary(ctx context.Context, summary models.Summary) error
	DeleteSummary(ctx context.Context, sessionID string) error

	// Multi-level summary operations
	GetSummariesByLevel(ctx context.Context, sessionID string, level int) ([]models.Summary, error)
	GetActiveSummaries(ctx context.Context, sessionID string, level int) ([]models.Summary, error)

	// Bulk summary operations (for compressing summaries themselves)
	MarkSummariesAsCompressed(ctx context.Context, summaryIDs []string, bulkSummaryID string) error
}

type SessionStore interface {
	CreateSession(ctx context.Context, sessionID string) error
	GetSession(ctx context.Context, sessionID string) (*models.ChatSession, error)
	UpdateSession(ctx context.Context, sessionID string) error
	DeleteSession(ctx context.Context, sessionID string) error
}

// ExtendedMessageStore combines all storage interfaces for convenience
type ExtendedMessageStore interface {
	MessageStore
	SummaryStore
	SessionStore
}
