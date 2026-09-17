package commitments

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ExtractionResult is what the model returns when extracting commitments.
type ExtractionResult struct {
	Items []ExtractionItem `json:"items"`
}

// ExtractionItem is a single extracted commitment candidate.
type ExtractionItem struct {
	Action         string  `json:"action"`           // "commitment" or "skip"
	Kind          string  `json:"kind"`             // event_check_in, deadline_check, care_check_in, open_loop
	Sensitivity   string  `json:"sensitivity"`       // routine, personal, care
	Source        string  `json:"source"`           // inferred_user_context, agent_promise
	Reason        string  `json:"reason"`           // Why this commitment exists
	SuggestedText string  `json:"suggested_text"`    // What to say when delivering
	DedupeKey     string  `json:"dedupe_key"`        // For deduplication
	Confidence    float64 `json:"confidence"`        // 0.0-1.0
	DueWindow     DueWindow `json:"due_window"`
}

// DueWindow defines when a commitment should be delivered.
type DueWindow struct {
	Earliest string `json:"earliest"` // ISO 8601 or relative ("tomorrow", "in 3 days")
	Latest   string `json:"latest"`   // ISO 8601 or relative
	Timezone string `json:"timezone"` // IANA timezone
}

// ExtractionPrompt builds the system prompt for commitment extraction.
// The model receives the conversation and returns JSON with extracted items.
func ExtractionPrompt(userText, assistantText string, timezone string) string {
	return fmt.Sprintf(`You extract follow-up commitments from a conversation between a user and an AI assistant.

Analyze the conversation for:
1. **Promises** — "I'll look into that", "let me check tomorrow", "I'll get back to you by Friday"
2. **Deadlines** — "this needs to be done by [date]", "before the launch"
3. **Care check-ins** — "how are you feeling about X?", "let me know how it goes"
4. **Open loops** — topics that were raised but not resolved

For each commitment found, return a JSON object with action "commitment". For things that are NOT commitments, use action "skip".

Conversation:
User: %s
Assistant: %s

Return JSON:
{
  "items": [
    {
      "action": "commitment",
      "kind": "event_check_in|deadline_check|care_check_in|open_loop",
      "sensitivity": "routine|personal|care",
      "source": "inferred_user_context|agent_promise",
      "reason": "Why this commitment exists (one sentence)",
      "suggested_text": "What to say when delivering the reminder",
      "dedupe_key": "unique key for deduplication",
      "confidence": 0.0-1.0,
      "due_window": {
        "earliest": "ISO 8601 or relative time",
        "latest": "ISO 8601 or relative time",
        "timezone": "%s"
      }
    }
  ]
}

Rules:
- Only extract genuine commitments, not casual mentions
- Confidence < 0.5 for weak signals
- Confidence > 0.8 for explicit promises
- If nothing commitment-worthy, return {"items": []}
- Be conservative — false positives are worse than false negatives
`, userText, assistantText, timezone)
}

// ParseExtraction parses the model's JSON response into ExtractionItems.
func ParseExtraction(response string) ([]ExtractionItem, error) {
	response = strings.TrimSpace(response)
	// Strip markdown code fences if present
	if strings.HasPrefix(response, "```") {
		lines := strings.Split(response, "\n")
		var inner []string
		inFence := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inFence = !inFence
				continue
			}
			if inFence {
				inner = append(inner, line)
			}
		}
		response = strings.Join(inner, "\n")
	}

	var result ExtractionResult
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, fmt.Errorf("parse extraction: %w", err)
	}

	// Filter out skip items
	var items []ExtractionItem
	for _, item := range result.Items {
		if item.Action == "commitment" {
			items = append(items, item)
		}
	}
	return items, nil
}

// ItemToRecord converts an ExtractionItem to a CommitmentRecord.
func ItemToRecord(item ExtractionItem, scope CommitmentScope, now time.Time) (CommitmentRecord, error) {
	kind := CommitmentKind(item.Kind)
	sensitivity := CommitmentSensitivity(item.Sensitivity)
	source := CommitmentSource(item.Source)

	if !ValidKinds[kind] {
		return CommitmentRecord{}, fmt.Errorf("invalid kind: %s", item.Kind)
	}
	if !ValidSensitivities[sensitivity] {
		return CommitmentRecord{}, fmt.Errorf("invalid sensitivity: %s", item.Sensitivity)
	}
	if !ValidSources[source] {
		return CommitmentRecord{}, fmt.Errorf("invalid source: %s", item.Source)
	}

	earliestMs, latestMs := parseDueWindow(item.DueWindow, now)

	id := generateID(scope.AgentID, now)

	return CommitmentRecord{
		ID:              id,
		Kind:            kind,
		Sensitivity:     sensitivity,
		Source:          source,
		Status:          StatusPending,
		Reason:          item.Reason,
		SuggestedText:   item.SuggestedText,
		DedupeKey:       item.DedupeKey,
		Confidence:      item.Confidence,
		EarliestDueMs:   earliestMs,
		LatestDueMs:     latestMs,
		Timezone:        item.DueWindow.Timezone,
		AgentID:         scope.AgentID,
		SessionKey:      scope.SessionKey,
		Channel:        scope.Channel,
		SenderID:       scope.SenderID,
		CreatedAtMs:     now.UnixMilli(),
		UpdatedAtMs:     now.UnixMilli(),
		ExpiresAtMs:     latestMs + int64(72*time.Hour/time.Millisecond), // 72h after latest due
	}, nil
}

// parseDueWindow converts relative or ISO 8601 time strings to Unix ms.
func parseDueWindow(dw DueWindow, now time.Time) (int64, int64) {
	earliest := parseTime(dw.Earliest, now)
	latest := parseTime(dw.Latest, now)

	// If only earliest is set, latest defaults to earliest + 24h
	if latest == 0 && earliest > 0 {
		latest = earliest + int64(24*time.Hour/time.Millisecond)
	}
	// If neither is set, default to 24h from now
	if earliest == 0 {
		earliest = now.Add(24 * time.Hour).UnixMilli()
		latest = now.Add(48 * time.Hour).UnixMilli()
	}

	return earliest, latest
}

// parseTime handles ISO 8601 and relative time strings.
func parseTime(s string, now time.Time) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}

	// Try ISO 8601
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixMilli()
	}

	// Relative time: "tomorrow", "in 3 days", "next week"
	lower := strings.ToLower(s)
	switch {
	case strings.Contains(lower, "tomorrow"):
		return now.Add(24 * time.Hour).UnixMilli()
	case strings.Contains(lower, "next week"):
		return now.Add(7 * 24 * time.Hour).UnixMilli()
	case strings.Contains(lower, "in "):
		// Extract number + unit
		var num int
		var unit string
		fmt.Sscanf(lower, "in %d %s", &num, &unit)
		if num > 0 {
			dur := time.Duration(num) * time.Hour
			if strings.Contains(unit, "day") {
				dur = time.Duration(num) * 24 * time.Hour
			} else if strings.Contains(unit, "week") {
				dur = time.Duration(num) * 7 * 24 * time.Hour
			}
			return now.Add(dur).UnixMilli()
		}
	}

	return 0
}

// generateID creates a unique commitment ID.
func generateID(agentID string, now time.Time) string {
	return fmt.Sprintf("commit_%s_%d", agentID, now.UnixNano())
}