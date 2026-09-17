// Package prompt builds deterministic prompts for Prizm agent tasks.
// It assembles agent name, project, task, and optional Remembrance context
// into a structured prompt that Prizm owns end-to-end.
package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuildPrompt assembles a deterministic prompt for the given task.
// If contextStr is empty, the "Retrieved Context" section is omitted.
func BuildPrompt(agent, project, task, contextStr string) string {
	var sb strings.Builder

	sb.WriteString("# Prizm Agent Task\n\n")
	sb.WriteString("## Agent\n")
	sb.WriteString(agent)
	sb.WriteString("\n\n")
	sb.WriteString("## Project\n")
	sb.WriteString(project)
	sb.WriteString("\n\n")
	sb.WriteString("## Task\n")
	sb.WriteString(task)
	sb.WriteString("\n")

	if strings.TrimSpace(contextStr) != "" {
		sb.WriteString("\n## Retrieved Context\n")
		sb.WriteString(contextStr)
		sb.WriteString("\n")
	}

	sb.WriteString("\n## Rules\n")
	sb.WriteString("- Follow the task directly.\n")
	sb.WriteString("- Use retrieved context when relevant.\n")
	sb.WriteString("- Do not invent files, commands, or results.\n")
	sb.WriteString("- Do not claim work was performed unless Prizm actually performed it.\n")
	sb.WriteString("- If required context is missing, say what is missing.\n")
	sb.WriteString("- Return concise, actionable output.\n")

	return sb.String()
}

// WritePrompt writes the prompt content to prompt.md in the given directory.
// The directory should be the run directory (e.g., runs/<run_id>/).
func WritePrompt(dir, content string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create run directory: %w", err)
	}
	path := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write prompt.md: %w", err)
	}
	return nil
}

// WriteOutput writes the LLM output content to output.md in the given directory.
func WriteOutput(dir, content string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create run directory: %w", err)
	}
	path := filepath.Join(dir, "output.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write output.md: %w", err)
	}
	return nil
}
