// Package agent provides tool request parsing for V3 controlled tool execution.
// It parses LLM output into either a final response or a structured tool request.
package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/emaharmony/prizm/internal/tool"
)

// AgentResponseType distinguishes between a final text response and a tool request.
type AgentResponseType string

const (
	// ResponseFinal means the agent is done and returning text output.
	ResponseFinal AgentResponseType = "final"
	// ResponseToolRequest means the agent wants to call a tool before completing.
	ResponseToolRequest AgentResponseType = "tool_request"
)

// AgentResponse represents the parsed output from the LLM. It can be either
// a final text response or a request to execute a tool.
type AgentResponse struct {
	Type      AgentResponseType `json:"type"`
	Content   string            `json:"content,omitempty"`
	ToolName  string            `json:"tool,omitempty"`
	ToolInput map[string]any    `json:"input,omitempty"`
}

// ParseAgentOutput attempts to parse LLM output as JSON. It recognizes two shapes:
//
//	{"type": "final", "content": "..."}     → final response
//	{"type": "tool_request", "tool": "read_file", "input": {"path": "README.md"}}
//	                                      → tool request
//
// If the output is not valid JSON or doesn't match either shape, it is treated
// as a final response with the raw text as content (fallback behavior).
func ParseAgentOutput(raw string) AgentResponse {
	// Trim whitespace and markdown code fences
	text := strings.TrimSpace(raw)

	// Try to extract JSON from markdown code fences if present
	text = extractJSON(text)

	var parsed AgentResponse
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		// Not valid JSON — treat as final text response
		return AgentResponse{
			Type:    ResponseFinal,
			Content: raw,
		}
	}

	switch parsed.Type {
	case "final":
		return AgentResponse{
			Type:    ResponseFinal,
			Content: parsed.Content,
		}
	case "tool_request":
		if parsed.ToolName == "" {
			// Missing tool name — fall back to final response
			return AgentResponse{
				Type:    ResponseFinal,
				Content: raw,
			}
		}
		return parsed
	default:
		// Unknown type — fall back to final response
		return AgentResponse{
			Type:    ResponseFinal,
			Content: raw,
		}
	}
}

// ParseAgentOutputWithFallback works like ParseAgentOutput but also scans
// the raw text for embedded tool_request JSON. This handles the common case
// where the LLM writes natural language followed by a JSON tool request,
// e.g. "Let me read the file. {"type":"tool_request","tool":"read_file","input":{...}}"
//
// If a valid tool_request JSON is found anywhere in the text, it is extracted
// and returned as a tool request. The surrounding text is discarded.
// Only if no tool_request is found does it fall back to a final response.
func ParseAgentOutputWithFallback(raw string) AgentResponse {
	// First try the standard parser
	parsed := ParseAgentOutput(raw)
	if parsed.Type == ResponseToolRequest {
		return parsed
	}

	// Standard parser returned final — but maybe there's an embedded tool_request
	text := strings.TrimSpace(raw)

	// Find all occurrences of "tool_request" in the text
	searchText := text
	for {
		// Find the start of a JSON object that contains "tool_request"
		idx := strings.Index(searchText, `"tool_request"`)
		if idx == -1 {
			break
		}

		// Find the opening { before this point
		braceStart := strings.LastIndex(searchText[:idx], "{")
		if braceStart == -1 {
			// No opening brace — advance past this occurrence
			searchText = searchText[idx+len(`"tool_request"`):]
			continue
		}

		// Find the matching closing } by counting braces
		depth := 0
		braceEnd := -1
		for i := braceStart; i < len(searchText); i++ {
			if searchText[i] == '{' {
				depth++
			} else if searchText[i] == '}' {
				depth--
				if depth == 0 {
					braceEnd = i
					break
				}
			}
		}

		if braceEnd == -1 {
			// No matching close brace — advance
			searchText = searchText[idx+len(`"tool_request"`):]
			continue
		}

		// Extract the JSON substring
		jsonCandidate := searchText[braceStart : braceEnd+1]

		// Try to parse it
		var tr AgentResponse
		if err := json.Unmarshal([]byte(jsonCandidate), &tr); err == nil && tr.Type == ResponseToolRequest && tr.ToolName != "" {
			return tr
		}

		// Advance past this occurrence
		searchText = searchText[braceEnd+1:]
	}

	// No tool_request found — return the final response from standard parser
	return parsed
}

// extractJSON tries to pull JSON content out of markdown code fences.
// If no fences are found, it returns the text unchanged.
func extractJSON(text string) string {
	// Check for ```json ... ``` blocks
	if idx := strings.Index(text, "```json"); idx != -1 {
		start := idx + 7 // skip ```json
		if end := strings.Index(text[start:], "```"); end != -1 {
			return strings.TrimSpace(text[start : start+end])
		}
	}
	// Check for ``` ... ``` blocks
	if idx := strings.Index(text, "```"); idx != -1 {
		start := idx + 3
		// Skip the language tag line if present
		if newlineIdx := strings.Index(text[start:], "\n"); newlineIdx != -1 && newlineIdx < 30 {
			start = start + newlineIdx + 1
		}
		if end := strings.Index(text[start:], "```"); end != -1 {
			return strings.TrimSpace(text[start : start+end])
		}
	}
	return text
}

// BuildToolPromptSuffix appends tool instructions to a prompt, telling the
// model to return JSON with either a "final" or "tool_request" shape.
// V4: Includes approval-gated mutation instructions.
// V28: Includes tool descriptions and parameter schemas so the model knows how to call them.
// V29: Stronger emphasis on actually USING tools instead of just talking about them.
// V29b: Includes workspace root path so the model knows where files are.
func BuildToolPromptSuffix(toolInfos []tool.ToolInfo, workspaceRoot string, allowedPaths ...string) string {
	var sb strings.Builder
	sb.WriteString("\n\n## Tool Instructions\n")
	sb.WriteString(fmt.Sprintf("Your workspace root is: %s\n", workspaceRoot))
	if len(allowedPaths) > 0 {
		sb.WriteString("You also have access to these directories:\n")
		for _, p := range allowedPaths {
			sb.WriteString(fmt.Sprintf("  - %s\n", p))
		}
		sb.WriteString("IMPORTANT: When reading files, searching, or getting project overviews for projects OUTSIDE your workspace, you MUST use the absolute path from the list above.\n")
		sb.WriteString("For example, to read a project at /Users/ema/projects/repos/bassbook, use path=\"/Users/ema/projects/repos/bassbook\" NOT path=\"../bassbook\" or path=\"bassbook\".\n")
		sb.WriteString("Relative paths and .. are blocked. Always use full absolute paths from the allowed directories list.\n")
	}
	sb.WriteString("\n")
	sb.WriteString("You have access to the following tools:\n\n")
	for _, ti := range toolInfos {
		sb.WriteString(fmt.Sprintf("- **%s**: %s", ti.Name, ti.Description))
		if len(ti.Schema.Input) > 0 {
			sb.WriteString(" Parameters: ")
			first := true
			for pname, spec := range ti.Schema.Input {
				if !first {
					sb.WriteString(", ")
				}
				first = false
				req := ""
				if spec.Required {
					req = " (required)"
				}
				sb.WriteString(fmt.Sprintf("%s: %s%s", pname, spec.Type, req))
			}
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n## How to Use Tools\n")
	sb.WriteString("Use tools ONLY when the user's CURRENT request explicitly needs information from files, code, git, or the filesystem.\n")
	sb.WriteString("Do NOT use tools for simple conversational responses like greetings, opinions, chat, or explaining concepts.\n")
	sb.WriteString("Do NOT call tools just because a topic was discussed earlier — only call tools if THIS specific message asks you to read/search/inspect something.\n")
	sb.WriteString("If you can answer from your own knowledge, respond directly with a final JSON.\n")
	sb.WriteString("When you DO need file/code info, you MUST actually call the tool — do NOT say \"Let me search for that\" in a final response.\n")
	sb.WriteString("Instead, emit a tool_request JSON and the system will execute it and give you the results.\n")
	sb.WriteString("If you want to read a file, use read_file. If you want to search, use search_files. If you want an overview, use project_overview.\n")
	sb.WriteString("Never describe what you would do — actually DO it by emitting a tool_request.\n\n")
	sb.WriteString("Respond with JSON in one of these shapes:\n")
	sb.WriteString("- Final response: {\"type\": \"final\", \"content\": \"your text here\"}\n")
	sb.WriteString("- Tool request: {\"type\": \"tool_request\", \"tool\": \"tool_name\", \"input\": {\"key\": \"value\"}}\n")
	sb.WriteString("\nYou may make at most ONE tool request per response. If you need multiple tool calls, respond with the first one and the system will provide results.\n")
	sb.WriteString("\n### V4: File Mutation Approval\n")
	sb.WriteString("File mutations require approval. You may propose a file change using write_file_proposal.\n")
	sb.WriteString("Do not claim that a file was changed unless Prizm returns a successful mutation.applied event.\n")
	sb.WriteString("If your proposal is approved, the file will be written by Prizm and you will receive a mutation.applied confirmation.\n")
	return sb.String()
}
