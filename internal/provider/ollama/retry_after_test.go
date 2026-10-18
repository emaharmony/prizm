package ollama

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"120", 120 * time.Second},
		{"invalid", 0},
		{"-4", 0},
		{"", 0},
	} {
		if got := parseRetryAfter(tc.value, now); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", tc.value, got, tc.want)
		}
	}
	if got := parseRetryAfter(now.Add(90*time.Second).UTC().Format(http.TimeFormat), now); got != 90*time.Second {
		t.Errorf("HTTP-date Retry-After = %s, want 90s", got)
	}
}
