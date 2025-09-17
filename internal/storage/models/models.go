package models

import (
	"time"
)

type Message struct {
	ID          string `json:"id"`
	SessionID   string `json:"session_id"`
	Role        string `json:"role"` // user, assistant, system, tool
	Content     string `json:"content"`
	MessageType string `json:"message_type"` // regular, summary, bulk_summary

	// Compression fields
	IsCompressed bool   `json:"is_compressed"`
	SummaryID    string `json:"summary_id,omitempty"`

	// Tool call fields for MCP
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`

	Timestamp time.Time `json:"timestamp"`
	Metadata  Metadata  `json:"metadata,omitempty"`
}

type Metadata struct {
	Tokens int     `json:"tokens,omitempty"`
	Cost   float64 `json:"cost,omitempty"`
	Model  string  `json:"model,omitempty"`
}

type Summary struct {
	ID          string   `json:"id"`
	SessionID   string   `json:"session_id"`
	SummaryText string   `json:"summary_text"`
	Anchors     []string `json:"anchors"`

	// Multi-level compression: 1 = regular summary, 2 = bulk summary
	SummaryLevel int `json:"summary_level"`

	// Coverage boundaries
	CoversFromMessageID string `json:"covers_from_message_id"`
	CoversToMessageID   string `json:"covers_to_message_id"`
	MessageCount        int    `json:"message_count"`

	// Compression can also apply to summaries
	IsCompressed bool   `json:"is_compressed"`
	SummaryID    string `json:"summary_id,omitempty"` // For bulk summaries that compress this summary

	TokensUsed int       `json:"tokens_used"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type ChatSession struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
}

// Helper methods for Message
func (m *Message) IsRegular() bool {
	return m.MessageType == "regular"
}

func (m *Message) IsSummary() bool {
	return m.MessageType == "summary"
}

func (m *Message) IsBulkSummary() bool {
	return m.MessageType == "bulk_summary"
}

func (m *Message) IsToolCall() bool {
	return m.Role == "tool" && m.ToolName != ""
}

// Helper methods for Summary
func (s *Summary) IsRegularSummary() bool {
	return s.SummaryLevel == 1
}

func (s *Summary) IsBulkSummary() bool {
	return s.SummaryLevel == 2
}

// Factory functions for creating messages
func NewUserMessage(sessionID, content string) Message {
	return Message{
		SessionID:   sessionID,
		Role:        "user",
		Content:     content,
		MessageType: "regular",
		Timestamp:   time.Now(),
		Metadata:    Metadata{},
	}
}

func NewAssistantMessage(sessionID, content string) Message {
	return Message{
		SessionID:   sessionID,
		Role:        "assistant",
		Content:     content,
		MessageType: "regular",
		Timestamp:   time.Now(),
		Metadata:    Metadata{},
	}
}

func NewSummaryMessage(sessionID, content string, summaryLevel int) Message {
	messageType := "summary"
	if summaryLevel == 2 {
		messageType = "bulk_summary"
	}

	return Message{
		SessionID:   sessionID,
		Role:        "assistant", // Summaries come from assistant
		Content:     content,
		MessageType: messageType,
		Timestamp:   time.Now(),
		Metadata:    Metadata{},
	}
}

func NewToolMessage(sessionID, content, toolName, toolCallID string) Message {
	return Message{
		SessionID:   sessionID,
		Role:        "tool",
		Content:     content,
		MessageType: "regular",
		ToolName:    toolName,
		ToolCallID:  toolCallID,
		Timestamp:   time.Now(),
		Metadata:    Metadata{},
	}
}

// Factory functions for creating summaries
func NewRegularSummary(sessionID, summaryText string, anchors []string) Summary {
	return Summary{
		SessionID:    sessionID,
		SummaryText:  summaryText,
		Anchors:      anchors,
		SummaryLevel: 1,
		UpdatedAt:    time.Now(),
	}
}

func NewBulkSummary(sessionID, summaryText string, anchors []string) Summary {
	return Summary{
		SessionID:    sessionID,
		SummaryText:  summaryText,
		Anchors:      anchors,
		SummaryLevel: 2,
		UpdatedAt:    time.Now(),
	}
}
