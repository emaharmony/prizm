// Package stage provides Prizm's pipeline execution engine (V14a+).
//
// V14b adds WAL crash recovery and idempotency. The retry logic has been
// moved to the shared internal/retry package so it can be used by both
// the stage pipeline and provider chain without import cycles.
//
// This file re-exports the retry types for backward compatibility with
// existing tests and code that imports from the stage package.
package stage

import (
	"context"
	"time"

	"github.com/emaharmony/prizm/internal/retry"
)

// RetryConfig controls exponential backoff retry behavior.
type RetryConfig = retry.RetryConfig

// RetryableError wraps an error with a retryable flag.
type RetryableError = retry.RetryableError

// NewRetryableError creates a retryable error.
func NewRetryableError(err error) *retry.RetryableError {
	return retry.NewRetryableError(err)
}

// NewNonRetryableError creates a non-retryable error.
func NewNonRetryableError(err error) *retry.RetryableError {
	return retry.NewNonRetryableError(err)
}

// IsRetryable checks if an error should be retried.
func IsRetryable(err error) bool {
	return retry.IsRetryable(err)
}

// RetryWithBackoff executes a function with exponential backoff retry.
func RetryWithBackoff[T any](ctx context.Context, config retry.RetryConfig, fn func(attempt int) (T, error)) (T, error) {
	return retry.RetryWithBackoff(ctx, config, fn)
}

// DefaultRetryConfig returns the default retry configuration.
func DefaultRetryConfig() retry.RetryConfig {
	return retry.DefaultRetryConfig()
}

// CalculateDelay computes the delay for a given attempt with exponential backoff and jitter.
func CalculateDelay(attempt int, config retry.RetryConfig) time.Duration {
	return retry.CalculateDelay(attempt, config)
}
