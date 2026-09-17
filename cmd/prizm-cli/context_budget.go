package main

import (
	"fmt"
	"log"
	"strings"

	"github.com/emaharmony/prizm/internal/provider"
)

// estimateTokenCount provides a rough estimate of the token count for a slice
// of chat messages. Uses a 3.2 chars-per-token heuristic which is conservative
// for mixed code/JSON/markdown content (GLM models tokenize densely).
func estimateTokenCount(messages []provider.ChatMessage) int {
	total := 0
	for _, msg := range messages {
		total += len(msg.Content) * 10 / 32 // ~3.2 chars per token
		for _, tc := range msg.ToolCalls {
			total += len(tc.Function.Name) * 10 / 32
			for _, v := range tc.Function.Arguments {
				total += len(fmt.Sprintf("%v", v)) * 10 / 32
			}
		}
		if msg.Role == "system" {
			total += len(msg.Content) * 10 / 32
		}
	}
	return total
}

// compressToolResults progressively compresses older tool results to keep the
// conversation within the token budget. Uses a two-tier strategy:
// - Tier 1 (keepRecent results): preserved in full
// - Tier 2 (older results): compressed to a brief digest with tool name,
//   success/failure, and a one-line summary
// Returns the number of messages compressed.
func compressToolResults(messages []provider.ChatMessage, keepRecent int) int {
	compressed := 0

	// Collect indices of tool result messages
	toolResultIndices := []int{}
	for i, msg := range messages {
		if msg.Role == "tool" {
			toolResultIndices = append(toolResultIndices, i)
		}
	}

	// Don't compress if we have fewer results than keepRecent
	if len(toolResultIndices) <= keepRecent {
		return 0
	}

	// Compress all tool results except the last keepRecent
	compressFrom := len(toolResultIndices) - keepRecent

	for i := 0; i < compressFrom && i < len(toolResultIndices); i++ {
		idx := toolResultIndices[i]
		msg := messages[idx]

		// Already compressed (short digest format) — skip
		if strings.HasPrefix(msg.Content, "[SUMMARY]") {
			continue
		}

		// Compress to a brief digest
		digest := compressToolDigest(msg.Content, msg.ToolID)
		if digest != msg.Content {
			messages[idx].Content = digest
			compressed++
		}
	}

	return compressed
}

// compressToolDigest compresses a tool result into a brief digest format.
// Preserves the key information while discarding verbose output.
func compressToolDigest(content string, toolID string) string {
	// Determine success/failure from content patterns
	isError := strings.Contains(content, `"error"`) ||
		strings.Contains(content, `"Error"`) ||
		strings.Contains(content, "failed") ||
		strings.Contains(content, "error:") ||
		strings.Contains(content, "denied")

	// Extract key info from common tool result patterns
	// JSON results: try to extract a summary line
	lines := strings.Split(content, "\n")

	// For very short results, just truncate to first line
	if len(lines) <= 2 && len(content) <= 150 {
		if len(content) <= 150 {
			return content // Already short enough
		}
	}

	// For JSON results, extract key fields
	if strings.HasPrefix(content, "{") {
		summary := extractJSONSummary(content, isError)
		return summary
	}

	// For multi-line results, take first meaningful line + line count
	firstLine := ""
	lineCount := len(lines)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			firstLine = trimmed
			break
		}
	}

	if firstLine == "" {
		firstLine = lines[0]
	}

	// Truncate first line if too long
	if len(firstLine) > 120 {
		firstLine = firstLine[:117] + "..."
	}

	status := "✓"
	if isError {
		status = "✗"
	}

	return fmt.Sprintf("[SUMMARY] %s %s (%d lines) %s", status, toolID, lineCount, firstLine)
}

// extractJSONSummary pulls key fields from JSON tool results for a compact digest.
func extractJSONSummary(content string, isError bool) string {
	// Common patterns in our tool results:
	// - plan_list: "- P-001: Title [status]"
	// - plan_update: "Step S1 of plan P-001 updated to in_progress"
	// - search_files: {"match_count": N, ...}
	// - git_status: branch info
	// - memory_search: {"count": N, ...}
	// - read_file: file content
	// - list_dir: directory listing

	status := "✓"
	if isError {
		status = "✗"
	}

	// Try to extract match_count (search results)
	if idx := strings.Index(content, `"match_count"`); idx != -1 {
		// Extract the number
		rest := content[idx:]
		if end := strings.IndexByte(rest, ','); end != -1 {
			if end > 30 {
				end = 30
			}
			return fmt.Sprintf("[SUMMARY] %s search: %s", status, rest[:end])
		}
	}

	// Try to extract count (memory search)
	if idx := strings.Index(content, `"count"`); idx != -1 {
		rest := content[idx:]
		if end := strings.IndexByte(rest, ','); end != -1 {
			if end > 30 {
				end = 30
			}
			return fmt.Sprintf("[SUMMARY] %s memory: %s", status, rest[:end])
		}
	}

	// For other JSON, take first 150 chars
	truncated := content
	if len(truncated) > 150 {
		truncated = truncated[:147] + "..."
	}

	return fmt.Sprintf("[SUMMARY] %s %s", status, truncated)
}

// contextBudget manages context window estimation and compression in the
// agentic loop. It monitors the estimated token count and compresses older
// tool results when the conversation exceeds the budget.
type contextBudget struct {
	modelContextTokens int     // Maximum context tokens for the model
	warnThreshold      float64 // Warn when tokens exceed this fraction (0.4 = 40%)
	compressThreshold  float64 // Compress when tokens exceed this fraction (0.5 = 50%)
	keepRecent         int     // Number of recent tool results to keep uncompressed
}

// defaultContextBudget returns a budget configured for the given model's context size.
func defaultContextBudget(modelContextTokens int) contextBudget {
	return contextBudget{
		modelContextTokens: modelContextTokens,
		warnThreshold:      0.40,
		compressThreshold:  0.50,
		keepRecent:         6,
	}
}

// checkAndCompress estimates the current token count and compresses if needed.
// Returns the estimated token count after any compression.
func (cb *contextBudget) checkAndCompress(messages []provider.ChatMessage, iteration int) int {
	estimated := estimateTokenCount(messages)
	budget := cb.modelContextTokens

	if budget <= 0 {
		return estimated // No budget set, skip compression
	}

	ratio := float64(estimated) / float64(budget)

	if ratio >= cb.compressThreshold {
		compressed := compressToolResults(messages, cb.keepRecent)
		if compressed > 0 {
			log.Printf("[CONTEXT-BUDGET] iteration %d: %.0f%% of context budget (%d/%d tokens), compressed %d older tool results to digests", iteration, ratio*100, estimated, budget, compressed)
		}
		// Re-estimate after compression
		estimated = estimateTokenCount(messages)
		ratio = float64(estimated) / float64(budget)
	}

	if ratio >= cb.warnThreshold {
		log.Printf("[CONTEXT-BUDGET] iteration %d: %.0f%% of context budget (%d/%d tokens)", iteration, ratio*100, estimated, budget)
	}

	return estimated
}

// modelContextTokens maps known model names to their maximum context window size.
func getModelContextTokens(model string) (int, bool) {
	known := map[string]int{
		"glm-5.3-flash:cloud":  262144, // current primary
		"glm-5.1:cloud":         202752, // legacy alias
		"glm-5.2:cloud":        202752, // legacy alias
		"glm-4:cloud":           131072,
		"deepseek-v4-pro:cloud": 131072,
		"deepseek-v4-flash:cloud": 131072,
		"qwen3.5:4b":            32768,
		"qwen3.5:9b":            65536,
	}
	tokens, ok := known[model]
	return tokens, ok
}