package context

import (
	"fmt"
)

// applyBudget truncates files to fit within the token budget.
// Truncation priority: file (60) > auto (30) > named sources (50-100).
// Soul (100) is never truncated.
func (b *Builder) applyBudget(files []ContextFile) []ContextFile {
	// Calculate total tokens
	totalTokens := 0
	for _, f := range files {
		totalTokens += f.EstimatedTokens
	}

	if totalTokens <= b.TokenBudget {
		return files // Fits within budget
	}

	// Sort by priority ascending (truncate lowest priority first)
	sorted := make([]ContextFile, len(files))
	copy(sorted, files)

	// Simple insertion sort by priority ascending
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Priority < sorted[j-1].Priority; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}

	// Truncate from lowest priority until we fit
	overage := totalTokens - b.TokenBudget
	for i := range sorted {
		if overage <= 0 {
			break
		}

		// Never truncate soul (priority 100)
		if sorted[i].Name == "soul" {
			continue
		}

		// Truncate a file only if it actually has tokens AND it is either a
		// non-named source or a low-priority (<80) named source. High-priority
		// named sources (soul, agents, …) are preserved.
		if sorted[i].EstimatedTokens > 0 && (sorted[i].Source != "named" || sorted[i].Priority < 80) {
			keepTokens := sorted[i].EstimatedTokens - overage
			if keepTokens < 0 {
				keepTokens = 0
			}

			if keepTokens == 0 {
				// Remove entirely
				overage -= sorted[i].EstimatedTokens
				sorted[i].Content = ""
				sorted[i].Truncated = true
				sorted[i].TruncatedBy = sorted[i].EstimatedTokens
				sorted[i].EstimatedTokens = 0
			} else {
				// Truncate content
				keepChars := keepTokens * 4 // Rough estimate
				originalTokens := sorted[i].EstimatedTokens
				if keepChars < len(sorted[i].Content) {
					sorted[i].Content = sorted[i].Content[:keepChars] + "\n\n[... truncated: " + fmt.Sprintf("%d", originalTokens-keepTokens) + " tokens omitted ...]\n"
					sorted[i].Truncated = true
					sorted[i].TruncatedBy = originalTokens - keepTokens
					sorted[i].EstimatedTokens = keepTokens
				}
				overage -= (originalTokens - keepTokens)
			}
		}
	}

	return sorted
}