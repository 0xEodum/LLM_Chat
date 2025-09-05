package llm

import (
	"context"
	"fmt"
	"math"
	"time"

	"go.uber.org/zap"
)

type RetryConfig struct {
	MaxRetries        int
	InitialDelay      time.Duration
	MaxDelay          time.Duration
	BackoffMultiplier float64
	RetryableErrors   []error
}

func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:        3,
		InitialDelay:      1 * time.Second,
		MaxDelay:          30 * time.Second,
		BackoffMultiplier: 2.0,
		RetryableErrors: []error{
			ErrRateLimited,
			// Можно добавить другие ретраиабл ошибки
		},
	}
}

func (c *Client) ChatCompletionWithRetry(ctx context.Context, messages []Message, retryConfig RetryConfig) (*ChatResponse, error) {
	var lastErr error

	for attempt := 0; attempt <= retryConfig.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(float64(retryConfig.InitialDelay) * math.Pow(retryConfig.BackoffMultiplier, float64(attempt-1)))
			if delay > retryConfig.MaxDelay {
				delay = retryConfig.MaxDelay
			}

			c.logger.Info("Retrying LLM request",
				zap.Int("attempt", attempt),
				zap.Duration("delay", delay),
				zap.Error(lastErr),
			)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		resp, err := c.ChatCompletion(ctx, messages)
		if err == nil {
			return resp, nil
		}

		lastErr = err

		// Проверяем, стоит ли ретраить эту ошибку
		if !isRetryableError(err, retryConfig.RetryableErrors) {
			break
		}
	}

	return nil, fmt.Errorf("failed after %d attempts: %w", retryConfig.MaxRetries+1, lastErr)
}

func isRetryableError(err error, retryableErrors []error) bool {
	for _, retryableErr := range retryableErrors {
		if err == retryableErr {
			return true
		}
	}
	return false
}
