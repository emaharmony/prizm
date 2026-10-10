package v2

import "strings"

// parse_export.go exposes the phase response parsers so out-of-package callers
// (the subagent worker) can interpret model turns using the exact same
// tool_request / final JSON contract the gated loop uses — no duplicated,
// drift-prone parsing.

// ParseToolRequestText extracts a tool call from model text. ok is false when
// the text contains no well-formed {"type":"tool_request", ...} block.
func ParseToolRequestText(text string) (tool string, input map[string]any, ok bool) {
	req := parseToolRequest(text)
	if req == nil {
		return "", nil, false
	}
	return req.Tool, req.Input, true
}

// ParseFinalText extracts a final answer from model text. ok is false when the
// text contains no {"type":"final", ...} block (distinguishing "no final" from
// "final with empty content").
func ParseFinalText(text string) (content string, ok bool) {
	if !strings.Contains(text, `{"type":"final"`) && !strings.Contains(text, `{"type": "final"`) {
		return "", false
	}
	content = strings.TrimSpace(parseFinal(text))
	return content, content != ""
}
