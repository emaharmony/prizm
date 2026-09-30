package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validCLIWorkflowYAML = `
apiVersion: prism.dev/v1alpha1
kind: MultiAgentWorkflow
metadata:
  name: cli-test-workflow
  version: "1.0.0"
spec:
  entryNode: start
  budgets:
    maxTransitions: 10
  nodes:
    - id: start
      type: role
      role: start
      agentProfile: start-default
      allowedOutcomes:
        - done
    - id: finished
      type: terminal
      terminalCondition: completed
  edges:
    - id: start-to-finished
      from: start
      to: finished
      when:
        outcome: done
`

const invalidCLIWorkflowYAML = `
apiVersion: prism.dev/v1alpha1
kind: MultiAgentWorkflow
metadata:
  name: cli-test-workflow-invalid
  version: "1.0.0"
spec:
  entryNode: ""
  budgets:
    maxTransitions: 10
  nodes:
    - id: start
      type: role
      role: start
      agentProfile: start-default
      allowedOutcomes:
        - done
  edges: []
`

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it — the same os.Pipe redirection technique
// cmd_scan_test.go's TestScanStartNoIssues already establishes for this
// package's CLI output-capture tests.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old

	var buf strings.Builder
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("copy captured stdout: %v", err)
	}
	return buf.String()
}

func writeTempWorkflow(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestExecuteGraphValidateValidFile(t *testing.T) {
	dir := t.TempDir()
	path := writeTempWorkflow(t, dir, "valid.yaml", validCLIWorkflowYAML)

	var err error
	out := captureStdout(t, func() {
		err = executeGraphValidate([]string{path})
	})
	if err != nil {
		t.Fatalf("executeGraphValidate: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("stdout = %q, want it to report the file as ok", out)
	}
}

func TestExecuteGraphValidateInvalidFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := writeTempWorkflow(t, dir, "invalid.yaml", invalidCLIWorkflowYAML)

	var err error
	captureStdout(t, func() {
		err = executeGraphValidate([]string{path})
	})
	if err == nil {
		t.Fatal("expected a non-nil error for a file with error-severity diagnostics")
	}
}

func TestExecuteGraphValidateJSONOutput(t *testing.T) {
	dir := t.TempDir()
	path := writeTempWorkflow(t, dir, "valid.yaml", validCLIWorkflowYAML)

	var err error
	out := captureStdout(t, func() {
		err = executeGraphValidate([]string{"--json", path})
	})
	if err != nil {
		t.Fatalf("executeGraphValidate: %v", err)
	}
	if !strings.Contains(out, filepath.Base(path)) {
		t.Errorf("json output = %q, want it keyed by a file path ending in %q", out, filepath.Base(path))
	}
	if !strings.Contains(out, "{") {
		t.Errorf("json output = %q, does not look like JSON", out)
	}
}

func TestExecuteGraphValidateWalksDirectoryRecursively(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTempWorkflow(t, dir, "top.yaml", validCLIWorkflowYAML)
	writeTempWorkflow(t, nested, "child.yaml", validCLIWorkflowYAML)
	// A non-workflow file must be ignored, not crash the walk.
	writeTempWorkflow(t, dir, "README.md", "not a workflow file")

	var err error
	out := captureStdout(t, func() {
		err = executeGraphValidate([]string{dir})
	})
	if err != nil {
		t.Fatalf("executeGraphValidate: %v", err)
	}
	if !strings.Contains(out, "top.yaml") || !strings.Contains(out, "child.yaml") {
		t.Errorf("expected both top-level and nested files to be validated, got: %q", out)
	}
}

func TestExecuteGraphValidateNoArgsReturnsError(t *testing.T) {
	if err := executeGraphValidate(nil); err == nil {
		t.Fatal("expected an error when no file/directory arguments are given")
	}
}
