package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"LLM_Chat/internal/storage/interfaces"
	"LLM_Chat/internal/storage/models"

	"github.com/lib/pq"
	_ "github.com/lib/pq"
	"go.uber.org/zap"
)

type PostgresStorage struct {
	db     *sql.DB
	logger *zap.Logger
}

func New(databaseURL string, logger *zap.Logger) (*PostgresStorage, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Configure connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	return &PostgresStorage{
		db:     db,
		logger: logger.With(zap.String("component", "postgres_storage")),
	}, nil
}

func (s *PostgresStorage) Close() error {
	return s.db.Close()
}

// GetDB returns the underlying database connection (for migrations)
func (s *PostgresStorage) GetDB() *sql.DB {
	return s.db
}

// MessageStore implementation
func (s *PostgresStorage) SaveMessage(ctx context.Context, msg models.Message) error {
	query := `
		INSERT INTO messages (id, session_id, role, content, message_type, is_compressed, 
		                     summary_id, tool_name, tool_call_id, created_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	metadataJSON, err := json.Marshal(msg.Metadata)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	var summaryID *string
	if msg.SummaryID != "" {
		summaryID = &msg.SummaryID
	}

	var toolName, toolCallID *string
	if msg.ToolName != "" {
		toolName = &msg.ToolName
	}
	if msg.ToolCallID != "" {
		toolCallID = &msg.ToolCallID
	}

	_, err = s.db.ExecContext(ctx, query,
		msg.ID, msg.SessionID, msg.Role, msg.Content, msg.MessageType,
		msg.IsCompressed, summaryID, toolName, toolCallID, msg.Timestamp, metadataJSON)

	if err != nil {
		return fmt.Errorf("failed to save message: %w", err)
	}

	s.logger.Debug("Message saved",
		zap.String("message_id", msg.ID),
		zap.String("session_id", msg.SessionID),
		zap.String("message_type", msg.MessageType))

	return nil
}

func (s *PostgresStorage) GetMessages(ctx context.Context, sessionID string, limit int) ([]models.Message, error) {
	query := `
		SELECT id, session_id, role, content, message_type, is_compressed, 
		       summary_id, tool_name, tool_call_id, created_at, metadata
		FROM messages 
		WHERE session_id = $1 
		ORDER BY created_at ASC
		LIMIT $2`

	rows, err := s.db.QueryContext(ctx, query, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	return s.scanMessages(rows)
}

func (s *PostgresStorage) GetMessagesForUI(ctx context.Context, sessionID string) ([]models.Message, error) {
	query := `
		SELECT id, session_id, role, content, message_type, is_compressed, 
		       summary_id, tool_name, tool_call_id, created_at, metadata
		FROM messages 
		WHERE session_id = $1 AND message_type = 'regular'
		ORDER BY created_at ASC`

	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages for UI: %w", err)
	}
	defer rows.Close()

	return s.scanMessages(rows)
}

func (s *PostgresStorage) GetActiveMessages(ctx context.Context, sessionID string) ([]models.Message, error) {
	query := `
		SELECT id, session_id, role, content, message_type, is_compressed, 
		       summary_id, tool_name, tool_call_id, created_at, metadata
		FROM messages 
		WHERE session_id = $1 AND message_type = 'regular' AND is_compressed = false
		ORDER BY created_at ASC`

	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to query active messages: %w", err)
	}
	defer rows.Close()

	return s.scanMessages(rows)
}

func (s *PostgresStorage) GetMessageCount(ctx context.Context, sessionID string) (int, error) {
	query := `SELECT COUNT(*) FROM messages WHERE session_id = $1 AND message_type = 'regular'`

	var count int
	err := s.db.QueryRowContext(ctx, query, sessionID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count messages: %w", err)
	}

	return count, nil
}

func (s *PostgresStorage) DeleteSession(ctx context.Context, sessionID string) error {
	// Delete session (cascade will handle messages and summaries)
	_, err := s.db.ExecContext(ctx, "DELETE FROM chat_sessions WHERE id = $1", sessionID)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

	s.logger.Info("Session deleted", zap.String("session_id", sessionID))
	return nil
}

func (s *PostgresStorage) MarkMessagesAsCompressed(ctx context.Context, messageIDs []string, summaryID string) error {
	if len(messageIDs) == 0 {
		return nil
	}

	query := `UPDATE messages SET is_compressed = true, summary_id = $1 WHERE id = ANY($2)`

	_, err := s.db.ExecContext(ctx, query, summaryID, pq.Array(messageIDs))
	if err != nil {
		return fmt.Errorf("failed to mark messages as compressed: %w", err)
	}

	s.logger.Debug("Messages marked as compressed",
		zap.String("summary_id", summaryID),
		zap.Int("message_count", len(messageIDs)))

	return nil
}

// SummaryStore implementation
func (s *PostgresStorage) GetSummary(ctx context.Context, sessionID string) (*models.Summary, error) {
	query := `
		SELECT id, session_id, summary_text, anchors, summary_level, 
		       covers_from_message_id, covers_to_message_id, message_count,
		       is_compressed, summary_id, tokens_used, created_at
		FROM summaries 
		WHERE session_id = $1 
		ORDER BY created_at DESC 
		LIMIT 1`

	row := s.db.QueryRowContext(ctx, query, sessionID)
	return s.scanSummary(row)
}

func (s *PostgresStorage) GetSummariesByLevel(ctx context.Context, sessionID string, level int) ([]models.Summary, error) {
	query := `
		SELECT id, session_id, summary_text, anchors, summary_level, 
		       covers_from_message_id, covers_to_message_id, message_count,
		       is_compressed, summary_id, tokens_used, created_at
		FROM summaries 
		WHERE session_id = $1 AND summary_level = $2 AND is_compressed = false
		ORDER BY created_at ASC`

	rows, err := s.db.QueryContext(ctx, query, sessionID, level)
	if err != nil {
		return nil, fmt.Errorf("failed to query summaries by level: %w", err)
	}
	defer rows.Close()

	return s.scanSummaries(rows)
}

func (s *PostgresStorage) GetActiveSummaries(ctx context.Context, sessionID string, level int) ([]models.Summary, error) {
	query := `
		SELECT id, session_id, summary_text, anchors, summary_level, 
		       covers_from_message_id, covers_to_message_id, message_count,
		       is_compressed, summary_id, tokens_used, created_at
		FROM summaries 
		WHERE session_id = $1 AND summary_level = $2 AND is_compressed = false
		ORDER BY created_at ASC`

	rows, err := s.db.QueryContext(ctx, query, sessionID, level)
	if err != nil {
		return nil, fmt.Errorf("failed to query active summaries: %w", err)
	}
	defer rows.Close()

	return s.scanSummaries(rows)
}

func (s *PostgresStorage) SaveSummary(ctx context.Context, summary models.Summary) error {
	query := `
		INSERT INTO summaries (id, session_id, summary_text, anchors, summary_level,
		                      covers_from_message_id, covers_to_message_id, message_count,
		                      is_compressed, summary_id, tokens_used, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`

	anchorsJSON, err := json.Marshal(summary.Anchors)
	if err != nil {
		return fmt.Errorf("failed to marshal anchors: %w", err)
	}

	var summaryID *string
	if summary.SummaryID != "" {
		summaryID = &summary.SummaryID
	}

	_, err = s.db.ExecContext(ctx, query,
		summary.ID, summary.SessionID, summary.SummaryText, anchorsJSON, summary.SummaryLevel,
		summary.CoversFromMessageID, summary.CoversToMessageID, summary.MessageCount,
		summary.IsCompressed, summaryID, summary.TokensUsed, summary.UpdatedAt)

	if err != nil {
		return fmt.Errorf("failed to save summary: %w", err)
	}

	s.logger.Debug("Summary saved",
		zap.String("summary_id", summary.ID),
		zap.String("session_id", summary.SessionID),
		zap.Int("summary_level", summary.SummaryLevel))

	return nil
}

func (s *PostgresStorage) DeleteSummary(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM summaries WHERE session_id = $1", sessionID)
	if err != nil {
		return fmt.Errorf("failed to delete summaries: %w", err)
	}

	return nil
}

func (s *PostgresStorage) MarkSummariesAsCompressed(ctx context.Context, summaryIDs []string, bulkSummaryID string) error {
	if len(summaryIDs) == 0 {
		return nil
	}

	query := `UPDATE summaries SET is_compressed = true, summary_id = $1 WHERE id = ANY($2)`

	_, err := s.db.ExecContext(ctx, query, bulkSummaryID, pq.Array(summaryIDs))
	if err != nil {
		return fmt.Errorf("failed to mark summaries as compressed: %w", err)
	}

	s.logger.Debug("Summaries marked as compressed",
		zap.String("bulk_summary_id", bulkSummaryID),
		zap.Int("summary_count", len(summaryIDs)))

	return nil
}

// SessionStore implementation
func (s *PostgresStorage) CreateSession(ctx context.Context, sessionID string) error {
	query := `INSERT INTO chat_sessions (id, created_at, updated_at, message_count) VALUES ($1, NOW(), NOW(), 0)`

	_, err := s.db.ExecContext(ctx, query, sessionID)
	if err != nil {
		// Check if session already exists
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" { // unique violation
			return nil // Session already exists, which is fine
		}
		return fmt.Errorf("failed to create session: %w", err)
	}

	s.logger.Debug("Session created", zap.String("session_id", sessionID))
	return nil
}

func (s *PostgresStorage) GetSession(ctx context.Context, sessionID string) (*models.ChatSession, error) {
	query := `SELECT id, created_at, updated_at, message_count FROM chat_sessions WHERE id = $1`

	var session models.ChatSession
	err := s.db.QueryRowContext(ctx, query, sessionID).Scan(
		&session.ID, &session.CreatedAt, &session.UpdatedAt, &session.MessageCount)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	return &session, nil
}

func (s *PostgresStorage) UpdateSession(ctx context.Context, sessionID string) error {
	query := `UPDATE chat_sessions SET updated_at = NOW() WHERE id = $1`

	result, err := s.db.ExecContext(ctx, query, sessionID)
	if err != nil {
		return fmt.Errorf("failed to update session: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	return nil
}

// Helper methods for scanning
func (s *PostgresStorage) scanMessages(rows *sql.Rows) ([]models.Message, error) {
	var messages []models.Message

	for rows.Next() {
		var msg models.Message
		var summaryID, toolName, toolCallID sql.NullString
		var metadataJSON []byte

		err := rows.Scan(
			&msg.ID, &msg.SessionID, &msg.Role, &msg.Content, &msg.MessageType,
			&msg.IsCompressed, &summaryID, &toolName, &toolCallID,
			&msg.Timestamp, &metadataJSON)

		if err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}

		// Handle nullable fields
		if summaryID.Valid {
			msg.SummaryID = summaryID.String
		}
		if toolName.Valid {
			msg.ToolName = toolName.String
		}
		if toolCallID.Valid {
			msg.ToolCallID = toolCallID.String
		}

		// Unmarshal metadata
		if err := json.Unmarshal(metadataJSON, &msg.Metadata); err != nil {
			s.logger.Warn("Failed to unmarshal message metadata", zap.Error(err))
			msg.Metadata = models.Metadata{}
		}

		messages = append(messages, msg)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}

	return messages, nil
}

func (s *PostgresStorage) scanSummary(row *sql.Row) (*models.Summary, error) {
	var summary models.Summary
	var summaryID sql.NullString
	var anchorsJSON []byte

	err := row.Scan(
		&summary.ID, &summary.SessionID, &summary.SummaryText, &anchorsJSON,
		&summary.SummaryLevel, &summary.CoversFromMessageID, &summary.CoversToMessageID,
		&summary.MessageCount, &summary.IsCompressed, &summaryID,
		&summary.TokensUsed, &summary.UpdatedAt)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("summary not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan summary: %w", err)
	}

	// Handle nullable fields
	if summaryID.Valid {
		summary.SummaryID = summaryID.String
	}

	// Unmarshal anchors
	if err := json.Unmarshal(anchorsJSON, &summary.Anchors); err != nil {
		s.logger.Warn("Failed to unmarshal anchors", zap.Error(err))
		summary.Anchors = []string{}
	}

	return &summary, nil
}

func (s *PostgresStorage) scanSummaries(rows *sql.Rows) ([]models.Summary, error) {
	var summaries []models.Summary

	for rows.Next() {
		var summary models.Summary
		var summaryID sql.NullString
		var anchorsJSON []byte

		err := rows.Scan(
			&summary.ID, &summary.SessionID, &summary.SummaryText, &anchorsJSON,
			&summary.SummaryLevel, &summary.CoversFromMessageID, &summary.CoversToMessageID,
			&summary.MessageCount, &summary.IsCompressed, &summaryID,
			&summary.TokensUsed, &summary.UpdatedAt)

		if err != nil {
			return nil, fmt.Errorf("failed to scan summary: %w", err)
		}

		// Handle nullable fields
		if summaryID.Valid {
			summary.SummaryID = summaryID.String
		}

		// Unmarshal anchors
		if err := json.Unmarshal(anchorsJSON, &summary.Anchors); err != nil {
			s.logger.Warn("Failed to unmarshal anchors", zap.Error(err))
			summary.Anchors = []string{}
		}

		summaries = append(summaries, summary)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}

	return summaries, nil
}

// Verify interfaces implementation
var _ interfaces.MessageStore = (*PostgresStorage)(nil)
var _ interfaces.SummaryStore = (*PostgresStorage)(nil)
var _ interfaces.SessionStore = (*PostgresStorage)(nil)
