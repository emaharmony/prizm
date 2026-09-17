package commitments

import (
	"fmt"
	"strings"
	"time"
)

// FormatPendingForPrompt renders pending commitments as a system prompt
// section. This is injected into the agent's system prompt so the model
// knows what it owes the user and can proactively follow up.
//
// Only commitments due within the delivery window (earliest_due_ms <= now)
// are shown. Past commitments (expired) are filtered out by the store.
func FormatPendingForPrompt(records []CommitmentRecord, now time.Time) string {
	if len(records) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Pending Commitments\n")
	b.WriteString("These are follow-ups you promised or that were inferred from conversation. ")
	b.WriteString("If the due window has arrived, proactively bring them up. ")
	b.WriteString("Don't wait for the user to ask — you offered to follow up.\n\n")

	for _, r := range records {
		dueIn := time.UnixMilli(r.EarliestDueMs).Sub(now)
		dueStr := formatDuration(dueIn)

		b.WriteString(fmt.Sprintf("- **[%s] %s**\n", r.Kind, r.Reason))
		if r.SuggestedText != "" {
			b.WriteString(fmt.Sprintf("  Suggested: \"%s\"\n", r.SuggestedText))
		}
		b.WriteString(fmt.Sprintf("  Due: %s (confidence: %.0f%%)\n", dueStr, r.Confidence*100))
		b.WriteString("\n")
	}

	return b.String()
}

// formatDuration renders a duration as human-readable text.
func formatDuration(d time.Duration) string {
	if d < 0 {
		abs := -d
		if abs < time.Hour {
			return fmt.Sprintf("%d min overdue", int(abs.Minutes()))
		}
		if abs < 24*time.Hour {
			return fmt.Sprintf("%dh overdue", int(abs.Hours()))
		}
		return fmt.Sprintf("%d days overdue", int(abs.Hours()/24))
	}
	if d < time.Hour {
		return fmt.Sprintf("in %d min", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("in %dh", int(d.Hours()))
	}
	return fmt.Sprintf("in %d days", int(d.Hours()/24))
}

// DeliveryConfig controls commitment delivery behavior.
type DeliveryConfig struct {
	// Enabled turns commitment delivery on/off.
	Enabled bool
	// MaxPerSession caps how many commitments to show in one prompt.
	MaxPerSession int
	// ExpireAfterHours marks commitments as expired after this many hours
	// past their latest due window.
	ExpireAfterHours int
}

// DefaultDeliveryConfig returns conservative defaults.
func DefaultDeliveryConfig() DeliveryConfig {
	return DeliveryConfig{
		Enabled:          true,
		MaxPerSession:    5,
		ExpireAfterHours: 72,
	}
}

// Deliver checks for due commitments, formats them for the prompt, and
// marks them as sent. Returns the formatted prompt section (may be empty).
func Deliver(store *Store, scope CommitmentScope, cfg DeliveryConfig, now time.Time) (string, error) {
	if !cfg.Enabled {
		return "", nil
	}

	// Expire old commitments first
	_, err := store.ExpireOld(now, cfg.ExpireAfterHours)
	if err != nil {
		return "", fmt.Errorf("expire old: %w", err)
	}

	// Get due commitments
	due, err := store.ListDue(now)
	if err != nil {
		return "", fmt.Errorf("list due: %w", err)
	}

	// Filter to this scope
	var scoped []CommitmentRecord
	for _, r := range due {
		if r.AgentID == scope.AgentID && r.SessionKey == scope.SessionKey {
			scoped = append(scoped, r)
		}
	}

	// Cap to max per session
	if cfg.MaxPerSession > 0 && len(scoped) > cfg.MaxPerSession {
		scoped = scoped[:cfg.MaxPerSession]
	}

	if len(scoped) == 0 {
		return "", nil
	}

	// Format for prompt
	prompt := FormatPendingForPrompt(scoped, now)

	// Mark as sent
	for _, r := range scoped {
		if err := store.UpdateStatus(r.ID, StatusSent); err != nil {
			// Don't fail the whole delivery if one update fails
			continue
		}
	}

	return prompt, nil
}