package ollama_test

import (
	"sort"
	"testing"

	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/ollama"
	"github.com/emaharmony/prizm/internal/tool"
)

func TestConvertToolsToOllamaBasic(t *testing.T) {
	toolInfos := []tool.ToolInfo{
		{
			Name:        "read_file",
			Description: "Read a file from the workspace",
			Schema: tool.ToolSchema{
				Input: map[string]tool.ParamSpec{
					"path": {
						Type:        "string",
						Description: "Path to the file",
						Required:    true,
					},
				},
			},
		},
		{
			Name:        "list_dir",
			Description: "List directory contents",
			Schema: tool.ToolSchema{
				Input: map[string]tool.ParamSpec{
					"path": {
						Type:        "string",
						Description: "Path to the directory",
						Required:    false,
					},
				},
			},
		},
	}

	functions := ollama.ConvertToolsToOllama(toolInfos)
	if len(functions) != 2 {
		t.Fatalf("expected 2 functions, got %d", len(functions))
	}

	// Sort for deterministic test order
	sort.Slice(functions, func(i, j int) bool {
		return functions[i].Function.Name < functions[j].Function.Name
	})

	if functions[0].Function.Name != "list_dir" {
		t.Errorf("expected function name 'list_dir', got '%s'", functions[0].Function.Name)
	}
	if functions[1].Function.Name != "read_file" {
		t.Errorf("expected function name 'read_file', got '%s'", functions[1].Function.Name)
	}

	// Check that read_file has "path" as required
	readFileParams, ok := functions[1].Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties to be a map")
	}
	if _, ok := readFileParams["path"]; !ok {
		t.Error("expected 'path' parameter in read_file")
	}

	// Check required field
	required, ok := functions[1].Function.Parameters["required"].([]string)
	if !ok {
		t.Error("expected required to be []string")
	}
	if len(required) != 1 || required[0] != "path" {
		t.Errorf("expected required=['path'], got %v", required)
	}

	// Check list_dir has no required params
	listDirRequired, exists := functions[0].Function.Parameters["required"]
	if exists {
		if req, ok := listDirRequired.([]string); ok && len(req) != 0 {
			t.Errorf("expected no required params for list_dir, got %v", req)
		}
	}
}

func TestConvertToolsToOllamaSecurity(t *testing.T) {
	// Verify that ConvertToolsToOllama only exposes public metadata
	// and does NOT include workspace paths, implementation details, etc.
	toolInfos := []tool.ToolInfo{
		{
			Name:        "read_file",
			Description: "Read a file from the workspace",
			Schema: tool.ToolSchema{
				Input: map[string]tool.ParamSpec{
					"path": {
						Type:        "string",
						Description: "Path to the file",
						Required:    true,
					},
				},
			},
		},
	}

	functions := ollama.ConvertToolsToOllama(toolInfos)
	if len(functions) != 1 {
		t.Fatalf("expected 1 function, got %d", len(functions))
	}

	fn := functions[0]
	if fn.Type != "function" {
		t.Errorf("expected type 'function', got '%s'", fn.Type)
	}

	// Verify only name, description, and parameters are set
	// (no workspace paths, implementation details, etc.)
	params, ok := fn.Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties to be a map")
	}
	pathParam, ok := params["path"].(map[string]any)
	if !ok {
		t.Fatal("expected path parameter to be a map")
	}
	if pathParam["type"] != "string" {
		t.Errorf("expected type 'string', got '%v'", pathParam["type"])
	}
	if pathParam["description"] != "Path to the file" {
		t.Errorf("expected description 'Path to the file', got '%v'", pathParam["description"])
	}

	// Verify no extra keys leaked into the parameter spec
	if len(pathParam) > 2 {
		t.Errorf("expected only type and description in param spec, got %d keys: %v", len(pathParam), pathParam)
	}
}

func TestConvertToolsToOllamaEmpty(t *testing.T) {
	functions := ollama.ConvertToolsToOllama([]tool.ToolInfo{})
	if len(functions) != 0 {
		t.Errorf("expected 0 functions for empty input, got %d", len(functions))
	}
}

func TestConvertToolsToOllamaMultipleParams(t *testing.T) {
	toolInfos := []tool.ToolInfo{
		{
			Name:        "search_files",
			Description: "Search for files matching a pattern",
			Schema: tool.ToolSchema{
				Input: map[string]tool.ParamSpec{
					"pattern": {
						Type:        "string",
						Description: "Search pattern",
						Required:    true,
					},
					"path": {
						Type:        "string",
						Description: "Directory to search in",
						Required:    false,
					},
					"max_results": {
						Type:        "integer",
						Description: "Maximum number of results",
						Required:    false,
					},
				},
			},
		},
	}

	functions := ollama.ConvertToolsToOllama(toolInfos)
	if len(functions) != 1 {
		t.Fatalf("expected 1 function, got %d", len(functions))
	}

	props, ok := functions[0].Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties to be a map")
	}
	if len(props) != 3 {
		t.Errorf("expected 3 parameters, got %d", len(props))
	}

	required, ok := functions[0].Function.Parameters["required"].([]string)
	if !ok {
		t.Error("expected required to be []string")
	}
	if len(required) != 1 || required[0] != "pattern" {
		t.Errorf("expected required=['pattern'], got %v", required)
	}
}

func TestChatTypesResponseMethods(t *testing.T) {
	// Test HasToolCalls and IsFinal on ChatGenerateResponse
	respWithTools := provider.ChatGenerateResponse{
		Content:   "",
		ToolCalls: []provider.ToolCall{{ID: "tc_1", Function: provider.FunctionCall{Name: "read_file"}}},
	}
	if !respWithTools.HasToolCalls() {
		t.Error("expected HasToolCalls() to be true")
	}
	if respWithTools.IsFinal() {
		t.Error("expected IsFinal() to be false when tool calls present")
	}

	respFinal := provider.ChatGenerateResponse{
		Content:   "Hello!",
		ToolCalls: nil,
	}
	if respFinal.HasToolCalls() {
		t.Error("expected HasToolCalls() to be false")
	}
	if !respFinal.IsFinal() {
		t.Error("expected IsFinal() to be true for content-only response")
	}

	respBoth := provider.ChatGenerateResponse{
		Content:   "Let me check.",
		ToolCalls: []provider.ToolCall{{ID: "tc_1", Function: provider.FunctionCall{Name: "search"}}},
	}
	if !respBoth.HasToolCalls() {
		t.Error("expected HasToolCalls() to be true")
	}
	if respBoth.IsFinal() {
		t.Error("expected IsFinal() to be false when tool calls present")
	}
}
