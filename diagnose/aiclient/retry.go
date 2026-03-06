package aiclient

import (
	"context"
	"errors"
	"math/rand"
	"time"
)

const maxBackoff = 30 * time.Second

// RetryConfig controls the retry behavior for AI API calls.
type RetryConfig struct {
	MaxRetries   int
	RetryBackoff time.Duration
}

// ChatWithRetry calls ChatClient.Chat with exponential backoff retry on transient errors.
func ChatWithRetry(ctx context.Context, client ChatClient, cfg RetryConfig, messages []Message, tools []Tool) (*ChatResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := cfg.RetryBackoff * time.Duration(1<<(attempt-1))
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			jitter := time.Duration(float64(backoff) * (0.75 + rand.Float64()*0.5))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(jitter):
			}
		}

		resp, err := client.Chat(ctx, messages, tools)
		if err == nil {
			return resp, nil
		}
		if !IsRetryable(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// IsRetryable returns true for transient HTTP errors that may succeed on retry.
// Retryable: 429 (rate limit), 500, 502, 503, 504 and context deadline without cancellation.
// Non-retryable: 400, 401, 403, 404 and explicit context cancellation.
func IsRetryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 429, 500, 502, 503, 504:
			return true
		default:
			return false
		}
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	// Network errors, timeouts etc. are generally retryable
	return true
}
