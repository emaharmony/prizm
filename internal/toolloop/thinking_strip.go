package toolloop

import "strings"

// thinkingTagPairs lists the open/close tags some models wrap inline
// reasoning in. Each pair is stripped from chat responses before display.
var thinkingTagPairs = [][2]string{
	{"<think>", "</think>"},
	{"<thinking>", "</thinking>"},
}

// stripThinkingContent removes <think>...</think> and <thinking>...</thinking>
// reasoning blocks (and any unclosed leading think block) that some models
// emit inline in chat responses, so users never see raw reasoning.
//
// This mirrors stripThinkingPrefix in internal/memory/query_planner.go and
// internal/agent/context_agent.go, but is chat-safe: it only removes explicit
// reasoning tags and never mangles surrounding content.
func stripThinkingContent(s string) string {
	for _, pair := range thinkingTagPairs {
		openTag, closeTag := pair[0], pair[1]
		for {
			start := strings.Index(s, openTag)
			if start < 0 {
				break
			}
			end := strings.Index(s, closeTag)
			if end < 0 {
				// Unclosed block: drop everything from the opening tag onward.
				s = s[:start]
				break
			}
			s = s[:start] + s[end+len(closeTag):]
		}
	}
	return strings.TrimSpace(s)
}