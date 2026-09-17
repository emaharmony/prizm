package context

import (
	"fmt"
	"strings"
)

// ContextFile represents a workspace file that has been read and processed.
type ContextFile struct {
	Name            string // Short name (e.g., "soul")
	Path            string // Full file path
	Content         string // Raw file content
	SizeBytes       int    // File size in bytes
	EstimatedTokens int    // Rough token estimate (content / 4)
	Priority        int    // Truncation priority (higher = kept longer)
	Source          string // "named", "auto", "file", "memory", "correspondence"
	Truncated       bool   // Whether content was truncated
	TruncatedBy     int    // Number of tokens removed by truncation
}

// InjectedContext is the result of context injection.
type InjectedContext struct {
	Files           []ContextFile
	TotalTokens     int
	Truncated       bool
	ContentHash     string // SHA-256 of raw concatenated content
	FormattedString string // The formatted context string for LLM prompt
}

// formatContext produces the formatted context string for LLM injection.
func formatContext(files []ContextFile) string {
	var sb strings.Builder

	for _, f := range files {
		if f.Content == "" {
			continue
		}

		sb.WriteString(fmt.Sprintf("## Workspace: %s\n", f.Name))
		if f.Truncated {
			sb.WriteString(fmt.Sprintf("[Truncated: %d tokens omitted]\n\n", f.TruncatedBy))
		}
		sb.WriteString(f.Content)
		sb.WriteString("\n\n")
	}

	return sb.String()
}

// estimateTokens provides a rough token estimate.
// English text averages ~4 characters per token.
func estimateTokens(content string) int {
	return len(content) / 4
}