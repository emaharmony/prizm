// cmd_tool.go implements the `prizm tool` subcommands (V3).
//
// Tools are the controlled way an AI agent interacts with the outside world.
// Instead of giving the LLM raw shell access, Prizm provides a registry of
// named tools with JSON schemas and deterministic policies.
//
// Built-in tools:
//   - echo:         Returns its input (for testing)
//   - list_dir:     Lists files in a directory (read-only, always allowed)
//   - read_file:    Reads a file's contents (read-only, always allowed)
//   - write_file_dry_run:  Previews what a file write would look like (allowed)
//   - write_file_proposal: Proposes a file write for human approval (requires_approval)
//   - create_directory_proposal: Proposes directory creation for human approval
//
// Commands:
//
//	prizm tool list                    — Show all tools and their input schemas
//	prizm tool run <name> --input '{}' — Execute a tool directly (for testing)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/emaharmony/prizm/internal/tool"
)

// executeToolList shows all registered tools, their descriptions, and input
// parameter schemas. Useful for discovering what tools are available and what
// inputs they expect.
func executeToolList() {
	registry := tool.NewRegistry()
	tool.RegisterBuiltinsV4(registry, ".", 1024*1024, "")
	tool.RegisterResearchTools(registry, nil, nil, tool.WebSearchConfig{})
	tool.RegisterImageTools(registry, imageToolsConfigFromPrizmConfig(nil, ".", nil))

	names := registry.List()
	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  Prizm V4 Built-in Tools")
	fmt.Println("═══════════════════════════════════════════")
	for _, name := range names {
		t, err := registry.Resolve(name)
		if err != nil {
			fmt.Printf("  %-20s (error: %v)\n", name, err)
			continue
		}
		fmt.Printf("  %-20s %s\n", name, t.Description())
		schema := t.Schema()
		if len(schema.Input) > 0 {
			fmt.Println("    Input:")
			for paramName, spec := range schema.Input {
				req := ""
				if spec.Required {
					req = " (required)"
				}
				fmt.Printf("      %s: %s%s — %s\n", paramName, spec.Type, req, spec.Description)
			}
		}
	}
	fmt.Println("═══════════════════════════════════════════")
}

// executeToolRun executes a single tool directly with the given JSON input.
// This is primarily for testing — it bypasses the agent pipeline and runs
// a tool as a standalone operation. Policy is still enforced.
//
// Example:
//
//	prizm tool run echo --input '{"text": "hello"}'
//	prizm tool run read_file --input '{"path": "README.md"}' --workspace .
func executeToolRun(toolName, inputJSON, project, workspace string, maxFileSize int64) {
	// Parse the JSON input
	var input map[string]any
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid JSON input: %v\n", err)
		os.Exit(1)
	}

	// Set up the tool registry and policy config
	registry := tool.NewRegistry()
	tool.RegisterBuiltinsV4(registry, workspace, maxFileSize, "")
	tool.RegisterResearchTools(registry, nil, nil, tool.WebSearchConfig{})
	tool.RegisterImageTools(registry, imageToolsConfigFromPrizmConfig(nil, workspace, nil))
	policyConfig := tool.PolicyConfig{
		WorkspaceRoot: workspace,
		MaxFileSize:   maxFileSize,
	}
	executor := tool.NewExecutor(registry, &policyConfig)

	fmt.Printf("Running tool %q with input: %s\n", toolName, inputJSON)

	result, err := executor.ExecuteWithPolicy(context.Background(), toolName, "prizm-cli", project, "tool-cli-run", input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error executing tool: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════")
	if result.Success {
		fmt.Println("  ✅ Tool Execution Succeeded")
	} else {
		fmt.Println("  ❌ Tool Execution Failed")
	}
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Tool:    %s\n", toolName)
	fmt.Printf("  Project: %s\n", project)
	if result.Error != "" {
		fmt.Printf("  Error:   %s\n", result.Error)
	}
	if len(result.Output) > 0 {
		fmt.Println("  Output:")
		outputData, _ := json.MarshalIndent(result.Output, "    ", "  ")
		fmt.Printf("    %s\n", string(outputData))
	}
	fmt.Println("═══════════════════════════════════════════")
}
