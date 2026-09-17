// Package main implements the `prizm runs` subcommand: list past gated-loop runs
// and read their persisted REPORT.md from the terminal.
//
// Usage:
//
//	prizm runs [--dir runs/gated-loop]            list recent runs
//	prizm runs <run-id> [--dir runs/gated-loop]   print a run's REPORT.md
//
// It is a read-only filesystem consumer of the artifacts the gated loop already
// writes (state JSON via SaveWorkflowState, REPORT.md via WriteReportArtifact).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/tracker"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
)

const defaultRunsDir = "runs/gated-loop"

// runEntry summarizes one gated-loop run for listing.
type runEntry struct {
	RunID     string
	Status    string
	StartedAt string
	HasReport bool
	ModTime   time.Time
}

// runStateHeader is the minimal subset of a persisted workflow state we read for
// the listing — decoupled from the v2 package on purpose.
type runStateHeader struct {
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
}

// listRunsFromDir scans a state-persistence directory and returns its runs,
// newest first. Runs are discovered from both the flat workflow-<id>.json state
// files and the <id>/REPORT.md artifacts, so a run shows up even if only one
// exists.
func listRunsFromDir(baseDir string) ([]runEntry, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, err
	}
	byID := map[string]*runEntry{}
	get := func(id string) *runEntry {
		e, ok := byID[id]
		if !ok {
			e = &runEntry{RunID: id}
			byID[id] = e
		}
		return e
	}

	for _, de := range entries {
		name := de.Name()
		switch {
		case !de.IsDir() && strings.HasPrefix(name, "workflow-") && strings.HasSuffix(name, ".json"):
			id := strings.TrimSuffix(strings.TrimPrefix(name, "workflow-"), ".json")
			e := get(id)
			if info, ierr := de.Info(); ierr == nil && info.ModTime().After(e.ModTime) {
				e.ModTime = info.ModTime()
			}
			if data, rerr := os.ReadFile(filepath.Join(baseDir, name)); rerr == nil {
				var hdr runStateHeader
				if json.Unmarshal(data, &hdr) == nil {
					if hdr.Status != "" {
						e.Status = hdr.Status
					}
					if hdr.StartedAt != "" {
						e.StartedAt = hdr.StartedAt
					}
				}
			}
		case de.IsDir():
			reportPath := filepath.Join(baseDir, name, "REPORT.md")
			if info, serr := os.Stat(reportPath); serr == nil {
				e := get(name)
				e.HasReport = true
				if info.ModTime().After(e.ModTime) {
					e.ModTime = info.ModTime()
				}
			}
		}
	}

	out := make([]runEntry, 0, len(byID))
	for _, e := range byID {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

func runStatusGlyph(status string) string {
	switch status {
	case "completed":
		return "✅"
	case "blocked", "failed":
		return "❌"
	case "paused":
		return "⏸️ "
	case "in_progress":
		return "▶️ "
	default:
		return "·"
	}
}

// renderRunsList formats the run listing (pure).
func renderRunsList(entries []runEntry) string {
	var b strings.Builder
	b.WriteString("🗂  gated-loop runs\n")
	b.WriteString(strings.Repeat("─", 60) + "\n")
	if len(entries) == 0 {
		b.WriteString("  (no runs found)\n")
		return b.String()
	}
	for _, e := range entries {
		report := "  "
		if e.HasReport {
			report = "📄"
		}
		status := e.Status
		if status == "" {
			status = "unknown"
		}
		fmt.Fprintf(&b, "  %s %s %-22s %-12s %s\n", runStatusGlyph(e.Status), report, e.RunID, status, e.StartedAt)
	}
	b.WriteString(strings.Repeat("─", 60) + "\n")
	b.WriteString(fmt.Sprintf("%d run(s) · 📄 = report available (prizm runs <id>)\n", len(entries)))
	return b.String()
}

// runEntryJSON is the machine-readable view of a run for `--json`.
type runEntryJSON struct {
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at,omitempty"`
	HasReport bool   `json:"has_report"`
	Modified  string `json:"modified,omitempty"`
}

// runsToJSON marshals run entries to indented JSON (pure, testable).
func runsToJSON(entries []runEntry) ([]byte, error) {
	out := make([]runEntryJSON, 0, len(entries))
	for _, e := range entries {
		j := runEntryJSON{RunID: e.RunID, Status: e.Status, StartedAt: e.StartedAt, HasReport: e.HasReport}
		if !e.ModTime.IsZero() {
			j.Modified = e.ModTime.UTC().Format(time.RFC3339)
		}
		out = append(out, j)
	}
	return json.MarshalIndent(out, "", "  ")
}

// readRunReport returns the REPORT.md content for a run, or an error.
func readRunReport(baseDir, runID string) (string, error) {
	path := filepath.Join(baseDir, runID, "REPORT.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("no report for run %q in %s: %w", runID, baseDir, err)
	}
	return string(data), nil
}

// formatRunStateSummary renders a parsed workflow state as a concise summary —
// used as a fallback for runs that have no REPORT.md yet (in-progress, blocked).
// Pure (no I/O) so it is unit-testable.
func formatRunStateSummary(st *v2.WorkflowState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🧭 run %s — %s [%s]\n", st.RunID, st.WorkflowName, st.Status)
	b.WriteString(strings.Repeat("─", 56) + "\n")
	if st.StartedAt != "" {
		fmt.Fprintf(&b, "started: %s\n", st.StartedAt)
	}
	fmt.Fprintf(&b, "tokens:  %s (prompt %s / completion %s)\n",
		tracker.HumanInt(st.TotalPromptTokens+st.TotalCompletionTokens), tracker.HumanInt(st.TotalPromptTokens), tracker.HumanInt(st.TotalCompletionTokens))

	if v := st.Verification; v != nil {
		status := "passed"
		if !v.Passed {
			status = "FAILED"
		}
		fmt.Fprintf(&b, "verify:  %s %s (exit %d, %d attempt(s))\n", v.Profile, status, v.ExitCode, v.Attempts)
	}

	// Phases, ordered by entry time (then name) for a stable, chronological view.
	type pv struct {
		name string
		ps   *v2.PhaseState
	}
	phases := make([]pv, 0, len(st.PhaseStates))
	for name, ps := range st.PhaseStates {
		phases = append(phases, pv{name, ps})
	}
	sort.Slice(phases, func(i, j int) bool {
		if phases[i].ps.EnteredAt != phases[j].ps.EnteredAt {
			return phases[i].ps.EnteredAt < phases[j].ps.EnteredAt
		}
		return phases[i].name < phases[j].name
	})
	b.WriteString("\nPhases\n")
	for _, p := range phases {
		fmt.Fprintf(&b, "  %-13s %-11s", p.name, p.ps.Status)
		if p.ps.GateResult != nil {
			fmt.Fprintf(&b, " gate %.2f", p.ps.GateResult.Score)
		}
		b.WriteString("\n")
	}

	if len(st.Delegations) > 0 {
		b.WriteString("\nDelegations\n")
		for _, d := range st.Delegations {
			fmt.Fprintf(&b, "  %-8s %s (%s)\n", d.TaskID, d.Agent, d.Status)
		}
	}
	return b.String()
}

// Structured per-run detail for `prizm runs <id> --json`.
type runPhaseJSON struct {
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Iterations int      `json:"iterations"`
	GateScore  *float64 `json:"gate_score,omitempty"`
}

type runVerificationJSON struct {
	Profile  string `json:"profile"`
	Passed   bool   `json:"passed"`
	ExitCode int    `json:"exit_code"`
	Attempts int    `json:"attempts"`
}

type runDelegationJSON struct {
	TaskID string `json:"task_id"`
	Agent  string `json:"agent"`
	Status string `json:"status"`
}

type runRollbackJSON struct {
	Reason string `json:"reason"`
	Error  string `json:"error,omitempty"`
	At     string `json:"at,omitempty"`
}

type runDetailJSON struct {
	RunID            string               `json:"run_id"`
	Workflow         string               `json:"workflow"`
	Status           string               `json:"status"`
	StartedAt        string               `json:"started_at,omitempty"`
	PromptTokens     int                  `json:"prompt_tokens"`
	CompletionTokens int                  `json:"completion_tokens"`
	Verification     *runVerificationJSON `json:"verification,omitempty"`
	Rollback         *runRollbackJSON     `json:"rollback,omitempty"`
	Phases           []runPhaseJSON       `json:"phases"`
	Delegations      []runDelegationJSON  `json:"delegations,omitempty"`
}

// runStateToJSON marshals a workflow state to the structured detail view (pure).
func runStateToJSON(st *v2.WorkflowState) ([]byte, error) {
	d := runDetailJSON{
		RunID:            st.RunID,
		Workflow:         st.WorkflowName,
		Status:           string(st.Status),
		StartedAt:        st.StartedAt,
		PromptTokens:     st.TotalPromptTokens,
		CompletionTokens: st.TotalCompletionTokens,
	}
	if v := st.Verification; v != nil {
		d.Verification = &runVerificationJSON{Profile: v.Profile, Passed: v.Passed, ExitCode: v.ExitCode, Attempts: v.Attempts}
	}
	if r := st.Rollback; r != nil {
		d.Rollback = &runRollbackJSON{Reason: r.Reason, Error: r.Error, At: r.At}
	}
	names := make([]string, 0, len(st.PhaseStates))
	for name := range st.PhaseStates {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := st.PhaseStates[names[i]], st.PhaseStates[names[j]]
		if a.EnteredAt != b.EnteredAt {
			return a.EnteredAt < b.EnteredAt
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		ps := st.PhaseStates[name]
		pj := runPhaseJSON{Name: name, Status: string(ps.Status), Iterations: ps.Iterations}
		if ps.GateResult != nil {
			score := ps.GateResult.Score
			pj.GateScore = &score
		}
		d.Phases = append(d.Phases, pj)
	}
	for _, dl := range st.Delegations {
		d.Delegations = append(d.Delegations, runDelegationJSON{TaskID: dl.TaskID, Agent: dl.Agent, Status: dl.Status})
	}
	return json.MarshalIndent(d, "", "  ")
}

// latestRunID returns the newest run's ID in baseDir (by mtime), or an error when
// there are none. Lets `prizm runs latest` work without knowing the gl-<ts> id.
func latestRunID(baseDir string) (string, error) {
	entries, err := listRunsFromDir(baseDir)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no runs found in %s", baseDir)
	}
	return entries[0].RunID, nil // listRunsFromDir sorts newest-first
}

// showRunDetail prints a run's REPORT.md if present, otherwise a state summary.
func showRunDetail(baseDir, runID string) (string, error) {
	if content, err := readRunReport(baseDir, runID); err == nil {
		return content, nil
	}
	st, err := v2.LoadWorkflowState(filepath.Join(baseDir, "workflow-"+runID+".json"))
	if err != nil {
		return "", fmt.Errorf("no report or state found for run %q in %s", runID, baseDir)
	}
	return formatRunStateSummary(st), nil
}

// executeRuns is the `prizm runs` entry point.
func executeRuns(args []string) {
	fs := flag.NewFlagSet("runs", flag.ExitOnError)
	dir := fs.String("dir", defaultRunsDir, "Gated-loop state/artifact directory")
	asJSON := fs.Bool("json", false, "Emit the run list as JSON")
	// Allow an optional positional run-id before or after flags.
	var runID string
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		runID = args[0]
		rest = args[1:]
	}
	fs.Parse(rest)

	if runID != "" {
		// "latest" resolves to the most recent run so operators don't need the id.
		if runID == "latest" {
			resolved, err := latestRunID(*dir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "❌ %v\n", err)
				os.Exit(1)
			}
			runID = resolved
		}
		if *asJSON {
			st, err := v2.LoadWorkflowState(filepath.Join(*dir, "workflow-"+runID+".json"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "❌ no state for run %q in %s (JSON needs the state file)\n", runID, *dir)
				os.Exit(1)
			}
			data, mErr := runStateToJSON(st)
			if mErr != nil {
				fmt.Fprintf(os.Stderr, "❌ %v\n", mErr)
				os.Exit(1)
			}
			fmt.Println(string(data))
			return
		}
		content, err := showRunDetail(*dir, runID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			os.Exit(1)
		}
		fmt.Print(content)
		return
	}

	entries, err := listRunsFromDir(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ cannot read runs dir %s: %v\n", *dir, err)
		os.Exit(1)
	}
	if *asJSON {
		data, mErr := runsToJSON(entries)
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", mErr)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}
	fmt.Print(renderRunsList(entries))
}
