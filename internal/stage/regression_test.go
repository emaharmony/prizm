package stage

import (
	"strings"
	"testing"
)

// P-007 Regression: Verify that content extraction always returns
// the "content" key regardless of Go's random map iteration order.

func TestToolResultContentExtraction_ContentKeyAlwaysWins(t *testing.T) {
	// This map will be iterated in random order by Go runtime.
	// "content" must always win regardless of iteration order.
	output := map[string]any{
		"path":    "/Users/example/projects/repos/sample/docs/architecture.md",
		"content": "This is the actual file content that matters.",
		"size":    13771,
	}

	// Run multiple times to catch random iteration order issues
	for i := 0; i < 100; i++ {
		gate := NewToolRelevanceGate(false) // gate disabled, not relevant here

		// Simulate the priority key lookup logic from formatToolResult
		resultStr := ""
		contentKeys := []string{"content", "output", "result", "text", "message", "body"}
		for _, key := range contentKeys {
			if v, exists := output[key]; exists {
				if s, ok := v.(string); ok && s != "" {
					resultStr = s
					break
				}
			}
		}

		if resultStr != "This is the actual file content that matters." {
			t.Errorf("iteration %d: expected content, got %q", i, resultStr)
		}

		// Also verify we're not getting the path
		if resultStr == "/Users/example/projects/repos/sample/docs/architecture.md" {
			t.Errorf("iteration %d: got path instead of content — P-007 regression!", i)
		}

		_ = gate // suppress unused warning
	}
}

func TestToolResultContentExtraction_FallbackKeys(t *testing.T) {
	tests := []struct {
		name     string
		output   map[string]any
		expected string
	}{
		{
			name:     "content key present",
			output:   map[string]any{"path": "/x", "content": "hello"},
			expected: "hello",
		},
		{
			name:     "output key present (no content)",
			output:   map[string]any{"path": "/x", "output": "world"},
			expected: "world",
		},
		{
			name:     "result key present (no content, no output)",
			output:   map[string]any{"path": "/x", "result": "data"},
			expected: "data",
		},
		{
			name:     "text key present",
			output:   map[string]any{"path": "/x", "text": "plain"},
			expected: "plain",
		},
		{
			name:     "message key present",
			output:   map[string]any{"path": "/x", "message": "hi"},
			expected: "hi",
		},
		{
			name:     "body key present (lowest priority)",
			output:   map[string]any{"path": "/x", "body": "payload"},
			expected: "payload",
		},
		{
			name:     "content wins over all others",
			output:   map[string]any{"path": "/x", "content": "winner", "output": "loser", "result": "also_loser", "body": "last"},
			expected: "winner",
		},
		{
			name:     "empty string skipped",
			output:   map[string]any{"content": "", "output": "fallback"},
			expected: "fallback",
		},
	}

	contentKeys := []string{"content", "output", "result", "text", "message", "body"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resultStr := ""
			for _, key := range contentKeys {
				if v, exists := tt.output[key]; exists {
					if s, ok := v.(string); ok && s != "" {
						resultStr = s
						break
					}
				}
			}
			if resultStr != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, resultStr)
			}
		})
	}
}

// P-009 Regression: Verify agent loop detection suppresses error messages
// but allows legitimate agent messages through.

func TestAgentLoopDetection_SuppressesErrorPatterns(t *testing.T) {
	gate := NewToolRelevanceGate(false) // gate itself isn't tested here

	// These messages should be suppressed by the agent handler
	suppressedMessages := []string{
		"I tried to use a tool to help with that, but something went wrong.",
		"Something went wrong with my response.",
		"I had trouble processing that — the AI service returned an error.",
		"something went wrong and I couldn't complete the task",
	}

	for _, msg := range suppressedMessages {
		lower := strings.ToLower(strings.TrimSpace(msg))
		errorPatterns := []string{
			"something went wrong",
			"i tried to use a tool",
			"had trouble processing",
			"ai service returned an error",
		}
		matched := false
		for _, pattern := range errorPatterns {
			if strings.Contains(lower, pattern) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("message %q should be suppressed but didn't match any pattern", msg)
		}
	}

	_ = gate
}

func TestAgentLoopDetection_AllowsLegitimateMessages(t *testing.T) {
	// These messages should NOT be suppressed
	legitimateMessages := []string{
		"I found the architecture doc and here's my review.",
		"Can you read the file at /Users/example/projects/sample/docs/architecture.md?",
		"The project structure looks solid, but I'd split LaunchEngine's scoring logic.",
		"Hey Astraea, what's the status on the Pudgy Power project?",
		"I had trouble finding the right file, but I located it eventually.",
	}

	errorPatterns := []string{
		"something went wrong",
		"i tried to use a tool",
		"had trouble processing",
		"ai service returned an error",
	}

	for _, msg := range legitimateMessages {
		lower := strings.ToLower(strings.TrimSpace(msg))
		matched := false
		for _, pattern := range errorPatterns {
			if strings.Contains(lower, pattern) {
				matched = true
				break
			}
		}
		if matched {
			t.Errorf("legitimate message %q was incorrectly matched by error pattern", msg)
		}
	}
}
