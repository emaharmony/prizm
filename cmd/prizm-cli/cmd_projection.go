// cmd_projection.go implements the `prizm projection` subcommands (V10).
//
// Projections turn event history into queryable current state. Instead of
// scanning thousands of events every time you want to know "how many approvals
// are pending?", you query a projection that has already computed the answer.
//
// Projections are pure functions: same events in the same order always produce
// the same output. They can be rebuilt at any time by replaying events.jsonl.
//
// Built-in projections:
//   - run_status:     Task lifecycle (created → running → completed/failed)
//   - approval_state: Approval counts (pending, approved, denied, expired)
//   - tool_history:   Tool call history with policy decisions
//
// Commands:
//
//	prizm projection list                          — Show available projections
//	prizm projection rebuild --run <id>             — Rebuild projections for one run
//	prizm projection rebuild --all                   — Rebuild projections for all runs
//	prizm projection query <name> --run <id>         — Query a projection snapshot
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/emaharmony/prizm/internal/projection"
	"github.com/emaharmony/prizm/internal/projection/builtin/agentactivity"
	approvalproj "github.com/emaharmony/prizm/internal/projection/builtin/approval"
	"github.com/emaharmony/prizm/internal/projection/builtin/runstatus"
	"github.com/emaharmony/prizm/internal/projection/builtin/toolhistory"
)

// newProjectionRunner creates a runner with all built-in projections registered.
// Adding a new projection? Register it here and it appears in `prizm projection list`.
func newProjectionRunner() *projection.Runner {
	return projection.NewRunner(
		runstatus.New(),
		approvalproj.New(),
		toolhistory.New(),
		agentactivity.New(),
	)
}

// executeProjectionList shows the names of all registered projections.
func executeProjectionList() {
	runner := newProjectionRunner()
	names := runner.List()

	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  Prizm V10 Projections")
	fmt.Println("═══════════════════════════════════════════")
	for _, name := range names {
		fmt.Printf("  • %s\n", name)
	}
	fmt.Println("═══════════════════════════════════════════")
}

// executeProjectionRebuild re-reads events.jsonl and recomputes projection
// snapshots. Use this when:
//   - A new projection was added and you need its initial snapshot
//   - A projection's logic changed and you need to recompute historical data
//   - A projection file was deleted and you need to recreate it
//
// With --run <id>, rebuilds one run. With --all, rebuilds every run.
func executeProjectionRebuild(runID string, all bool, runsDir string) {
	runner := newProjectionRunner()

	if all {
		// Rebuild all runs in the runs directory
		entries, err := os.ReadDir(runsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read runs directory: %v\n", err)
			os.Exit(1)
		}

		count := 0
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if entry.Name() == "policy" {
				continue // skip the policy artifact directory
			}

			eventsFile := filepath.Join(runsDir, entry.Name(), "events.jsonl")
			if _, err := os.Stat(eventsFile); os.IsNotExist(err) {
				continue // skip runs without events
			}

			runPath := filepath.Join(runsDir, entry.Name())
			if err := runner.Run(eventsFile, runPath); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: %s: %v\n", entry.Name(), err)
				continue
			}
			count++
		}

		fmt.Println("═══════════════════════════════════════════")
		fmt.Printf("  Rebuilt projections for %d runs\n", count)
		fmt.Println("═══════════════════════════════════════════")
	} else {
		// Rebuild a single run
		eventsFile := filepath.Join(runsDir, runID, "events.jsonl")
		runPath := filepath.Join(runsDir, runID)

		if _, err := os.Stat(eventsFile); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error: events file not found: %s\n", eventsFile)
			os.Exit(1)
		}

		if err := runner.Run(eventsFile, runPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("═══════════════════════════════════════════")
		fmt.Printf("  Rebuilt projections for run %s\n", runID)
		fmt.Println("═══════════════════════════════════════════")

		// Show which projection files were written
		projDir := filepath.Join(runPath, "projections")
		entries, _ := os.ReadDir(projDir)
		for _, entry := range entries {
			fmt.Printf("  • %s\n", entry.Name())
		}
	}
}

// executeProjectionQuery reads and prints a projection snapshot as JSON.
// The snapshot is stored at runs/<id>/projections/<name>.json.
// If the file doesn't exist, it suggests running `prizm projection rebuild` first.
func executeProjectionQuery(name, runID, runsDir string) {
	projFile := filepath.Join(runsDir, runID, "projections", name+".json")

	data, err := os.ReadFile(projFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: projection %q not found for run %s\n", name, runID)
		fmt.Fprintf(os.Stderr, "  Run 'prizm projection rebuild --run %s' first\n", runID)
		os.Exit(1)
	}

	fmt.Println(string(data))
}
