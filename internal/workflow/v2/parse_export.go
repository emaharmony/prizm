package v2

import (
	"encoding/json"
	"strings"
)

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

// ParseCanonicalToolRequestText decodes exactly one tool_request JSON object.
// It is intentionally stricter than ParseToolRequestText, which retains the
// workflow/v2 compatibility behavior of finding an envelope inside prose.
func ParseCanonicalToolRequestText(text string) (tool string, input map[string]any, ok bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &fields); err != nil || len(fields) != 3 {
		return "", nil, false
	}
	for _, key := range []string{"type", "tool", "input"} {
		if _, exists := fields[key]; !exists {
			return "", nil, false
		}
	}
	var kind string
	if err := json.Unmarshal(fields["type"], &kind); err != nil || kind != "tool_request" {
		return "", nil, false
	}
	if err := json.Unmarshal(fields["tool"], &tool); err != nil || strings.TrimSpace(tool) == "" {
		return "", nil, false
	}
	if err := json.Unmarshal(fields["input"], &input); err != nil || input == nil {
		return "", nil, false
	}
	if tool == "apply_patch_proposal" && !validApplyPatchProposalInput(input) {
		return "", nil, false
	}
	return tool, input, true
}

func validApplyPatchProposalInput(input map[string]any) bool {
	if len(input) != 2 {
		return false
	}
	patch, patchOK := input["patch"].(string)
	baseSHA, baseOK := input["base_sha"].(string)
	return patchOK && strings.TrimSpace(patch) != "" && baseOK && strings.TrimSpace(baseSHA) != ""
}

// ParseCanonicalFinalText decodes exactly one final JSON object.
func ParseCanonicalFinalText(text string) (content string, ok bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &fields); err != nil || len(fields) != 2 {
		return "", false
	}
	if _, exists := fields["type"]; !exists {
		return "", false
	}
	if _, exists := fields["content"]; !exists {
		return "", false
	}
	var kind string
	if err := json.Unmarshal(fields["type"], &kind); err != nil || kind != "final" {
		return "", false
	}
	if err := json.Unmarshal(fields["content"], &content); err != nil || strings.TrimSpace(content) == "" {
		return "", false
	}
	return content, true
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
