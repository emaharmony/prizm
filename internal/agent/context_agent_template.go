package agent

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// TemplateExtract produces a deterministic context block by parsing
// workspace files directly, without using an LLM. It extracts key
// identity fields from SOUL.md, IDENTITY.md, USER.md, HEARTBEAT.md,
// and MEMORY.md to produce a structured summary.
//
// This is used as a fallback when model compression fails, or as the
// primary extraction method when deterministic output is preferred.
func (ca *ContextAgent) TemplateExtract(taskDescription string) string {
	var sb strings.Builder

	// Extract identity from SOUL.md
	if soul := ca.extractFileSection("SOUL.md", []string{"Identity", "Core Role", "Personality"}); soul != "" {
		sb.WriteString(soul)
	}

	// Extract identity from IDENTITY.md
	if identity := ca.extractFileSection("IDENTITY.md", []string{"Identity"}); identity != "" {
		sb.WriteString(identity)
	}

	// Extract preferences from USER.md
	if user := ca.extractFileSection("USER.md", []string{"About", "Working Relationship", "Communication Preferences", "ADHD"}); user != "" {
		sb.WriteString(user)
	}

	// Extract active projects from HEARTBEAT.md
	if heartbeat := ca.extractFileSection("HEARTBEAT.md", []string{"Active Projects", "On Hold", "Constraints", "Model Stack"}); heartbeat != "" {
		sb.WriteString(heartbeat)
	}

	// Extract recent memories
	memDir := filepath.Join(ca.workspaceRoot, "memory")
	memContent := ca.readRecentMemoryFiles(memDir, 5)
	if memContent != "" {
		// Truncate memory to last 500 chars for template extraction
		if len(memContent) > 500 {
			memContent = memContent[len(memContent)-500:]
		}
		sb.WriteString("\n=== recent-memory ===\n")
		sb.WriteString(memContent)
		sb.WriteString("\n")
	}

	result := sb.String()
	if result == "" {
		return ca.fallback()
	}

	// Cap to maxContext chars (roughly maxContext * 4 chars ≈ maxContext tokens)
	maxChars := ca.maxPredictTokens() * 4
	if len(result) > maxChars {
		result = result[:maxChars] + "\n[...truncated...]"
	}

	log.Printf("[CONTEXT-AGENT] template extraction: %d bytes, %d tokens estimated", len(result), len(result)/4)
	return result
}

// extractFileSection reads a workspace file and extracts the specified sections.
// Sections are identified by ## or ### headers. Returns empty string if file
// doesn't exist or no sections match.
func (ca *ContextAgent) extractFileSection(filename string, sections []string) string {
	filePath := filepath.Join(ca.workspaceRoot, filename)
	data, err := os.ReadFile(filePath)
	if err != nil {
		// Try .openclaw/workspace subdirectory
		filePath = filepath.Join(ca.workspaceRoot, ".openclaw", "workspace", filename)
		data, err = os.ReadFile(filePath)
		if err != nil {
			return ""
		}
	}

	content := string(data)
	var sb strings.Builder

	// Parse the file into sections by ## or ### headers
	fileSections := parseMarkdownSections(content)

	// Extract requested sections
	for _, section := range sections {
		// Try exact match first, then case-insensitive prefix match
		for header, body := range fileSections {
			headerLower := strings.ToLower(header)
			sectionLower := strings.ToLower(section)
			if strings.Contains(headerLower, sectionLower) {
				sb.WriteString(fmt.Sprintf("\n=== %s ===\n", header))
				// Truncate section body to 200 chars
				body = strings.TrimSpace(body)
				if len(body) > 200 {
					body = body[:200] + "..."
				}
				sb.WriteString(body)
				sb.WriteString("\n")
				break
			}
		}
	}

	if sb.Len() == 0 {
		// Fallback: extract first 300 chars of the file as a summary
		content = strings.TrimSpace(content)
		if len(content) > 300 {
			content = content[:300] + "..."
		}
		return fmt.Sprintf("\n=== %s ===\n%s\n", filename, content)
	}

	return sb.String()
}

// parseMarkdownSections parses markdown content into a map of header → body.
// Headers are ## or ### lines. The body is everything until the next header.
func parseMarkdownSections(content string) map[string]string {
	sections := make(map[string]string)
	var currentHeader string
	var currentBody strings.Builder

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "### ") || strings.HasPrefix(trimmed, "## ") {
			// Save previous section
			if currentHeader != "" {
				sections[currentHeader] = currentBody.String()
			}
			currentHeader = strings.TrimLeft(trimmed, "# ")
			currentBody.Reset()
		} else {
			currentBody.WriteString(line)
			currentBody.WriteString("\n")
		}
	}
	// Save last section
	if currentHeader != "" {
		sections[currentHeader] = currentBody.String()
	}

	return sections
}