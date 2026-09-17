// cmd_approval.go implements the `prizm approval` subcommands (V4).
//
// When an AI agent wants to write a file, Prizm doesn't let it do so directly.
// Instead, it creates an "approval" — a pending request that a human must
// explicitly approve or deny. This file provides the CLI for managing those
// approvals.
//
// The workflow:
//  1. Agent calls write_file_proposal → Prizm creates a pending approval
//  2. Human runs `prizm approval list` → sees pending approvals
//  3. Human runs `prizm approval approve <id> --by ema` → file gets written
//     OR human runs `prizm approval deny <id> --by ema` → file is NOT written
//
// Commands:
//
//	prizm approval list [--run <id>]    — List approvals (optionally filtered by run)
//	prizm approval show <id> --run <id> — Show full approval details
//	prizm approval approve <id>        — Approve a pending mutation (writes file)
//	prizm approval deny <id>           — Deny a pending mutation (no file written)
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/mutation"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/tool"
)

// executeApprovalList shows all approvals, optionally filtered to a single run.
// Without --run, it scans all run directories to find every approval.
func executeApprovalList(runID, runsDir string) {
	store := approval.NewStore(runsDir)

	var approvals []*approval.Approval
	var err error

	if runID != "" {
		// Filter to a specific run
		approvals, err = store.List(runID)
	} else {
		// List all approvals across all runs
		entries, readErr := os.ReadDir(runsDir)
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to read runs directory: %v\n", readErr)
			os.Exit(1)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				runApprovals, listErr := store.List(entry.Name())
				if listErr == nil {
					approvals = append(approvals, runApprovals...)
				}
			}
		}
		err = nil
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(approvals) == 0 {
		fmt.Println("No approvals found.")
		return
	}

	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  Prizm V4 Approvals")
	fmt.Println("═══════════════════════════════════════════")
	for _, a := range approvals {
		fmt.Printf("  %s\n", a.ApprovalID)
		fmt.Printf("    Status:       %s\n", a.Status)
		fmt.Printf("    Type:         %s\n", a.MutationType)
		fmt.Printf("    Target:       %s\n", a.TargetPath)
		fmt.Printf("    Requested by: %s\n", a.RequestedBy)
		fmt.Printf("    Run:          %s\n", a.RunID)
		if a.Status == approval.StatusApproved {
			fmt.Printf("    Approved by:  %s at %v\n", a.ApprovedBy, a.ApprovedAt)
		} else if a.Status == approval.StatusDenied {
			fmt.Printf("    Denied by:    %s\n", a.DeniedBy)
			if a.DenialReason != "" {
				fmt.Printf("    Reason:       %s\n", a.DenialReason)
			}
		}
		fmt.Println()
	}
	fmt.Println("═══════════════════════════════════════════")
}

// executeApprovalShow displays full details about a specific approval,
// including the policy decision, file preview, and who approved/denied it.
// Requires both --run and the approval ID.
func executeApprovalShow(approvalID, runID, runsDir string) {
	if runID == "" {
		fmt.Fprintln(os.Stderr, "Error: --run flag is required for show")
		os.Exit(1)
	}
	if approvalID == "" || approvalID == "show" {
		fmt.Fprintln(os.Stderr, "Error: approval ID required")
		os.Exit(1)
	}

	store := approval.NewStore(runsDir)

	a, err := store.Load(runID, approvalID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Approval: %s\n", a.ApprovalID)
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Status:       %s\n", a.Status)
	fmt.Printf("  Type:         %s\n", a.MutationType)
	fmt.Printf("  Target:       %s\n", a.TargetPath)
	fmt.Printf("  Run:          %s\n", a.RunID)
	fmt.Printf("  Correlation:  %s\n", a.CorrelationID)
	fmt.Printf("  Requested by: %s\n", a.RequestedBy)
	fmt.Printf("  Project:      %s\n", a.Project)
	fmt.Printf("  Policy:       %s (%s)\n", a.Policy.Decision, a.Policy.Reason)
	fmt.Printf("  Created:      %v\n", a.CreatedAt)
	if a.Preview != "" {
		fmt.Printf("  Preview:\n%s\n", a.Preview)
	}
	if a.Status == approval.StatusApproved {
		fmt.Printf("  Approved by:  %s at %v\n", a.ApprovedBy, a.ApprovedAt)
	}
	if a.Status == approval.StatusDenied {
		fmt.Printf("  Denied by:    %s at %v\n", a.DeniedBy, a.DeniedAt)
		if a.DenialReason != "" {
			fmt.Printf("  Reason:       %s\n", a.DenialReason)
		}
	}
	fmt.Println("═══════════════════════════════════════════")
}

// executeApprovalApprove applies a pending mutation — the file gets written
// to disk. The --by flag is required (who approved it) and --run is required
// (which run the approval belongs to).
//
// This is the human-in-the-loop gate: the AI proposed, Prizm validated, and
// now the human decides. No LLM self-approval — ever.
func executeApprovalApprove(approvalID, approvedBy, runID, workspace, runsDir, configPath string) {
	if approvedBy == "" {
		fmt.Fprintln(os.Stderr, "Error: --by flag is required")
		os.Exit(1)
	}
	if runID == "" {
		fmt.Fprintln(os.Stderr, "Error: --run flag is required")
		os.Exit(1)
	}

	store := approval.NewStore(runsDir)

	// MCP tool approvals need a live, connected MCP client — this
	// short-lived CLI process doesn't have one. Fail early with clear
	// guidance instead of a confusing "tool not found" error.
	if pending, err := store.Load(runID, approvalID); err == nil && strings.HasPrefix(pending.ToolName, "mcp_") {
		fmt.Fprintf(os.Stderr, "Error: %q is an MCP tool approval — the CLI has no live MCP connection.\nApprove it from the Discord button instead (requires `prizm serve` running).\n", pending.ToolName)
		os.Exit(1)
	}

	writeRoots := approvalWriteRoots(configPath)
	executor := mutation.NewExecutor(workspace, store, writeRoots...)
	// V62: enables applying MutationToolCall approvals — the human has
	// already explicitly approved this exact command/action, so tier_3 is
	// used for shell (the hard blocklist, always enforced first regardless
	// of tier, is re-checked in validateSafety before execution). Git
	// mutation tools are re-invoked via the registry with the exact
	// original input.
	executor.SetShellTool(approvalShellTool(configPath))
	executor.SetRegistry(approvalRegistry(workspace, configPath))

	// Print events as they happen (CLI doesn't have NATS bus)
	executor.SetEmitter(func(eventType, source string, payload map[string]any) {
		fmt.Printf("  💎 [%s] ", eventType)
		if id, ok := payload["approval_id"].(string); ok {
			fmt.Printf("approval=%s ", id)
		}
		if tp, ok := payload["target_path"].(string); ok {
			fmt.Printf("target=%s", tp)
		}
		fmt.Println()
	})

	result, err := executor.ApplyWithRun(context.Background(), runID, approvalID, approvedBy)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════")
	if result.Success {
		fmt.Println("  ✅ Mutation Applied Successfully")
	} else {
		fmt.Println("  ❌ Mutation Failed")
	}
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Approval ID: %s\n", result.ApprovalID)
	fmt.Printf("  Target Path: %s\n", result.TargetPath)
	if result.Message != "" {
		fmt.Printf("  Message:     %s\n", result.Message)
	}
	fmt.Println("═══════════════════════════════════════════")
}

// executeApprovalDeny rejects a pending mutation — the file is NOT written.
// The approval status changes to "denied" and the proposed content is discarded.
// This is safe: denial never touches the filesystem.
func executeApprovalDeny(approvalID, deniedBy, reason, runID, runsDir string) {
	if deniedBy == "" {
		fmt.Fprintln(os.Stderr, "Error: --by flag is required")
		os.Exit(1)
	}
	if runID == "" {
		fmt.Fprintln(os.Stderr, "Error: --run flag is required")
		os.Exit(1)
	}

	store := approval.NewStore(runsDir)
	executor := mutation.NewExecutor(".", store)

	executor.SetEmitter(func(eventType, source string, payload map[string]any) {
		fmt.Printf("  💎 [%s] ", eventType)
		if id, ok := payload["approval_id"].(string); ok {
			fmt.Printf("approval=%s", id)
		}
		fmt.Println()
	})

	err := executor.DenyApproval(runID, approvalID, deniedBy, reason)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  ❌ Approval Denied")
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Approval ID: %s\n", approvalID)
	fmt.Printf("  Denied By:   %s\n", deniedBy)
	if reason != "" {
		fmt.Printf("  Reason:      %s\n", reason)
	}
	fmt.Println("  (No files were written)")
	fmt.Println("═══════════════════════════════════════════")
}

func approvalWriteRoots(configPath string) []string {
	if configPath == "" {
		configPath = "prizm.yaml"
	}
	cfg, err := orchestrator.LoadConfig(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		fmt.Fprintf(os.Stderr, "Error loading config %q: %v\n", configPath, err)
		os.Exit(1)
	}
	return configuredWriteRoots(cfg)
}

// approvalShellTool builds a ShellTool for re-running an approved shell
// command. Missing/unreadable config falls back to a tier_3 shell tool with
// no extra blocked patterns — the hard blocklist still applies.
func approvalShellTool(configPath string) *tool.ShellTool {
	if configPath == "" {
		configPath = "prizm.yaml"
	}
	cfg, err := orchestrator.LoadConfig(configPath)
	if err != nil {
		return &tool.ShellTool{Policy: tool.ShellPolicy{Tier: "tier_3"}}
	}
	return &tool.ShellTool{
		Policy:         tool.BuildShellPolicyFromConfig("tier_3", cfg.Shell.Allowlists, cfg.Shell.Defaults.BlockedPatterns),
		DefaultTimeout: cfg.Shell.Defaults.TimeoutSeconds,
		MaxOutputBytes: cfg.Shell.Defaults.MaxOutputBytes,
	}
}

// approvalRegistry builds a tool registry for re-invoking approved git
// mutations (git_checkout, git_add, git_commit, git_push, create_pr) from
// the CLI. Mirrors the git tool registration in cmd_serve.go's server
// startup. MCP tools are deliberately not included here — they need a live
// connection this short-lived CLI process doesn't have; those approvals
// must be applied via the Discord button (or `prizm serve` running) instead.
func approvalRegistry(workspace, configPath string) *tool.Registry {
	if configPath == "" {
		configPath = "prizm.yaml"
	}
	writeRoots := approvalWriteRoots(configPath)
	protectedBranch := ""
	if cfg, err := orchestrator.LoadConfig(configPath); err == nil {
		protectedBranch = cfg.ProtectedBranch()
	}
	registry := tool.NewRegistry()
	tool.RegisterBuiltinsV4(registry, workspace, 0, protectedBranch, writeRoots...)
	registry.Register(&tool.GitCreatePRTool{})
	return registry
}
