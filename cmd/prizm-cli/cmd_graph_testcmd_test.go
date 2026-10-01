package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const passingScenarioYAML = `
workflow: ./workflow.yaml
scenarios:
  - name: happy-path
    script:
      - role: start
        outcome: done
    expect:
      finalStatus: completed
      terminalNode: "node:terminal:completed"
`

const failingScenarioYAML = `
workflow: ./workflow.yaml
scenarios:
  - name: wrong-expectation
    script:
      - role: start
        outcome: done
    expect:
      finalStatus: failed
`

func TestExecuteGraphTestPassingScenario(t *testing.T) {
	dir := t.TempDir()
	writeTempWorkflow(t, dir, "workflow.yaml", validCLIWorkflowYAML)
	scenarioPath := writeTempWorkflow(t, dir, "scenario.yaml", passingScenarioYAML)

	var err error
	out := captureStdout(t, func() {
		err = executeGraphTest([]string{scenarioPath})
	})
	if err != nil {
		t.Fatalf("executeGraphTest: %v (output: %s)", err, out)
	}
	if !strings.Contains(out, "[PASS]") {
		t.Errorf("stdout = %q, want it to report PASS", out)
	}
}

func TestExecuteGraphTestFailingScenarioReturnsError(t *testing.T) {
	dir := t.TempDir()
	writeTempWorkflow(t, dir, "workflow.yaml", validCLIWorkflowYAML)
	scenarioPath := writeTempWorkflow(t, dir, "scenario.yaml", failingScenarioYAML)

	var err error
	out := captureStdout(t, func() {
		err = executeGraphTest([]string{scenarioPath})
	})
	if err == nil {
		t.Fatal("expected a non-nil error when a scenario fails")
	}
	if !strings.Contains(out, "[FAIL]") {
		t.Errorf("stdout = %q, want it to report FAIL", out)
	}
}

func TestExecuteGraphTestJSONOutput(t *testing.T) {
	dir := t.TempDir()
	writeTempWorkflow(t, dir, "workflow.yaml", validCLIWorkflowYAML)
	scenarioPath := writeTempWorkflow(t, dir, "scenario.yaml", passingScenarioYAML)

	var err error
	out := captureStdout(t, func() {
		err = executeGraphTest([]string{"--json", scenarioPath})
	})
	if err != nil {
		t.Fatalf("executeGraphTest: %v", err)
	}
	if !strings.Contains(out, `"passed": true`) {
		t.Errorf("json output = %q, want a passed:true entry", out)
	}
}

// scenarioReferencingParentWorkflowYAML resolves its workflow reference one
// directory up, so a recursive `graph test <dir>` scan of the scenario's own
// directory never also picks up the referenced workflow definition file as
// if it were itself a scenario file.
const scenarioReferencingParentWorkflowYAML = `
workflow: ../workflow.yaml
scenarios:
  - name: happy-path
    script:
      - role: start
        outcome: done
    expect:
      finalStatus: completed
`

func TestExecuteGraphTestWalksDirectoryRecursively(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	// The referenced workflow definition lives OUTSIDE the scanned
	// directory tree: collectFilesByExt's recursive walk picks up every
	// .yaml file under the scanned root, and a workflow definition file is
	// not itself a valid scenario file.
	writeTempWorkflow(t, dir, "workflow.yaml", validCLIWorkflowYAML)
	writeTempWorkflow(t, nested, "scenario.yaml", scenarioReferencingParentWorkflowYAML)

	var err error
	out := captureStdout(t, func() {
		err = executeGraphTest([]string{nested})
	})
	if err != nil {
		t.Fatalf("executeGraphTest: %v (output: %s)", err, out)
	}
	if !strings.Contains(out, "[PASS]") {
		t.Errorf("stdout = %q, want a PASS from the nested scenario file", out)
	}
}

func TestExecuteGraphTestNoArgsReturnsError(t *testing.T) {
	if err := executeGraphTest(nil); err == nil {
		t.Fatal("expected an error when no file/directory arguments are given")
	}
}
