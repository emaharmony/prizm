package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillWriteTool_CreateSkill(t *testing.T) {
	dir := t.TempDir()
	tool := NewSkillWriteTool(dir)
	result, err := tool.Execute(nil, map[string]any{
		"name":        "debug-nats",
		"description": "Debug NATS connection issues",
		"body":        "# Debug NATS\n\n## When to Use\nWhen NATS is unreachable.\n\n## Procedure\n1. Check nats-server is running\n2. Check port 4222",
		"category":    "debugging",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Error)
	}
	// Verify file was written
	path := filepath.Join(dir, "debug-nats", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("skill file not written: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `name: "debug-nats"`) {
		t.Error("expected frontmatter with quoted name")
	}
	if !strings.Contains(content, "Debug NATS connection issues") {
		t.Error("expected description in frontmatter")
	}
	if !strings.Contains(content, "# Debug NATS") {
		t.Error("expected body content")
	}
}

func TestSkillWriteTool_MissingName(t *testing.T) {
	tool := NewSkillWriteTool(t.TempDir())
	result, _ := tool.Execute(nil, map[string]any{
		"description": "test",
		"body":        "test",
	})
	if result.Success {
		t.Error("expected failure when name is missing")
	}
}

func TestSkillWriteTool_NilDir(t *testing.T) {
	tool := NewSkillWriteTool("")
	result, _ := tool.Execute(nil, map[string]any{
		"name":        "test",
		"description": "test",
		"body":        "test",
	})
	if result.Success {
		t.Error("expected failure with empty skills dir")
	}
}

func TestSanitizeSkillName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Debug NATS", "debug-nats"},
		{"my_skill", "my-skill"},
		{"Already-Clean", "already-clean"},
		{"UPPER CASE", "upper-case"},
		{"  trim  ", "trim"},
		{"a b c", "a-b-c"},
	}
	for _, tt := range tests {
		got := sanitizeSkillName(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeSkillName(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}