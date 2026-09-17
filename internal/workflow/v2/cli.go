package v2

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
)

// CLICommands provides CLI access to the V2 Natural Gates workflow system.
// Usage: prizm workflow v2 <subcommand>

// PrintWorkflowStatus prints the current workflow status in a readable format.
func PrintWorkflowStatus(statePath string) error {
	state, err := LoadCurrentWorkflowState(statePath)
	if err != nil {
		return fmt.Errorf("no active workflow: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()

	fmt.Fprintf(w, "Workflow:\t%s\n", state.WorkflowName)
	fmt.Fprintf(w, "Run ID:\t%s\n", state.RunID)
	fmt.Fprintf(w, "Status:\t%s\n", state.Status)
	fmt.Fprintf(w, "Current Phase:\t%s\n", state.CurrentPhase())
	fmt.Fprintf(w, "Started:\t%s\n", state.StartedAt)
	fmt.Fprintf(w, "Updated:\t%s\n", state.UpdatedAt)
	fmt.Fprintln(w)

	// Phase states
	fmt.Fprintln(w, "Phase\tStatus\tIterations\tGate Score")
	fmt.Fprintln(w, "-----\t------\t----------\t----------")
	for _, phaseCfg := range DefaultConfig().Phases {
		if ps, ok := state.PhaseStates[phaseCfg.Name]; ok {
			score := "—"
			if ps.GateResult != nil {
				score = fmt.Sprintf("%.2f", ps.GateResult.Score)
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", phaseCfg.Name, ps.Status, ps.Iterations, score)
		}
	}
	fmt.Fprintln(w)

	// Assumptions
	if len(state.Assumptions) > 0 {
		fmt.Fprintf(w, "Assumptions:\t%d total\n", len(state.Assumptions))
		open := 0
		for _, a := range state.Assumptions {
			if a.Status == "open" {
				open++
			}
		}
		fmt.Fprintf(w, "Open:\t%d\n", open)
		fmt.Fprintln(w)
	}

	// Confidence
	fmt.Fprintln(w, "Confidence Domain\tScore\tEvidence Count")
	fmt.Fprintln(w, "-----------------\t-----\t--------------")
	for domain, cd := range state.ConfidenceMatrix {
		fmt.Fprintf(w, "%s\t%.2f\t%d\n", domain, cd.Score, len(cd.Evidence))
	}
	fmt.Fprintln(w)

	// Plan
	if state.Plan != nil && len(state.Plan.Tasks) > 0 {
		fmt.Fprintln(w, "Task ID\tAgent\tStatus\tDescription")
		fmt.Fprintln(w, "-------\t-----\t------\t-----------")
		for _, task := range state.Plan.Tasks {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", task.ID, task.Agent, task.Status, task.Description)
		}
		fmt.Fprintln(w)
	}

	// Feedback
	if state.Feedback != nil {
		if state.Feedback.PreExecution != nil {
			fmt.Fprintf(w, "Pre-Execution:\t%s\n", state.Feedback.PreExecution.Status)
		}
		if state.Feedback.PostExecution != nil {
			fmt.Fprintln(w, "Post-Execution Reviews:")
			for reviewer, rs := range state.Feedback.PostExecution.Reviewers {
				fmt.Fprintf(w, "  %s:\t%s\n", reviewer, rs.Status)
			}
		}
	}

	return nil
}

// PrintWorkflowList lists all saved workflow states.
func PrintWorkflowList(stateDir string) error {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return fmt.Errorf("no workflow states found: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()

	fmt.Fprintln(w, "Run ID\tStatus\tPhase\tStarted")
	fmt.Fprintln(w, "------\t------\t-----\t-------")

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := fmt.Sprintf("%s/%s", stateDir, entry.Name())
		state, err := LoadWorkflowState(path)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", state.RunID, state.Status, state.CurrentPhase(), state.StartedAt)
	}

	return nil
}

// ExportWorkflowStateJSON exports the full workflow state as JSON to stdout.
func ExportWorkflowStateJSON(statePath string) error {
	state, err := LoadCurrentWorkflowState(statePath)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(state)
}
