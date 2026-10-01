package toolloop

import "testing"

func TestStripThinkingContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no thinking tags", "Hello, world!", "Hello, world!"},
		{"closed think block", "<think>Let me analyze this.</think>The answer is 42.", "The answer is 42."},
		{"closed thinking block", "<thinking>reasoning here</thinking>Final answer.", "Final answer."},
		{"unclosed think block drops rest", "Good answer.<think>and then more reasoning that never closes", "Good answer."},
		{"multiple think blocks", "<think>first.</think><think>second.</think>Done", "Done"},
		{"leading whitespace trimmed", "<think>x.</think>   Result here", "Result here"},
		{"only a think block", "<think>all reasoning", ""},
		{"think block in middle", "Before <think>hidden</think> after", "Before  after"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripThinkingContent(tt.input)
			if got != tt.want {
				t.Errorf("stripThinkingContent() = %q, want %q", got, tt.want)
			}
		})
	}
}
