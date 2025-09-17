-- Migration: 001_initial_schema.sql
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
                          summary_id UUID NULL REFERENCES summaries(id) ON DELETE SET NULL,

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
                           summary_id UUID NULL REFERENCES summaries(id) ON DELETE SET NULL,

                           tokens_used INTEGER DEFAULT 0,
                           created_at TIMESTAMP DEFAULT NOW()
);

-- Add the foreign key constraint after summaries table is created
ALTER TABLE messages ADD CONSTRAINT fk_messages_summary_id
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
COMMENT ON COLUMN summaries.covers_to_message_id IS 'Last message ID covered by this summary';