package main

import (
	"fmt"
	"strings"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/remembrance"
	"github.com/emaharmony/prizm/internal/session"
)

// lowConfidenceThreshold is the score below which Remembrance results are
// considered weak. When all results fall below this threshold, a nudge is
// appended encouraging the model to use the memory_search tool.
const lowConfidenceThreshold = 0.30

// remembranceMemoryBlock formats the Remembrance context response into a
// system prompt block. When structured ContextJSON is available, each memory
// is shown with its score and reason so the model can assess relevance.
// When all scores are below lowConfidenceThreshold, a nudge is appended
// suggesting the model use the memory_search tool for more targeted results.
func remembranceMemoryBlock(ctx *remembrance.ContextPackResponse) string {
	if ctx == nil {
		return ""
	}

	// Prefer structured ContextJSON (has scores + reasons)
	if ctx.ContextJSON != nil && len(ctx.ContextJSON.Memories) > 0 {
		return formatStructuredMemories(ctx)
	}

	// Fall back to ContextMarkdown (no scores available)
	if strings.TrimSpace(ctx.ContextMarkdown) != "" {
		return "Long-term semantic memory from Remembrance. Use this below the exact recent local transcript if they conflict:\n" + ctx.ContextMarkdown
	}

	return ""
}

// formatStructuredMemories renders memory results with scores and a
// low-confidence nudge when appropriate.
func formatStructuredMemories(ctx *remembrance.ContextPackResponse) string {
	var parts []string
	maxScore := 0.0

	for _, mem := range ctx.ContextJSON.Memories {
		if strings.TrimSpace(mem.Summary) == "" {
			continue
		}
		// Track highest score for nudge decision
		if mem.Score > maxScore {
			maxScore = mem.Score
		}

		// Truncate long summaries
		summary := mem.Summary
		if len(summary) > 200 {
			summary = summary[:200] + "..."
		}

		// Format: [score: 0.21, loosely related] Title — summary...
		reason := mem.Reason
		if reason == "" {
			reason = "no reason provided"
		}
		parts = append(parts, fmt.Sprintf("- [score: %.2f, %s] %s — %s",
			mem.Score, reason, mem.Title, summary))
	}

	if len(parts) == 0 {
		return ""
	}

	header := "## Long-term Memory (from Remembrance)\n"
	header += "Use this below the exact recent local transcript if they conflict.\n\n"

	block := header + strings.Join(parts, "\n")

	// Low-confidence nudge: if the best result is below threshold, suggest
	// the model search for more specific context herself.
	if maxScore < lowConfidenceThreshold {
		block += fmt.Sprintf("\n\n⚠️ All memory results are low-confidence (best score: %.2f). "+
			"These may not be relevant to the current conversation. "+
			"Consider using the memory_search tool to search for more specific context about this topic.",
			maxScore)
	}

	return block
}

func localRecentSummary(sess *session.Session) string {
	if sess == nil {
		return ""
	}
	var lines []string
	for _, msg := range sess.Messages {
		if msg.Role == "system" && strings.Contains(msg.Content, "Local rolling summary") {
			lines = append(lines, msg.Content)
			continue
		}
		if msg.Role != "user" && msg.Role != "agent" {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		if len(content) > 240 {
			content = content[:240] + "..."
		}
		speaker := msg.Role
		if msg.Role == "agent" && msg.AgentID != "" {
			speaker = msg.AgentID
		}
		lines = append(lines, speaker+": "+content)
	}
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return strings.Join(lines, "\n")
}

func channelRoleContext(role *orchestrator.ChannelRole) string {
	if role == nil {
		return ""
	}
	if role.Context != "" {
		return role.Context
	}
	return role.Role
}