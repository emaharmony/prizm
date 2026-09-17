// cmd_workflow.go implements the `prizm workflow` subcommands (V7).
//
// Workflows are named sequences of steps that an AI agent can execute.
// Instead of a single LLM call, a workflow defines a pipeline:
//
//	Step 1: tool call (read_file)
//	Step 2: condition (check if output contains error)
//	Step 3: tool call (write_file_proposal) — only if step 2 matched
//
// Workflows are defined in YAML files under examples/workflows/.
// The runner executes them step by step, tracking state and emitting events.
//
// Commands:
//
//	prizm workflow list                    — Show registered workflows
//	prizm workflow show <name>            — Show workflow steps
//	prizm workflow run <name> --input {}  — Execute a workflow
//	prizm workflow status <run_id>        — Check workflow run state
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/emaharmony/prizm/internal/workflow"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

// newWorkflowRegistry loads workflow definitions from examples/workflows/.
// This is best-effort — missing files don't cause a fatal error.
func newWorkflowRegistry() *workflow.Registry {
	reg := workflow.NewRegistry()
	reg.LoadFromDir("examples/workflows") //nolint:errcheck // best-effort loading
	return reg
}

// executeWorkflowList shows all registered workflows with version and description.
func executeWorkflowList() {
	registry := newWorkflowRegistry()
	names := registry.List()

	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  Prizm V7 Workflows")
	fmt.Println("═══════════════════════════════════════════")
	if len(names) == 0 {
		fmt.Println("  (no workflows registered)")
	}
	for _, name := range names {
		w, err := registry.Resolve(name)
		if err != nil {
			fmt.Printf("  %-30s (error: %v)\n", name, err)
			continue
		}
		fmt.Printf("  %-30s v%d — %s\n", name, w.Version, w.Description)
	}
	fmt.Printf("  %-30s v%d — %s\n", multiagent.ReferenceWorkflowID, 1, "Bounded Planner → Developer → Tester → Reviewer workflow")
	fmt.Println("═══════════════════════════════════════════")
}

// executeWorkflowShow displays the steps of a workflow definition.
// Each step shows its type, ID, and optional condition (when clause).
func executeWorkflowShow(workflowName string) {
	if workflowName == multiagent.ReferenceWorkflowID {
		definition := multiagent.DefaultReferenceDefinition()
		fmt.Printf("Workflow: %s\n", definition.ID)
		fmt.Println("Description: Bounded multi-agent software delivery with deterministic correction loops")
		fmt.Printf("Version: %d\nRoles: %d\n", definition.Version, len(definition.Roles))
		for index, role := range definition.Roles {
			fmt.Printf("  %d. %s (agent profile: %s)\n", index+1, role.Role, role.AgentRef)
		}
		fmt.Printf("Safety ceilings: %d transitions, %d tokens, %s\n",
			definition.Budgets.MaxTransitions,
			definition.Budgets.MaxTokens,
			definition.Budgets.MaxDuration)
		return
	}
	registry := newWorkflowRegistry()
	w, err := registry.Resolve(workflowName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Workflow: %s\n", w.Name)
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Description: %s\n", w.Description)
	fmt.Printf("  Version:     %d\n", w.Version)
	fmt.Printf("  Steps:       %d\n", len(w.Steps))
	for i, s := range w.Steps {
		fmt.Printf("    %d. [%s] %s", i+1, s.ID, s.Type)
		if s.When != "" {
			fmt.Printf(" (when: %s)", s.When)
		}
		fmt.Println()
	}
	fmt.Println("═══════════════════════════════════════════")
}

// executeWorkflowRun executes a workflow by name with optional JSON input.
// It sets up tool handlers connecting to Prizm's tool registry and policy,
// then runs the workflow step by step.
//
// The workflow emits events at each step transition. Artifacts (state, events)
// are written to the run directory.
func executeWorkflowRun(workflowName, inputFile, runDir string) {
	registry := newWorkflowRegistry()
	w, err := registry.Resolve(workflowName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Load optional input from a JSON file
	var input map[string]any
	if inputFile != "" {
		data, readErr := os.ReadFile(inputFile)
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to read input file: %v\n", readErr)
			os.Exit(1)
		}
		if jsonErr := json.Unmarshal(data, &input); jsonErr != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid JSON input: %v\n", jsonErr)
			os.Exit(1)
		}
	}
	if input == nil {
		input = map[string]any{}
	}

	// Generate a run ID and create the artifact directory
	runID := event.NewRunID()
	artifactDir := filepath.Join(runDir, runID)
	if mkErr := os.MkdirAll(artifactDir, 0755); mkErr != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to create run directory: %v\n", mkErr)
		os.Exit(1)
	}

	// Build handlers connecting to Prizm primitives
	// The ToolExecute handler runs tools through Prizm's policy-enforced executor
	toolReg := tool.NewRegistry()
	tool.RegisterBuiltinsV4(toolReg, ".", 1024*1024, "")
	policyCfg := tool.PolicyConfig{WorkspaceRoot: ".", MaxFileSize: 1024 * 1024}
	toolExec := tool.NewExecutor(toolReg, &policyCfg)

	handlers := workflow.StepHandlers{
		ToolExecute: func(ctx context.Context, toolName string, toolInput map[string]any) (map[string]any, error) {
			result, execErr := toolExec.ExecuteWithPolicy(ctx, toolName, "prizm-workflow", "prizm", runID, toolInput)
			if execErr != nil {
				return nil, execErr
			}
			if !result.Success {
				return nil, fmt.Errorf("tool %q failed: %s", toolName, result.Error)
			}
			return result.Output, nil
		},
	}

	// Create the workflow runner and wire up event emission
	runner := workflow.NewRunner(handlers)
	runner.SetRunDir(artifactDir)
	runner.SetEmitter(func(eventType, source string, payload map[string]any) {
		fmt.Printf("  💎 [%s]", eventType)
		if wn, ok := payload["workflow_name"]; ok {
			fmt.Printf(" workflow=%v", wn)
		}
		if sid, ok := payload["step_id"]; ok && sid != "" {
			fmt.Printf(" step=%v", sid)
		}
		if st, ok := payload["status"]; ok {
			fmt.Printf(" status=%v", st)
		}
		fmt.Println()
	})

	ctx := context.Background()
	result, runErr := runner.Run(ctx, w, input)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "Error: workflow failed: %v\n", runErr)
		os.Exit(1)
	}

	// Write artifacts (state, events, summary)
	_ = workflow.WriteWorkflowArtifacts(artifactDir, w, result.State, result)

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════")
	switch result.Status {
	case "completed":
		fmt.Println("  ✅ Workflow: COMPLETED")
	case "failed":
		fmt.Println("  ❌ Workflow: FAILED")
	case "paused":
		fmt.Println("  ⏸️  Workflow: PAUSED")
	default:
		fmt.Printf("  ❓ Workflow: %s\n", result.Status)
	}
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Workflow:     %s\n", w.Name)
	fmt.Printf("  Version:      %d\n", w.Version)
	fmt.Printf("  Status:       %s\n", result.Status)
	fmt.Printf("  Steps Run:    %d\n", len(result.State.StepStates))
	fmt.Printf("  Run ID:       %s\n", result.RunID)
	fmt.Printf("  Artifacts:    %s\n", artifactDir)
	fmt.Println("═══════════════════════════════════════════")
	fmt.Println()
}

// executeWorkflowStatus reads a workflow run's saved state and displays
// the status of each step. This lets you check on a paused or failed
// workflow without re-running it.
func executeWorkflowStatus(runID, runDir string) {
	statePath := filepath.Join(runDir, runID, "workflow_state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: workflow state not found for run %s: %v\n", runID, err)
		os.Exit(1)
	}

	var state workflow.WorkflowState
	if jsonErr := json.Unmarshal(data, &state); jsonErr != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid workflow state: %v\n", jsonErr)
		os.Exit(1)
	}

	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Workflow: %s (v%d)\n", state.WorkflowName, state.Version)
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Status:        %s\n", state.Status)
	fmt.Printf("  Run ID:        %s\n", state.RunID)
	if state.CorrelationID != "" {
		fmt.Printf("  Correlation:   %s\n", state.CorrelationID)
	}
	if state.CurrentStep != nil {
		fmt.Printf("  Current Step:  %s\n", *state.CurrentStep)
	}
	fmt.Printf("  Steps:         %d\n", len(state.StepStates))
	for _, s := range state.StepStates {
		icon := "⏳"
		switch s.Status {
		case "completed":
			icon = "✅"
		case "failed":
			icon = "❌"
		case "skipped":
			icon = "⏭️"
		}
		fmt.Printf("    %s [%s] %s", icon, s.ID, s.Type)
		if s.Status != "started" {
			fmt.Printf(" — %s", s.Status)
		}
		fmt.Println()
	}
	fmt.Println("═══════════════════════════════════════════")
}
