package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SkillWriteTool lets an agent create or update a SKILL.md file.
// This enables self-improvement — after completing a complex task,
// the agent can write a skill capturing what it learned.
//
// The tool is policy-gated: it can only write to the configured skills
// directory (typically <workspace>/skills/<name>/SKILL.md).
type SkillWriteTool struct {
	SkillsDir string // Root directory for skills (e.g., workspace/skills)
}

// NewSkillWriteTool creates a skill_write tool that writes to the given skills directory.
func NewSkillWriteTool(skillsDir string) *SkillWriteTool {
	return &SkillWriteTool{SkillsDir: skillsDir}
}

func (t *SkillWriteTool) Name() string { return "skill_create" }

func (t *SkillWriteTool) Description() string {
	return "Create or update a SKILL.md skill file. Use after completing a complex task to capture what you learned as a reusable skill. The skill will be available for future use via use_skill."
}

func (t *SkillWriteTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"name":        {Type: "string", Description: "Skill name (lowercase, hyphenated, e.g., 'debug-nats-connection')", Required: true},
			"description": {Type: "string", Description: "One-line description of what the skill does (max 60 chars)", Required: true},
			"body":        {Type: "string", Description: "The full SKILL.md body content (markdown instructions the agent follows when the skill is invoked)", Required: true},
			"category":    {Type: "string", Description: "Optional category for the skill (e.g., 'debugging', 'devops', 'testing')", Required: false},
		},
		Output: ParamSpec{Type: "object", Description: "The created skill path and name"},
	}
}

func (t *SkillWriteTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	if t.SkillsDir == "" {
		return ToolResult{Success: false, Error: "skills directory not configured"}, nil
	}

	name, _ := input["name"].(string)
	if name == "" {
		return ToolResult{Success: false, Error: "skill_create requires a 'name'"}, nil
	}
	// Sanitize name — only lowercase, hyphens, alphanumeric
	name = sanitizeSkillName(name)
	if name == "" {
		return ToolResult{Success: false, Error: "invalid skill name (use lowercase letters, numbers, hyphens)"}, nil
	}

	description, _ := input["description"].(string)
	if description == "" {
		return ToolResult{Success: false, Error: "skill_create requires a 'description'"}, nil
	}
	if len(description) > 60 {
		description = string([]rune(description)[:60]) // rune-safe truncation
	}

	body, _ := input["body"].(string)
	if body == "" {
		return ToolResult{Success: false, Error: "skill_create requires 'body' content"}, nil
	}

	category, _ := input["category"].(string)

	// Build the SKILL.md content with properly escaped YAML frontmatter
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %q\n", name))
	sb.WriteString(fmt.Sprintf("description: %q\n", description))
	if category != "" {
		sb.WriteString(fmt.Sprintf("metadata:\n  hermes:\n    category: %q\n", category))
	}
	sb.WriteString("---\n\n")
	sb.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		sb.WriteString("\n")
	}

	// Write to <skillsDir>/<name>/SKILL.md
	skillDir := filepath.Join(t.SkillsDir, name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("create skill directory: %v", err)}, nil
	}

	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(sb.String()), 0644); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("write skill file: %v", err)}, nil
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"name":    name,
			"path":    skillPath,
			"message": fmt.Sprintf("Skill '%s' created at %s. It will be available via use_skill on next session.", name, skillPath),
		},
	}, nil
}

// sanitizeSkillName lowercases and strips to valid skill name characters.
func sanitizeSkillName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r == ' ' || r == '_' {
			b.WriteRune('-')
		}
	}
	result := strings.Trim(b.String(), "-")
	return result
}