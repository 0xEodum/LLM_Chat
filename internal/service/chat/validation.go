package chat

import (
	"errors"
	"strings"
)

var (
	ErrEmptySessionID   = errors.New("session ID cannot be empty")
	ErrEmptyMessage     = errors.New("message cannot be empty")
	ErrMessageTooLong   = errors.New("message is too long")
	ErrInvalidSessionID = errors.New("invalid session ID format")
)

const (
	MaxMessageLength   = 10000 // Максимальная длина сообщения
	MaxSessionIDLength = 100   // Максимальная длина session ID
)

func ValidateProcessMessageRequest(req ProcessMessageRequest) error {
	if strings.TrimSpace(req.SessionID) == "" {
		return ErrEmptySessionID
	}

	if len(req.SessionID) > MaxSessionIDLength {
		return ErrInvalidSessionID
	}

	if strings.TrimSpace(req.Message) == "" {
		return ErrEmptyMessage
	}

	if len(req.Message) > MaxMessageLength {
		return ErrMessageTooLong
	}

	return nil
}
