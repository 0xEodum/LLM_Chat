package context

import (
	"context"
)

// ContextManager определяет интерфейс для управления контекстом
type ContextManager interface {
	BuildContext(ctx context.Context, req ContextRequest) (*ContextResponse, error)
	GetContextInfo(ctx context.Context, sessionID string) (*ContextInfo, error)
	CleanupSession(ctx context.Context, sessionID string) error
}

// Verify interface implementation
var _ ContextManager = (*Manager)(nil)
