// cmd_validation.go implements the `prizm validation` subcommands (V5) and
// the approve-with-validation workflow.
//
// Validation runs a command (like `go test ./...`) and checks if it passes.
// This ensures that approved mutations don't break the project. Validation
// profiles define what to run, with safety checks to prevent shell injection.
//
// The approve-with-validation flow (V5) chains three steps:
//  1. Approve the mutation (writes the file)
//  2. Run validation profiles (e.g., go test)
//  3. Run a deterministic review of the changes
//
// Commands:
//
//	prizm validation list              — Show available validation profiles
//	prizm validation run <profile>    — Run a specific validation profile
//	prizm approval approve <id> --validate — Approve + validate + review
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/review"
	"github.com/emaharmony/prizm/internal/run"
	"github.com/emaharmony/prizm/internal/validation"
)

// executeValidationList shows all registered validation profiles with their
// commands, timeouts, and allowed exit codes.
func executeValidationList() {
	registry := validation.NewRegistry()
	profiles := registry.List()

	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  Prizm V5 Validation Profiles")
	fmt.Println("═══════════════════════════════════════════")
	for _, name := range profiles {
		p, err := registry.Resolve(name)
		if err != nil {
			fmt.Printf("  %-20s (error: %v)\n", name, err)
			continue
		}
		fmt.Printf("  %-20s %s\n", p.Name, p.Description)
		fmt.Printf("    Command:  %s %s\n", p.Command, fmtArgs(p.Args))
		fmt.Printf("    Timeout:  %ds\n", p.TimeoutSeconds)
		fmt.Printf("    Allowed Exit Codes: %v\n", p.AllowedExitCodes)
	}
	fmt.Println("═══════════════════════════════════════════")
}

// fmtArgs joins a slice of strings with spaces — used for displaying
// command arguments in validation profile listings.
func fmtArgs(args []string) string {
	result := ""
	for i, a := range args {
		if i > 0 {
			result += " "
		}
		result += a
	}
	return result
}

// executeValidationRun executes a single validation profile and prints the
// result. The profile's command runs in a subprocess with safety checks
// (no shell metacharacters, working directory must be within project root).
func executeValidationRun(profileName, project, runDir, runID string) {
	registry := validation.NewRegistry()

	// Generate a run ID if not provided
	if runID == "" {
		runID = event.NewRunID()
	}

	// Create the artifact directory for this run
	artifactDir := filepath.Join(runDir, runID)
	if err := os.MkdirAll(artifactDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to create run directory: %v\n", err)
		os.Exit(1)
	}

	executor := validation.NewExecutor(registry, project, artifactDir)
	executor.SetEmitter(func(eventType, source string, payload map[string]any) {
		fmt.Printf("  💎 [%s] ", eventType)
		if pn, ok := payload["profile_name"].(string); ok {
			fmt.Printf("profile=%s ", pn)
		}
		fmt.Println()
	})

	ctx := context.Background()
	result, err := executor.Run(ctx, profileName, event.NewCorrelationID())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════")
	if result.Status == "passed" {
		fmt.Println("  ✅ Validation Passed")
	} else {
		fmt.Printf("  ❌ Validation %s\n", result.Status)
	}
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Profile:    %s\n", result.Profile)
	fmt.Printf("  Status:     %s\n", result.Status)
	fmt.Printf("  Exit Code:  %d\n", result.ExitCode)
	fmt.Printf("  Duration:   %dms\n", result.DurationMs)
	if result.StdoutPath != "" {
		fmt.Printf("  Stdout:     %s\n", result.StdoutPath)
	}
	if result.StderrPath != "" {
		fmt.Printf("  Stderr:     %s\n", result.StderrPath)
	}
	if result.Error != "" {
		fmt.Printf("  Error:      %s\n", result.Error)
	}
	fmt.Println("═══════════════════════════════════════════")
}

// executeApprovalApproveWithValidation chains three steps (V5 pipeline):
//  1. Approve the mutation → file is written to disk
//  2. Run validation profiles → check if the project still builds/tests
//  3. Run deterministic review → summarize the change
//
// If validation fails, the mutation is still applied (no auto-rollback) but
// the status shows validation_status: failed. This is intentional —
// rollback is a separate decision that should be made by a human.
func executeApprovalApproveWithValidation(approvalID, approvedBy, runID, workspace, runsDir, configPath string) {
	if approvedBy == "" {
		fmt.Fprintln(os.Stderr, "Error: --by flag is required")
		os.Exit(1)
	}
	if runID == "" {
		fmt.Fprintln(os.Stderr, "Error: --run flag is required")
		os.Exit(1)
	}

	log.SetFlags(log.Ltime | log.Lshortfile)

	runDir := filepath.Join(runsDir, runID)

	// Create a minimal runner for the V5 pipeline
	runner := run.NewRunner(run.RunConfig{
		Project:              "prizm",
		RunDir:               runsDir,
		MutationAllowedPaths: approvalWriteRoots(configPath),
		BusURL:               "nats://localhost:4222",
		ValidationRegistry:   validation.NewRegistry(),
		Reviewer:             review.NewReviewer("lumi-deterministic"),
	})

	result, err := runner.ApproveWithValidation(runDir, approvalID, approvedBy, workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  ✅ Mutation Approved + Validated + Reviewed")
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Run ID:       %s\n", runID)
	fmt.Printf("  Status:       %s\n", result.Status)

	// Show validation results
	if len(result.ValidationResults) > 0 {
		fmt.Println("  ── Validations ──")
		for _, vr := range result.ValidationResults {
			statusIcon := "✅"
			if vr.Status != "passed" {
				statusIcon = "❌"
			}
			fmt.Printf("    %s %s: %s (%dms)\n", statusIcon, vr.Profile, vr.Status, vr.DurationMs)
		}
	}

	// Show review results
	if result.Review != nil {
		fmt.Println("  ── Review ──")
		fmt.Printf("    Reviewer:      %s\n", result.Review.Reviewer)
		fmt.Printf("    Recommendation: %s\n", result.Review.Recommendation)
		fmt.Printf("    Summary:        %s\n", result.Review.Summary)
		if result.ReviewArtifactPath != "" {
			fmt.Printf("    Artifact:       %s\n", result.ReviewArtifactPath)
		}
	}

	fmt.Println("═══════════════════════════════════════════")
	fmt.Println()
}
