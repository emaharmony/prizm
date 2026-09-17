// Package safety provides Prizm's security infrastructure.
//
// Security layers:
//  1. Path containment (path.go) — file operations stay within workspace root
//  2. Prompt injection defense (this file) — detect and neutralize injection attempts
//  3. Input sanitization — strip dangerous content from user inputs
//  4. Rate limiting — prevent abuse from rapid-fire requests
//  5. Command injection prevention — validate all tool inputs before execution
//
// Design principle: Defense in depth. Each layer catches what the layer above misses.
// The LLM is treated as untrusted — it can be tricked, so we never let it decide
// what's safe. Policy is deterministic and enforced at the tool execution boundary.
package safety

import (
	"strings"
	"unicode"
)

// InjectionCheckResult contains the outcome of a prompt injection scan.
type InjectionCheckResult struct {
	Clean     bool     `json:"clean"`
	Severity  string   `json:"severity"`            // "none", "low", "medium", "high", "critical"
	Flags     []string `json:"flags"`               // What patterns were detected
	Sanitized string   `json:"sanitized,omitempty"` // Sanitized version of input
}

// injectionPatterns are patterns that commonly appear in prompt injection attacks.
// These are checked against user input BEFORE it reaches the LLM.
var injectionPatterns = []struct {
	pattern  string
	severity string
	flag     string
}{
	// System prompt extraction attempts
	{"ignore previous instructions", "high", "instruction_override"},
	{"ignore all previous", "high", "instruction_override"},
	{"forget your instructions", "high", "instruction_override"},
	{"disregard your instructions", "high", "instruction_override"},
	{"you are now", "high", "instruction_override"},
	{"new instructions", "high", "instruction_override"},
	{"override your", "high", "instruction_override"},
	{"system prompt", "medium", "prompt_extraction"},
	{"reveal your prompt", "medium", "prompt_extraction"},
	{"show your instructions", "medium", "prompt_extraction"},
	{"print your instructions", "medium", "prompt_extraction"},

	// Tool execution manipulation
	{"execute", "medium", "command_attempt"},
	{"run command", "medium", "command_attempt"},
	{"shell command", "high", "command_attempt"},
	{"bash", "medium", "command_attempt"},
	{"sudo", "high", "privilege_escalation"},
	{"rm -rf", "critical", "destructive_command"},
	{"format c:", "critical", "destructive_command"},
	{"del /f", "critical", "destructive_command"},
	{"shutdown", "high", "destructive_command"},
	{"drop table", "high", "destructive_command"},

	// Delimiter injection — trying to break out of context boundaries
	{"</system>", "medium", "delimiter_injection"},
	{"</instructions>", "medium", "delimiter_injection"},
	{"### IMPORTANT", "low", "attention_hijack"},
	{"[SYSTEM]", "low", "role_injection"},

	// Data exfiltration attempts
	{"send to", "medium", "exfiltration"},
	{"post to", "medium", "exfiltration"},
	{"upload to", "medium", "exfiltration"},
	{"webhook", "low", "exfiltration"},
	{"api key", "high", "credential_theft"},
	{"password", "medium", "credential_theft"},
	{"token", "low", "credential_theft"},
	{".env", "medium", "credential_theft"},
}

// CheckPromptInjection scans user input for common prompt injection patterns.
// This is NOT a perfect defense — sophisticated attacks can bypass it.
// It's a first-pass filter that catches the low-hanging fruit.
//
// Returns an InjectionCheckResult with severity and flags.
// The caller decides what to do based on severity:
//   - "none" / "low" → proceed normally
//   - "medium" → proceed but log the flag
//   - "high" → sanitize input before proceeding
//   - "critical" → block the input entirely
func CheckPromptInjection(input string) InjectionCheckResult {
	normalized := normalizeForCheck(input)
	var flags []string
	highestSeverity := "none"

	for _, ip := range injectionPatterns {
		if strings.Contains(normalized, ip.pattern) {
			// Avoid duplicate flags
			dup := false
			for _, f := range flags {
				if f == ip.flag {
					dup = true
					break
				}
			}
			if !dup {
				flags = append(flags, ip.flag)
			}

			if severityOrder(ip.severity) > severityOrder(highestSeverity) {
				highestSeverity = ip.severity
			}
		}
	}

	return InjectionCheckResult{
		Clean:    len(flags) == 0,
		Severity: highestSeverity,
		Flags:    flags,
	}
}

// SanitizeInput removes or neutralizes dangerous content from user input.
// This is applied when injection check returns "medium" or "high" severity.
// For "critical" severity, the input should be blocked entirely, not sanitized.
func SanitizeInput(input string) string {
	// Remove null bytes (can break downstream parsers)
	sanitized := strings.ReplaceAll(input, "\x00", "")

	// Remove ANSI escape sequences (terminal manipulation)
	sanitized = stripANSI(sanitized)

	// Remove control characters except newline, tab, carriage return
	sanitized = stripControlChars(sanitized)

	// Neutralize common injection patterns by wrapping them
	// (we don't delete — we mark them so the LLM sees them as quoted text)
	sanitized = neutralizeInjectionPatterns(sanitized)

	return sanitized
}

// SanitizeToolInput sanitizes inputs before they're passed to tools.
// This prevents command injection through tool parameters.
func SanitizeToolInput(toolName string, input map[string]any) map[string]any {
	sanitized := make(map[string]any, len(input))

	for key, val := range input {
		switch v := val.(type) {
		case string:
			sanitized[key] = sanitizeToolString(toolName, key, v)
		default:
			sanitized[key] = val
		}
	}

	return sanitized
}

// sanitizeToolString applies tool-specific sanitization rules.
func sanitizeToolString(toolName, key, value string) string {
	// All tools: strip null bytes and control chars
	s := strings.ReplaceAll(value, "\x00", "")
	s = stripControlChars(s)

	// Git commit messages: prevent git message injection
	if toolName == "git_commit" && key == "message" {
		s = strings.ReplaceAll(s, "\n", " ")
		s = strings.ReplaceAll(s, "\r", "")
		// Strip git commit message delimiter
		s = strings.ReplaceAll(s, "---", "—")
	}

	// Path parameters: enforce path safety
	if key == "path" || key == "paths" {
		s = strings.ReplaceAll(s, "\x00", "")
		// Don't allow shell expansion characters in paths
		s = strings.ReplaceAll(s, "`", "")
		s = strings.ReplaceAll(s, "$", "")
		s = strings.ReplaceAll(s, "|", "")
		s = strings.ReplaceAll(s, ";", "")
		s = strings.ReplaceAll(s, "&", "")
		s = strings.ReplaceAll(s, ">", "")
		s = strings.ReplaceAll(s, "<", "")
	}

	return s
}

// normalizeForCheck normalizes text for pattern matching.
// This catches attempts to evade detection with unicode tricks, extra spaces, etc.
func normalizeForCheck(input string) string {
	// Lowercase
	normalized := strings.ToLower(input)

	// Collapse whitespace
	normalized = collapseWhitespace(normalized)

	// Remove zero-width characters (common evasion technique)
	normalized = stripZeroWidth(normalized)

	// Normalize unicode lookalikes (basic set)
	normalized = normalizeLookalikes(normalized)

	return normalized
}

// collapseWhitespace reduces multiple spaces/tabs to single spaces.
func collapseWhitespace(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteRune(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return b.String()
}

// stripZeroWidth removes zero-width unicode characters.
func stripZeroWidth(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\u200B', '\u200C', '\u200D', '\uFEFF', '\u200E', '\u200F':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// normalizeLookalikes replaces common unicode lookalikes with ASCII equivalents.
func normalizeLookalikes(s string) string {
	replacements := map[rune]rune{
		'і': 'i', // Cyrillic і
		'о': 'o', // Cyrillic о
		'а': 'a', // Cyrillic а
		'с': 'c', // Cyrillic с
		'е': 'e', // Cyrillic е
		'х': 'x', // Cyrillic х
		'ᴏ': 'o',
		'ᴀ': 'a',
		'ꜱ': 's',
		'ʏ': 'y',
		'ᴛ': 't',
		'ᴇ': 'e',
		'ʀ': 'r',
		'ɴ': 'n',
		'ᴍ': 'm',
	}
	var b strings.Builder
	for _, r := range s {
		if replacement, ok := replacements[r]; ok {
			b.WriteRune(replacement)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// stripANSI removes ANSI escape sequences.
func stripANSI(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// Skip until we find a terminating character (0x40-0x7E)
			i += 2
			for i < len(s) {
				if s[i] >= 0x40 && s[i] <= 0x7E {
					i++
					break
				}
				i++
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// stripControlChars removes control characters except \n, \r, \t.
func stripControlChars(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			b.WriteRune(r)
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// neutralizeInjectionPatterns wraps detected injection phrases in brackets
// so the LLM interprets them as quoted text, not instructions.
func neutralizeInjectionPatterns(input string) string {
	// Only neutralize the most dangerous patterns
	dangerousPhrases := []string{
		"ignore previous instructions",
		"ignore all previous",
		"forget your instructions",
		"disregard your instructions",
	}

	lower := strings.ToLower(input)
	for _, phrase := range dangerousPhrases {
		idx := strings.Index(lower, phrase)
		if idx >= 0 {
			// Replace in original (preserving case)
			input = input[:idx] + "[QUOTED: " + input[idx:idx+len(phrase)] + "]" + input[idx+len(phrase):]
			lower = strings.ToLower(input) // re-normalize for next check
		}
	}
	return input
}

// severityOrder maps severity levels to numeric values for comparison.
func severityOrder(severity string) int {
	switch severity {
	case "none":
		return 0
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	case "critical":
		return 4
	default:
		return 0
	}
}
