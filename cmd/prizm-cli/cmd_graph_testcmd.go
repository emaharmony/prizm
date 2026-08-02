package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emaharmony/prism/internal/workflow/multiagent"
)

// graphTestOutcome is one scenario's result, in a shape suitable for both
// human-readable and --json output.
type graphTestOutcome struct {
	File     string   `json:"file"`
	Scenario string   `json:"scenario,omitempty"`
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// executeGraphTest implements `prism graph test <file|dir>...`.
//
// Each positional argument may be a single scenario fixture file
// (.yaml/.yml/.json) or a directory, walked recursively (same convention as
// `graph validate`, via collectFilesByExt). For each scenario file: load it
// via multiagent.LoadScenarioFile, resolve its referenced workflow
// definition relative to the scenario file's own directory, compile that
// workflow ONCE, then run every scenario in the file against the shared
// compiled graph via multiagent.RunScenario. Exits non-zero if any scenario
// in any file fails (or if any file fails to load/compile at all).
func executeGraphTest(args []string) error {
	fs := flag.NewFlagSet("graph test", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Print structured JSON results")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("prism graph test requires at least one scenario file or directory argument")
	}

	files, err := collectFilesByExt(fs.Args(), ".yaml", ".yml", ".json")
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("prism graph test: no .yaml/.yml/.json files found under the given path(s)")
	}

	var outcomes []graphTestOutcome
	anyFailed := false

	for _, file := range files {
		fileOutcomes, failed := runScenarioFile(file)
		outcomes = append(outcomes, fileOutcomes...)
		if failed {
			anyFailed = true
		}
	}

	if *jsonOutput {
		if err := writeJSON(os.Stdout, outcomes); err != nil {
			return err
		}
	} else {
		printGraphTestOutcomes(os.Stdout, outcomes)
	}

	if anyFailed {
		return errors.New("graph test: one or more scenarios failed")
	}
	return nil
}

// runScenarioFile loads one scenario fixture file, compiles the workflow it
// references once, and runs every scenario declared in it. The bool result
// reports whether anything in this file failed (a load/compile error, or
// any scenario assertion mismatch).
func runScenarioFile(file string) ([]graphTestOutcome, bool) {
	sf, err := multiagent.LoadScenarioFile(file)
	if err != nil {
		return []graphTestOutcome{{File: file, Error: err.Error()}}, true
	}

	workflowPath := resolveScenarioWorkflowPath(file, sf.Workflow)
	graph, diags, err := loadAndCompileGraph(workflowPath)
	if err != nil {
		printDiagnostics(os.Stderr, diags)
		return []graphTestOutcome{{
			File:  file,
			Error: fmt.Sprintf("compile workflow %s: %v", workflowPath, err),
		}}, true
	}

	failed := false
	outcomes := make([]graphTestOutcome, 0, len(sf.Scenarios))
	for _, scenario := range sf.Scenarios {
		result, runErr := multiagent.RunScenario(context.Background(), graph, scenario)
		outcome := graphTestOutcome{File: file, Scenario: scenario.Name, Passed: result.Passed, Failures: result.Failures}
		if runErr != nil {
			outcome.Passed = false
			outcome.Error = runErr.Error()
		}
		if !outcome.Passed {
			failed = true
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, failed
}

// resolveScenarioWorkflowPath resolves a ScenarioFile.Workflow reference
// relative to the scenario file's own location, not the process's working
// directory — matching how the plan describes scenario fixtures being
// portable alongside their workflow definitions.
func resolveScenarioWorkflowPath(scenarioFile, workflow string) string {
	if filepath.IsAbs(workflow) {
		return workflow
	}
	return filepath.Join(filepath.Dir(scenarioFile), workflow)
}

func printGraphTestOutcomes(w *os.File, outcomes []graphTestOutcome) {
	for _, o := range outcomes {
		status := "PASS"
		if !o.Passed {
			status = "FAIL"
		}
		name := o.Scenario
		if name == "" {
			name = "(file)"
		}
		fmt.Fprintf(w, "[%s] %s :: %s\n", status, o.File, name)
		if o.Error != "" {
			fmt.Fprintf(w, "    error: %s\n", o.Error)
		}
		for _, f := range o.Failures {
			fmt.Fprintf(w, "    - %s\n", f)
		}
	}
}
