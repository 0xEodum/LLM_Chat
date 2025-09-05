package models

import (
	"time"
)

type Message struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"` // user, assistant, system
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	Metadata  Metadata  `json:"metadata,omitempty"`
}

type Metadata struct {
	Tokens int     `json:"tokens,omitempty"`
	Cost   float64 `json:"cost,omitempty"`
	Model  string  `json:"model,omitempty"`
}

type Summary struct {
	SessionID    string    `json:"session_id"`
	Anchors      []string  `json:"anchors"`
	BriefSummary string    `json:"brief_summary"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type ChatSession struct {
	ID           string    `json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
}
