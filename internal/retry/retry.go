// Package retry provides exponential backoff retry logic for Prizm.
//
// This is a shared package used by both the stage pipeline and provider chain.
// It defines what errors are retryable and provides a generic retry-with-backoff
// function.
//
// Key design decisions:
//   - Mutations NEVER retry — enforced by NonRetryableError type
//   - 429, 503, timeout, network errors are retryable
//   - Exponential backoff with jitter to prevent thundering herd
package retry

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

// RetryConfig controls exponential backoff retry behavior.
type RetryConfig struct {
	MaxRetries int           // Maximum number of retry attempts (0 = no retry)
	BaseDelay  time.Duration // Initial delay between retries
	MaxDelay   time.Duration // Maximum delay between retries
	Jitter     time.Duration // Random jitter added to each delay
}

// DefaultRetryConfig returns the default retry configuration.
// 3 retries, 1s base, 30s max, 500ms jitter.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries: 3,
		BaseDelay:  1 * time.Second,
		MaxDelay:   30 * time.Second,
		Jitter:     500 * time.Millisecond,
	}
}

// RetryableError wraps an error with a retryable flag.
// Not all errors should be retried — mutations, for example, should
// never be retried because they have side effects.
type RetryableError struct {
	Err       error
	Retryable bool
}

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// NewRetryableError creates a retryable error.
func NewRetryableError(err error) *RetryableError {
	return &RetryableError{Err: err, Retryable: true}
}

// NewNonRetryableError creates a non-retryable error.
// Used for mutations and other operations that have side effects.
func NewNonRetryableError(err error) *RetryableError {
	return &RetryableError{Err: err, Retryable: false}
}

// IsRetryable checks if an error should be retried.
// Returns true for RetryableError with Retryable=true,
// and for common transient errors (429, 503, timeout, network).
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if re, ok := err.(*RetryableError); ok {
		return re.Retryable
	}
	if err == context.DeadlineExceeded || err == context.Canceled {
		return true
	}
	msg := err.Error()
	for _, pattern := range []string{"503", "429", "timeout", "connection refused", "connection reset", "temporary failure", "rate limit"} {
		if contains(msg, pattern) {
			return true
		}
	}
	return false
}

// RetryWithBackoff executes a function with exponential backoff retry.
// It retries only on retryable errors. Non-retryable errors return immediately.
func RetryWithBackoff[T any](ctx context.Context, config RetryConfig, fn func(attempt int) (T, error)) (T, error) {
	var lastErr error
	var zero T

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		default:
		}

		result, err := fn(attempt)
		if err == nil {
			return result, nil
		}

		if !IsRetryable(err) {
			return zero, err
		}

		lastErr = err
		if attempt >= config.MaxRetries {
			break
		}

		delay := CalculateDelay(attempt, config)
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(delay):
		}
	}

	return zero, fmt.Errorf("retry: %d attempts exhausted: %w", config.MaxRetries+1, lastErr)
}

// CalculateDelay computes the delay for a given attempt with exponential backoff and jitter.
// CalculateDelay computes the delay for a given attempt with exponential backoff and jitter.
func CalculateDelay(attempt int, config RetryConfig) time.Duration {
	delay := config.BaseDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
		if delay >= config.MaxDelay {
			delay = config.MaxDelay
			break
		}
	}
	if delay > config.MaxDelay {
		delay = config.MaxDelay
	}
	if config.Jitter > 0 {
		jitter := time.Duration(rand.Int63n(int64(config.Jitter)))
		delay += jitter - config.Jitter/2
		if delay < 0 {
			delay = 0
		}
	}
	return delay
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
