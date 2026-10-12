package retry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"nil", nil, false},
		{"explicit retryable", NewRetryableError(fmt.Errorf("503")), true},
		{"explicit non-retryable", NewNonRetryableError(fmt.Errorf("forbidden")), false},
		{"503", fmt.Errorf("server returned 503"), true},
		{"429", fmt.Errorf("rate limit 429"), true},
		{"401 invalid api key", fmt.Errorf("provider returned 401: invalid api key"), false},
		{"403 forbidden", fmt.Errorf("provider returned 403: forbidden"), false},
		{"timeout", fmt.Errorf("connection timeout"), true},
		{"context deadline", context.DeadlineExceeded, true},
		{"context canceled", context.Canceled, true},
		{"generic error", fmt.Errorf("something went wrong"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRetryable(tt.err)
			if got != tt.retryable {
				t.Errorf("IsRetryable(%v) = %v, want %v", tt.err, got, tt.retryable)
			}
		})
	}
}

func TestParseRetryAfterAndTypedDelay(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{"7", 7 * time.Second},
		{now.Add(9 * time.Second).Format(http.TimeFormat), 9 * time.Second},
		{"invalid", 0},
		{"-1", 0},
	} {
		if got := ParseRetryAfter(test.value, now); got != test.want {
			t.Errorf("ParseRetryAfter(%q)=%v want %v", test.value, got, test.want)
		}
	}
	err := NewRetryableErrorAfter(errors.New("rate limited"), 4*time.Second)
	if !IsRetryable(err) || err.RetryAfter() != 4*time.Second {
		t.Fatalf("typed Retry-After lost: %#v", err)
	}
}

func TestRetryWithBackoff_Success(t *testing.T) {
	config := RetryConfig{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond, Jitter: 0}
	result, err := RetryWithBackoff(context.Background(), config, func(attempt int) (string, error) {
		return "success", nil
	})
	if err != nil {
		t.Fatalf("RetryWithBackoff() error = %v", err)
	}
	if result != "success" {
		t.Errorf("result = %q, want success", result)
	}
}

func TestRetryWithBackoff_RetrySuccess(t *testing.T) {
	config := RetryConfig{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond, Jitter: 0}
	result, err := RetryWithBackoff(context.Background(), config, func(attempt int) (string, error) {
		if attempt < 2 {
			return "", NewRetryableError(fmt.Errorf("transient error"))
		}
		return fmt.Sprintf("success on attempt %d", attempt), nil
	})
	if err != nil {
		t.Fatalf("RetryWithBackoff() error = %v", err)
	}
	_ = result
}

func TestRetryWithBackoff_NonRetryableError(t *testing.T) {
	config := RetryConfig{MaxRetries: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 10 * time.Millisecond, Jitter: 0}
	attempt := 0
	_, err := RetryWithBackoff(context.Background(), config, func(a int) (string, error) {
		attempt++
		return "", NewNonRetryableError(fmt.Errorf("mutation failed"))
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if attempt != 1 {
		t.Errorf("non-retryable error should only attempt once, got %d attempts", attempt)
	}
}

func TestRetryWithBackoff_ExhaustedRetries(t *testing.T) {
	config := RetryConfig{MaxRetries: 2, BaseDelay: 1 * time.Millisecond, MaxDelay: 5 * time.Millisecond, Jitter: 0}
	_, err := RetryWithBackoff(context.Background(), config, func(a int) (string, error) {
		return "", NewRetryableError(fmt.Errorf("always fails"))
	})
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
}

func TestRetryWithBackoff_ContextCancellation(t *testing.T) {
	config := RetryConfig{MaxRetries: 10, BaseDelay: 1 * time.Millisecond, MaxDelay: 5 * time.Millisecond, Jitter: 0}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := RetryWithBackoff(ctx, config, func(a int) (string, error) {
		return "", NewRetryableError(fmt.Errorf("slow fail"))
	})
	if err != context.DeadlineExceeded {
		t.Errorf("expected context error, got %v", err)
	}
}

func TestCalculateDelay(t *testing.T) {
	config := RetryConfig{MaxRetries: 5, BaseDelay: 1 * time.Second, MaxDelay: 30 * time.Second, Jitter: 0}
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 1 * time.Second},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{5, 30 * time.Second},
	}
	for _, tt := range tests {
		got := CalculateDelay(tt.attempt, config)
		if got != tt.want {
			t.Errorf("calculateDelay(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestDefaultRetryConfig(t *testing.T) {
	cfg := DefaultRetryConfig()
	if cfg.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", cfg.MaxRetries)
	}
	if cfg.BaseDelay != 1*time.Second {
		t.Errorf("BaseDelay = %v, want 1s", cfg.BaseDelay)
	}
	if cfg.MaxDelay != 30*time.Second {
		t.Errorf("MaxDelay = %v, want 30s", cfg.MaxDelay)
	}
}

func TestNewRetryableError(t *testing.T) {
	err := NewRetryableError(fmt.Errorf("test"))
	if !err.Retryable {
		t.Error("RetryableError should be retryable")
	}
	if err.Error() != "test" {
		t.Errorf("Error() = %q, want test", err.Error())
	}
}

func TestNewNonRetryableError(t *testing.T) {
	err := NewNonRetryableError(fmt.Errorf("test"))
	if err.Retryable {
		t.Error("NonRetryableError should not be retryable")
	}
}

func TestRetryableError_Unwrap(t *testing.T) {
	inner := fmt.Errorf("inner")
	err := NewRetryableError(inner)
	if err.Unwrap() != inner {
		t.Error("Unwrap() should return inner error")
	}
}
