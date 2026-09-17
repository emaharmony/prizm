package prompt_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/prompt"
)

func TestBuildPromptBasic(t *testing.T) {
	result := prompt.BuildPrompt("lumi", "prizm", "Explain the lifecycle", "")

	if !strings.Contains(result, "## Agent\nlumi") {
		t.Error("prompt missing agent section")
	}
	if !strings.Contains(result, "## Project\nprizm") {
		t.Error("prompt missing project section")
	}
	if !strings.Contains(result, "## Task\nExplain the lifecycle") {
		t.Error("prompt missing task section")
	}
	if !strings.Contains(result, "## Rules") {
		t.Error("prompt missing rules section")
	}
	if strings.Contains(result, "## Retrieved Context") {
		t.Error("prompt should not contain context section when context is empty")
	}
}

func TestBuildPromptWithContext(t *testing.T) {
	result := prompt.BuildPrompt("lumi", "prizm", "Analyze code", "Previous discussion about event-driven architecture")

	if !strings.Contains(result, "## Retrieved Context") {
		t.Error("prompt missing context section when context provided")
	}
	if !strings.Contains(result, "Previous discussion about event-driven architecture") {
		t.Error("prompt missing context content")
	}
}

func TestBuildPromptWithEmptyContext(t *testing.T) {
	result := prompt.BuildPrompt("lumi", "prizm", "Test task", "   ")

	if strings.Contains(result, "## Retrieved Context") {
		t.Error("prompt should omit context section for whitespace-only context")
	}
}

func TestBuildPromptRulesContent(t *testing.T) {
	result := prompt.BuildPrompt("lumi", "prizm", "Test", "")

	rules := []string{
		"Follow the task directly",
		"Do not invent files",
		"Do not claim work was performed",
		"Return concise, actionable output",
	}
	for _, rule := range rules {
		if !strings.Contains(result, rule) {
			t.Errorf("prompt missing rule: %s", rule)
		}
	}
}

func TestWritePrompt(t *testing.T) {
	tmpDir := t.TempDir()
	content := prompt.BuildPrompt("lumi", "prizm", "Test task", "")

	err := prompt.WritePrompt(tmpDir, content)
	if err != nil {
		t.Fatalf("WritePrompt failed: %v", err)
	}

	path := filepath.Join(tmpDir, "prompt.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read prompt.md: %v", err)
	}

	if string(data) != content {
		t.Error("prompt.md content does not match")
	}
}

func TestWriteOutput(t *testing.T) {
	tmpDir := t.TempDir()
	content := "This is the LLM output."

	err := prompt.WriteOutput(tmpDir, content)
	if err != nil {
		t.Fatalf("WriteOutput failed: %v", err)
	}

	path := filepath.Join(tmpDir, "output.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read output.md: %v", err)
	}

	if string(data) != content {
		t.Error("output.md content does not match")
	}
}

func TestWritePromptCreatesDir(t *testing.T) {
	tmpDir := t.TempDir()
	nestedDir := filepath.Join(tmpDir, "runs", "run_test123")

	err := prompt.WritePrompt(nestedDir, "test prompt")
	if err != nil {
		t.Fatalf("WritePrompt with nested dir failed: %v", err)
	}

	path := filepath.Join(nestedDir, "prompt.md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("prompt.md was not created in nested directory")
	}
}
