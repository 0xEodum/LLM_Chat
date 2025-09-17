package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

type Migration struct {
	Version int
	Name    string
	SQL     string
}

type Migrator struct {
	db     *sql.DB
	logger *zap.Logger
}

func NewMigrator(db *sql.DB, logger *zap.Logger) *Migrator {
	return &Migrator{
		db:     db,
		logger: logger.With(zap.String("component", "migrator")),
	}
}

// RunMigrationsFromFS runs migrations from embedded filesystem
func (m *Migrator) RunMigrationsFromFS(ctx context.Context, migrationFS fs.FS, migrationDir string) error {
	migrations, err := m.loadMigrationsFromFS(migrationFS, migrationDir)
	if err != nil {
		return fmt.Errorf("failed to load migrations: %w", err)
	}

	return m.runMigrations(ctx, migrations)
}

// RunMigrationsFromStrings runs migrations from string slice (for testing/embedding)
func (m *Migrator) RunMigrationsFromStrings(ctx context.Context, migrationSQL []string) error {
	migrations := make([]Migration, len(migrationSQL))
	for i, sql := range migrationSQL {
		migrations[i] = Migration{
			Version: i + 1,
			Name:    fmt.Sprintf("migration_%03d", i+1),
			SQL:     sql,
		}
	}

	return m.runMigrations(ctx, migrations)
}

func (m *Migrator) runMigrations(ctx context.Context, migrations []Migration) error {
	// Ensure migrations table exists
	if err := m.ensureMigrationTable(ctx); err != nil {
		return fmt.Errorf("failed to create migration table: %w", err)
	}

	// Get applied migrations
	applied, err := m.getAppliedMigrations(ctx)
	if err != nil {
		return fmt.Errorf("failed to get applied migrations: %w", err)
	}

	// Sort migrations by version
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	// Run pending migrations
	for _, migration := range migrations {
		if applied[migration.Version] {
			m.logger.Debug("Migration already applied",
				zap.Int("version", migration.Version),
				zap.String("name", migration.Name))
			continue
		}

		m.logger.Info("Running migration",
			zap.Int("version", migration.Version),
			zap.String("name", migration.Name))

		if err := m.runMigration(ctx, migration); err != nil {
			return fmt.Errorf("failed to run migration %d (%s): %w",
				migration.Version, migration.Name, err)
		}

		m.logger.Info("Migration completed successfully",
			zap.Int("version", migration.Version),
			zap.String("name", migration.Name))
	}

	return nil
}

func (m *Migrator) loadMigrationsFromFS(migrationFS fs.FS, migrationDir string) ([]Migration, error) {
	var migrations []Migration

	err := fs.WalkDir(migrationFS, migrationDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() || !strings.HasSuffix(path, ".sql") {
			return nil
		}

		// Parse version from filename (e.g., "001_initial_schema.sql" -> 1)
		filename := d.Name()
		parts := strings.SplitN(filename, "_", 2)
		if len(parts) < 2 {
			return fmt.Errorf("invalid migration filename format: %s (expected: NNN_name.sql)", filename)
		}

		version, err := strconv.Atoi(parts[0])
		if err != nil {
			return fmt.Errorf("invalid version in filename %s: %w", filename, err)
		}

		// Read migration SQL
		sqlBytes, err := fs.ReadFile(migrationFS, path)
		if err != nil {
			return fmt.Errorf("failed to read migration file %s: %w", path, err)
		}

		migration := Migration{
			Version: version,
			Name:    strings.TrimSuffix(filename, ".sql"),
			SQL:     string(sqlBytes),
		}

		migrations = append(migrations, migration)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return migrations, nil
}

func (m *Migrator) ensureMigrationTable(ctx context.Context) error {
	query := `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			applied_at TIMESTAMP DEFAULT NOW()
		)`

	_, err := m.db.ExecContext(ctx, query)
	return err
}

func (m *Migrator) getAppliedMigrations(ctx context.Context) (map[int]bool, error) {
	query := `SELECT version FROM schema_migrations`

	rows, err := m.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}

	return applied, rows.Err()
}

func (m *Migrator) runMigration(ctx context.Context, migration Migration) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Execute migration SQL
	if _, err := tx.ExecContext(ctx, migration.SQL); err != nil {
		return fmt.Errorf("failed to execute migration SQL: %w", err)
	}

	// Record migration as applied
	query := `INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3)`
	if _, err := tx.ExecContext(ctx, query, migration.Version, migration.Name, time.Now()); err != nil {
		return fmt.Errorf("failed to record migration: %w", err)
	}

	return tx.Commit()
}

// GetCurrentVersion returns the highest applied migration version
func (m *Migrator) GetCurrentVersion(ctx context.Context) (int, error) {
	// Ensure migration table exists
	if err := m.ensureMigrationTable(ctx); err != nil {
		return 0, err
	}

	query := `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`

	var version int
	err := m.db.QueryRowContext(ctx, query).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("failed to get current version: %w", err)
	}

	return version, nil
}

// ListAppliedMigrations returns a list of all applied migrations
func (m *Migrator) ListAppliedMigrations(ctx context.Context) ([]Migration, error) {
	query := `SELECT version, name FROM schema_migrations ORDER BY version`

	rows, err := m.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var migrations []Migration
	for rows.Next() {
		var migration Migration
		if err := rows.Scan(&migration.Version, &migration.Name); err != nil {
			return nil, err
		}
		migrations = append(migrations, migration)
	}

	return migrations, rows.Err()
}

// Embedded migrations for easy deployment
var EmbeddedMigrations = []string{
	// Migration 001: Initial schema
	`-- Migration: 001_initial_schema.sql
-- Create initial database schema for chat system with multi-level compression

-- Enable UUID extension
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Chat sessions table
CREATE TABLE chat_sessions (
    id VARCHAR(100) PRIMARY KEY,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW(),
    message_count INTEGER DEFAULT 0
);

-- Messages table with compression support
CREATE TABLE messages (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    session_id VARCHAR(100) NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    role VARCHAR(20) NOT NULL CHECK (role IN ('user', 'assistant', 'system', 'tool')),
    content TEXT NOT NULL,
    message_type VARCHAR(20) DEFAULT 'regular' CHECK (message_type IN ('regular', 'summary', 'bulk_summary')),
    
    -- Compression fields
    is_compressed BOOLEAN DEFAULT FALSE,
    summary_id UUID NULL,
    
    -- Tool call fields for MCP
    tool_name VARCHAR(100) NULL,
    tool_call_id VARCHAR(100) NULL,
    
    created_at TIMESTAMP DEFAULT NOW(),
    metadata JSONB DEFAULT '{}'
);

-- Summaries table with multi-level support
CREATE TABLE summaries (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    session_id VARCHAR(100) NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    summary_text TEXT NOT NULL,
    anchors JSONB DEFAULT '[]',
    
    -- Multi-level compression: 1 = regular summary, 2 = bulk summary
    summary_level INTEGER DEFAULT 1 CHECK (summary_level IN (1, 2)),
    
    -- Coverage boundaries
    covers_from_message_id UUID NOT NULL,
    covers_to_message_id UUID NOT NULL,
    message_count INTEGER DEFAULT 0,
    
    -- Compression can also apply to summaries
    is_compressed BOOLEAN DEFAULT FALSE,
    summary_id UUID NULL,
    
    tokens_used INTEGER DEFAULT 0,
    created_at TIMESTAMP DEFAULT NOW()
);

-- Add foreign key constraints
ALTER TABLE messages ADD CONSTRAINT fk_messages_summary_id 
    FOREIGN KEY (summary_id) REFERENCES summaries(id) ON DELETE SET NULL;
    
ALTER TABLE summaries ADD CONSTRAINT fk_summaries_summary_id 
    FOREIGN KEY (summary_id) REFERENCES summaries(id) ON DELETE SET NULL;

-- Indexes for performance
CREATE INDEX idx_messages_session_id ON messages(session_id);
CREATE INDEX idx_messages_session_created ON messages(session_id, created_at);
CREATE INDEX idx_messages_compressed ON messages(session_id, is_compressed);
CREATE INDEX idx_messages_type ON messages(session_id, message_type);

CREATE INDEX idx_summaries_session_id ON summaries(session_id);
CREATE INDEX idx_summaries_level ON summaries(session_id, summary_level);
CREATE INDEX idx_summaries_compressed ON summaries(session_id, is_compressed);
CREATE INDEX idx_summaries_created ON summaries(session_id, created_at);

CREATE INDEX idx_chat_sessions_updated ON chat_sessions(updated_at);

-- Function to update session updated_at and message_count
CREATE OR REPLACE FUNCTION update_session_stats()
RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        UPDATE chat_sessions 
        SET 
            updated_at = NOW(),
            message_count = (
                SELECT COUNT(*) 
                FROM messages 
                WHERE session_id = NEW.session_id AND message_type = 'regular'
            )
        WHERE id = NEW.session_id;
        RETURN NEW;
    ELSIF TG_OP = 'DELETE' THEN
        UPDATE chat_sessions 
        SET 
            updated_at = NOW(),
            message_count = (
                SELECT COUNT(*) 
                FROM messages 
                WHERE session_id = OLD.session_id AND message_type = 'regular'
            )
        WHERE id = OLD.session_id;
        RETURN OLD;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

-- Triggers to automatically update session stats
CREATE TRIGGER trigger_update_session_on_message_insert
    AFTER INSERT ON messages
    FOR EACH ROW
    EXECUTE FUNCTION update_session_stats();

CREATE TRIGGER trigger_update_session_on_message_delete
    AFTER DELETE ON messages
    FOR EACH ROW
    EXECUTE FUNCTION update_session_stats();

-- Comments for documentation
COMMENT ON TABLE messages IS 'Chat messages with multi-level compression support';
COMMENT ON COLUMN messages.message_type IS 'Type: regular (normal messages), summary (level 1), bulk_summary (level 2)';
COMMENT ON COLUMN messages.is_compressed IS 'True if this message is covered by a summary';
COMMENT ON COLUMN messages.summary_id IS 'Reference to the summary that covers this message';

COMMENT ON TABLE summaries IS 'Multi-level summaries: level 1 (regular) and level 2 (bulk)';
COMMENT ON COLUMN summaries.summary_level IS '1 = regular summary, 2 = bulk summary of summaries';
COMMENT ON COLUMN summaries.covers_from_message_id IS 'First message ID covered by this summary';
COMMENT ON COLUMN summaries.covers_to_message_id IS 'Last message ID covered by this summary';`,
}
