package session

import (
	"strings"
	"time"
)

// SessionMemory is a structured continuity document produced during compaction.
// When a session's context exceeds its budget, the conversation is summarized
// into this structure, which becomes the new context anchor — allowing the
// agent to continue long autonomous runs without losing the thread.
//
// Inspired by OpenClaude's SessionMemory template.
type SessionMemory struct {
	Title        string    // What is this session about?
	CurrentState string    // Where are we right now?
	Task         string    // What is the active task?
	Files        []string  // Which files have been modified?
	Errors       []string  // What errors have we hit?
	Learnings    []string  // What did we learn?
	Worklog      []string  // What steps have we completed?
	KeyResults   string    // What are the key results so far?
	CompactedAt  time.Time // When was this summary produced?
}

// Format renders the SessionMemory as a markdown block suitable for injection
// into the system prompt as a continuity anchor.
func (sm *SessionMemory) Format() string {
	var b strings.Builder
	b.WriteString("## Session Continuity\n\n")
	if sm.Title != "" {
		b.WriteString("**Title:** " + sm.Title + "\n\n")
	}
	if sm.CurrentState != "" {
		b.WriteString("**Current State:** " + sm.CurrentState + "\n\n")
	}
	if sm.Task != "" {
		b.WriteString("**Active Task:** " + sm.Task + "\n\n")
	}
	if len(sm.Files) > 0 {
		b.WriteString("**Files Modified:**\n")
		for _, f := range sm.Files {
			b.WriteString("- " + f + "\n")
		}
		b.WriteString("\n")
	}
	if len(sm.Errors) > 0 {
		b.WriteString("**Errors Encountered:**\n")
		for _, e := range sm.Errors {
			b.WriteString("- " + e + "\n")
		}
		b.WriteString("\n")
	}
	if len(sm.Learnings) > 0 {
		b.WriteString("**What We Learned:**\n")
		for _, l := range sm.Learnings {
			b.WriteString("- " + l + "\n")
		}
		b.WriteString("\n")
	}
	if len(sm.Worklog) > 0 {
		b.WriteString("**Worklog:**\n")
		for _, w := range sm.Worklog {
			b.WriteString("- " + w + "\n")
		}
		b.WriteString("\n")
	}
	if sm.KeyResults != "" {
		b.WriteString("**Key Results:** " + sm.KeyResults + "\n\n")
	}
	b.WriteString("*This summary was produced by context compaction. Continue from Current State. Do not repeat completed worklog items.*\n")
	return b.String()
}

// BuildSessionMemoryFromMessages produces a SessionMemory by scanning
// conversation messages for key information. This is a heuristic extraction —
// it looks for tool calls, errors, file modifications, and task descriptions
// in the message history.
//
// For higher-quality summaries, the caller can use an LLM to produce the
// SessionMemory instead. This function is the fallback when no LLM is available
// or for fast compaction.
func BuildSessionMemoryFromMessages(messages []Message) *SessionMemory {
	sm := &SessionMemory{
		CompactedAt: time.Now(),
	}

	for _, msg := range messages {
		switch msg.Role {
		case "user":
			if sm.Task == "" && len(msg.Content) > 0 {
				// First user message is likely the task
				sm.Task = truncateMsg(msg.Content, 200)
				if sm.Title == "" {
					sm.Title = truncateMsg(msg.Content, 80)
				}
			}
		case "agent":
			// Look for file references in agent responses
			extractFiles(msg.Content, &sm.Files)
			// Look for error indicators
			extractErrors(msg.Content, &sm.Errors)
			// Track as worklog
			if len(msg.Content) > 0 {
				sm.Worklog = append(sm.Worklog, truncateMsg(msg.Content, 100))
			}
		case "tool":
			// Tool calls — extract file paths and errors
			extractFiles(msg.Content, &sm.Files)
			if strings.Contains(strings.ToLower(msg.Content), "error") ||
				strings.Contains(strings.ToLower(msg.Content), "failed") {
				sm.Errors = append(sm.Errors, truncateMsg(msg.Content, 100))
			}
		}
	}

	// Current state is the last agent message
	if len(sm.Worklog) > 0 {
		sm.CurrentState = sm.Worklog[len(sm.Worklog)-1]
	}

	// Cap worklog to last 10 items to keep it manageable
	if len(sm.Worklog) > 10 {
		sm.Worklog = sm.Worklog[len(sm.Worklog)-10:]
	}

	// Cap errors to last 5
	if len(sm.Errors) > 5 {
		sm.Errors = sm.Errors[len(sm.Errors)-5:]
	}

	return sm
}

// truncateMsg returns the first n characters of s.
func truncateMsg(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	// Truncate at rune boundary to avoid splitting multi-byte UTF-8
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// extractFiles looks for file path patterns in text and adds them to the files list.
// Matches tokens that start with / or ./ and look like file paths (have extension,
// reasonable length). Avoids false positives from URLs, version strings, ratios.
func extractFiles(text string, files *[]string) {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Scan for path-like tokens within the line
		for i := 0; i < len(line); i++ {
			if line[i] != '/' {
				continue
			}
			// Must be at start of line OR preceded by a space (standalone token)
			if i > 0 && line[i-1] != ' ' && line[i-1] != '\t' {
				continue
			}
			// Extract the path token (up to space or end of line)
			rest := line[i:]
			end := strings.IndexAny(rest, " \t,;()'")
			if end < 0 {
				end = len(rest)
			}
			path := rest[:end]
			// Must have a dot (extension), be > 3 chars, < 200 chars
			// Reject URLs (must not contain ://)
			if strings.Contains(path, "://") {
				continue
			}
			if strings.Contains(path, ".") && len(path) > 3 && len(path) < 200 {
				if !contains(*files, path) {
					*files = append(*files, path)
				}
			}
		}
	}
}

// extractErrors looks for error indicators in text.
func extractErrors(text string, errors *[]string) {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "error:") || strings.Contains(lower, "failed:") || strings.Contains(lower, "panic:") {
		// Extract the error line
		for _, line := range strings.Split(text, "\n") {
			ll := strings.ToLower(line)
			if strings.Contains(ll, "error:") || strings.Contains(ll, "failed:") || strings.Contains(ll, "panic:") {
				if !contains(*errors, line) {
					*errors = append(*errors, truncateMsg(line, 150))
				}
			}
		}
	}
}

func contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}