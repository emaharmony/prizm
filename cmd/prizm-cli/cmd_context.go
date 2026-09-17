package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	workspacecontext "github.com/emaharmony/prizm/internal/context"
)

const defaultContextTokenBudget = 4000

// Context show command flags
var contextShowCmdFlag = flag.NewFlagSet("context show", flag.ExitOnError)
var contextShowNamed = contextShowCmdFlag.String("context", "", "Named contexts to show (comma-separated: soul,agents,user,heartbeat,memory)")
var contextShowAuto = contextShowCmdFlag.Bool("auto", false, "Auto-discover docs matching a task")
var contextShowTask = contextShowCmdFlag.String("task", "", "Task description for auto-discovery")
var contextShowWorkspace = contextShowCmdFlag.String("workspace-root", "", "Workspace root directory (default: ~/.openclaw/workspace)")
var contextShowBudget = contextShowCmdFlag.Int("budget", 0, "Token budget (0 = default 4000, -1 = no truncation)")
var contextShowFile = contextShowCmdFlag.String("context-file", "", "Additional context file")

func runContextCommand(args []string) error {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Error: context subcommand required (show)")
		fmt.Fprintln(os.Stderr, "Usage: prizm context show [--context soul,agents] [--auto] [--task <desc>] [--workspace-root <path>]")
		os.Exit(1)
	}

	switch args[0] {
	case "show":
		contextShowCmdFlag.Parse(args[1:])
		return showContext()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown context subcommand '%s'\n", args[0])
		os.Exit(1)
	}
	return nil
}

func showContext() error {
	// Resolve workspace root
	workspaceRoot := *contextShowWorkspace
	if workspaceRoot == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("cannot determine home directory: %w", err)
		}
		workspaceRoot = filepath.Join(homeDir, ".openclaw", "workspace")
	}

	// Check workspace exists
	if _, err := os.Stat(workspaceRoot); os.IsNotExist(err) {
		return fmt.Errorf("workspace directory not found: %s", workspaceRoot)
	}

	// Build named contexts
	var namedContexts []string
	if *contextShowNamed != "" {
		namedContexts = strings.Split(*contextShowNamed, ",")
	}

	// Build auto-discovered files
	var autoFiles []string
	if *contextShowAuto && *contextShowTask != "" {
		autoFiles = workspacecontext.DiscoverFiles(workspaceRoot, *contextShowTask)
	}

	// Build explicit files
	var explicitFiles []string
	if *contextShowFile != "" {
		explicitFiles = []string{*contextShowFile}
	}

	tokenBudget, err := resolveContextTokenBudget(*contextShowBudget)
	if err != nil {
		return err
	}

	// Build context
	builder := workspacecontext.NewBuilder(workspaceRoot).
		WithNamedContexts(namedContexts).
		WithAutoFiles(autoFiles).
		WithExplicitFiles(explicitFiles).
		WithTokenBudget(tokenBudget)

	result, err := builder.Build()
	if err != nil {
		return fmt.Errorf("context build: %w", err)
	}

	// Display results
	fmt.Printf("Workspace: %s\n", workspaceRoot)
	fmt.Printf("Files injected: %d\n", len(result.Files))
	fmt.Printf("Total tokens: %d\n", result.TotalTokens)
	fmt.Printf("Truncated: %v\n", result.Truncated)
	fmt.Printf("Content hash: %s...\n\n", result.ContentHash[:16])

	for _, f := range result.Files {
		fmt.Printf("  %-15s  %d tokens  %d bytes  priority:%d  source:%s", f.Name, f.EstimatedTokens, f.SizeBytes, f.Priority, f.Source)
		if f.Truncated {
			fmt.Printf("  truncated:%d tokens omitted", f.TruncatedBy)
		}
		fmt.Println()
	}

	fmt.Println("\n--- Injected Context ---")
	fmt.Println(result.FormattedString)
	fmt.Println("--- End Context ---")

	return nil
}

// buildContextForRun builds context for the run pipeline.
func buildContextForRun(workspaceRoot string, namedContexts []string, autoTask string, explicitFiles []string, tokenBudget int) (*workspacecontext.InjectedContext, error) {
	var autoFiles []string
	if autoTask != "" {
		autoFiles = workspacecontext.DiscoverFiles(workspaceRoot, autoTask)
	}

	resolvedBudget, err := resolveContextTokenBudget(tokenBudget)
	if err != nil {
		return nil, err
	}

	builder := workspacecontext.NewBuilder(workspaceRoot).
		WithNamedContexts(namedContexts).
		WithAutoFiles(autoFiles).
		WithExplicitFiles(explicitFiles).
		WithTokenBudget(resolvedBudget)

	return builder.Build()
}
func resolveContextTokenBudget(budget int) (int, error) {
	switch {
	case budget == -1:
		return 0, nil
	case budget == 0:
		return defaultContextTokenBudget, nil
	case budget > 0:
		return budget, nil
	default:
		return 0, fmt.Errorf("context token budget must be -1 (no truncation), 0 (default %d), or a positive cap", defaultContextTokenBudget)
	}
}
