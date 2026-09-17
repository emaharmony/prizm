// cmd_run.go implements the `prizm run` command (V1→V5).
//
// This is Prizm's main command — it runs the full agent pipeline:
//  1. Create a task event (V1)
//  2. Call the LLM provider to generate a response (V2)
//  3. Execute tools the LLM requests (V3)
//  4. Create approvals for file writes (V4)
//  5. Run validation and review on approved mutations (V5)
//
// Each step emits events to the event log. The run produces artifacts:
//   - events.jsonl: every event emitted during the run
//   - summary.json: human-readable summary of the run
//   - output.md: the LLM's markdown response
//   - prompt.md: the prompt sent to the LLM
//
// The runConfig struct holds all flags from the CLI. The actual execution
// logic lives in internal/run/runner.go — this file just wires CLI flags
// to the runner and prints the result.
package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/run"
)

// runConfig holds all CLI flags for the `prizm run` command.
// It maps 1:1 to the flags defined in main.go's subcommand parser.
type runConfig struct {
	Task             string // The task description (required)
	WorkspaceContext string // Pre-built workspace context (V19 --context flags) injected into the prompt
	Project          string // Project name for event metadata
	Agent            string // Agent name for event metadata
	BusURL           string // NATS bus URL for event publishing
	MemoryEnabled    bool   // Enable the Remembrance context hook
	RequireMemory    bool   // Fail if Remembrance is unavailable
	MemoryURL        string // Remembrance service URL
	RunDir           string // Directory for run artifacts

	// LLM fields
	Provider     provider.Provider // The LLM provider (mock or ollama)
	ProviderName string            // Human-readable provider name
	Model        string            // Model name to use
	Temperature  float64           // LLM sampling temperature
	MaxTokens    int               // Maximum output tokens
	Timeout      time.Duration     // LLM request timeout
	DryRunPrompt bool              // Build prompt but skip LLM call
}

// executeRun creates a runner with the given config and runs the full pipeline.
// On success, it prints a summary with run ID, status, events, and artifact paths.
// On failure, it prints the error and exits with code 1.
func executeRun(cfg runConfig) {
	log.SetFlags(log.Ltime | log.Lshortfile)

	if cfg.Task == "" {
		fmt.Fprintln(os.Stderr, "Error: --task is required")
		os.Exit(1)
	}

	runner := run.NewRunner(run.RunConfig{
		Task:             cfg.Task,
		WorkspaceContext: cfg.WorkspaceContext,
		Project:          cfg.Project,
		Agent:            cfg.Agent,
		BusURL:           cfg.BusURL,
		MemoryEnabled:    cfg.MemoryEnabled,
		RequireMemory:    cfg.RequireMemory,
		MemoryURL:        cfg.MemoryURL,
		RunDir:           cfg.RunDir,
		Provider:         cfg.Provider,
		ProviderName:     cfg.ProviderName,
		Model:            cfg.Model,
		Temperature:      cfg.Temperature,
		MaxTokens:        cfg.MaxTokens,
		Timeout:          cfg.Timeout,
		DryRunPrompt:     cfg.DryRunPrompt,
	})

	result, err := runner.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "═══════════════════════════════════════════")
		fmt.Fprintln(os.Stderr, "  ❌ Prizm Run Failed")
		fmt.Fprintln(os.Stderr, "═══════════════════════════════════════════")
		if result != nil {
			fmt.Fprintf(os.Stderr, "  Run ID:          %s\n", result.RunID)
			fmt.Fprintf(os.Stderr, "  Error:           %s\n", result.Error)
			if result.Provider != "" {
				fmt.Fprintf(os.Stderr, "  Provider:        %s\n", result.Provider)
			}
			if result.Model != "" {
				fmt.Fprintf(os.Stderr, "  Model:           %s\n", result.Model)
			}
			fmt.Fprintf(os.Stderr, "  Events:          %s\n", result.EventsPath)
		} else {
			fmt.Fprintf(os.Stderr, "  Error:           %s\n", err)
		}
		fmt.Fprintln(os.Stderr, "═══════════════════════════════════════════")
		fmt.Fprintln(os.Stderr)
		os.Exit(1)
	}

	// Print success summary
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════")

	if result.DryRun {
		fmt.Println("  ✅ Prizm Run Complete (dry-run)")
	} else {
		fmt.Println("  ✅ Prizm Run Complete")
	}

	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Run ID:          %s\n", result.RunID)
	fmt.Printf("  Status:          %s\n", result.Status)
	if result.Provider != "" {
		fmt.Printf("  Provider:        %s\n", result.Provider)
	}
	if result.Model != "" {
		fmt.Printf("  Model:           %s\n", result.Model)
	}
	fmt.Printf("  Events emitted:  %d\n", result.EventCount)
	fmt.Printf("  Event log:       %s\n", result.EventsPath)
	if result.PromptPath != "" {
		fmt.Printf("  Prompt:          %s\n", result.PromptPath)
	}
	if result.OutputPath != "" {
		fmt.Printf("  Output:          %s\n", result.OutputPath)
	}
	fmt.Printf("  Summary:         %s\n", result.SummaryPath)

	if result.DryRun {
		fmt.Println("  (No LLM call — dry-run mode)")
	}

	// If a tool was executed directly (V3), show its result
	if result.ToolCallResult != nil {
		fmt.Println("  ── Tool Call ──")
		fmt.Printf("  Success:         %v\n", result.ToolCallResult.Success)
		if result.ToolCallResult.Error != "" {
			fmt.Printf("  Error:           %s\n", result.ToolCallResult.Error)
		}
	}

	fmt.Printf("  Duration:        %dms\n", result.DurationMs)
	fmt.Println("═══════════════════════════════════════════")
	fmt.Println()
}
