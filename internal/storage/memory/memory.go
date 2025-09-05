package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"LLM_Chat/internal/storage/interfaces"
	"LLM_Chat/internal/storage/models"
)

type MemoryStorage struct {
	messages  map[string][]models.Message   // sessionID -> messages
	summaries map[string]models.Summary     // sessionID -> summary
	sessions  map[string]models.ChatSession // sessionID -> session
	mu        sync.RWMutex
}

func New() *MemoryStorage {
	return &MemoryStorage{
		messages:  make(map[string][]models.Message),
		summaries: make(map[string]models.Summary),
		sessions:  make(map[string]models.ChatSession),
	}
}

// MessageStore implementation
func (m *MemoryStorage) SaveMessage(ctx context.Context, msg models.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.messages[msg.SessionID] = append(m.messages[msg.SessionID], msg)

	// Update session
	if session, exists := m.sessions[msg.SessionID]; exists {
		session.UpdatedAt = time.Now()
		session.MessageCount++
		m.sessions[msg.SessionID] = session
	}

	return nil
}

func (m *MemoryStorage) GetMessages(ctx context.Context, sessionID string, limit int) ([]models.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	messages, exists := m.messages[sessionID]
	if !exists {
		return []models.Message{}, nil
	}

	// Sort by timestamp
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Timestamp.Before(messages[j].Timestamp)
	})

	// Apply limit
	if limit > 0 && len(messages) > limit {
		return messages[len(messages)-limit:], nil
	}

	return messages, nil
}

func (m *MemoryStorage) GetMessageCount(ctx context.Context, sessionID string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	messages, exists := m.messages[sessionID]
	if !exists {
		return 0, nil
	}

	return len(messages), nil
}

func (m *MemoryStorage) DeleteSession(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.messages, sessionID)
	delete(m.summaries, sessionID)
	delete(m.sessions, sessionID)

	return nil
}

// SummaryStore implementation
func (m *MemoryStorage) GetSummary(ctx context.Context, sessionID string) (*models.Summary, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	summary, exists := m.summaries[sessionID]
	if !exists {
		return nil, fmt.Errorf("summary not found for session %s", sessionID)
	}

	return &summary, nil
}

func (m *MemoryStorage) SaveSummary(ctx context.Context, summary models.Summary) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.summaries[summary.SessionID] = summary
	return nil
}

func (m *MemoryStorage) DeleteSummary(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.summaries, sessionID)
	return nil
}

// SessionStore implementation
func (m *MemoryStorage) CreateSession(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[sessionID]; exists {
		return fmt.Errorf("session %s already exists", sessionID)
	}

	m.sessions[sessionID] = models.ChatSession{
		ID:           sessionID,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		MessageCount: 0,
	}

	return nil
}

func (m *MemoryStorage) GetSession(ctx context.Context, sessionID string) (*models.ChatSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, exists := m.sessions[sessionID]
	if !exists {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	return &session, nil
}

func (m *MemoryStorage) UpdateSession(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, exists := m.sessions[sessionID]
	if !exists {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	session.UpdatedAt = time.Now()
	m.sessions[sessionID] = session

	return nil
}

// Verify interfaces implementation
var _ interfaces.MessageStore = (*MemoryStorage)(nil)
var _ interfaces.SummaryStore = (*MemoryStorage)(nil)
var _ interfaces.SessionStore = (*MemoryStorage)(nil)
