package llm

import "errors"

var (
	ErrAPIKeyNotSet        = errors.New("API key is not set")
	ErrInvalidModel        = errors.New("invalid model specified")
	ErrEmptyMessages       = errors.New("messages cannot be empty")
	ErrContextCanceled     = errors.New("context was canceled")
	ErrStreamClosed        = errors.New("stream was closed")
	ErrRateLimited         = errors.New("rate limited by API")
	ErrInsufficientCredits = errors.New("insufficient credits")
)
