package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteGraphCompileValidFilePrintsJSON(t *testing.T) {
	dir := t.TempDir()
	path := writeTempWorkflow(t, dir, "valid.yaml", validCLIWorkflowYAML)

	var err error
	out := captureStdout(t, func() {
		err = executeGraphCompile([]string{path})
	})
	if err != nil {
		t.Fatalf("executeGraphCompile: %v", err)
	}
	for _, want := range []string{"fingerprint", "schema_version", "workflow_version", "entry_node_id"} {
		if !strings.Contains(out, want) {
			t.Errorf("compiled JSON output missing %q; got: %s", want, out)
		}
	}
}

func TestExecuteGraphCompileOutWritesFile(t *testing.T) {
	dir := t.TempDir()
	path := writeTempWorkflow(t, dir, "valid.yaml", validCLIWorkflowYAML)
	outPath := filepath.Join(dir, "compiled.json")

	var err error
	captureStdout(t, func() {
		err = executeGraphCompile([]string{"--out", outPath, path})
	})
	if err != nil {
		t.Fatalf("executeGraphCompile: %v", err)
	}
	data, readErr := os.ReadFile(outPath)
	if readErr != nil {
		t.Fatalf("read --out file: %v", readErr)
	}
	if !strings.Contains(string(data), "fingerprint") {
		t.Errorf("--out file content missing fingerprint: %s", data)
	}
}

func TestExecuteGraphCompileInvalidFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := writeTempWorkflow(t, dir, "invalid.yaml", invalidCLIWorkflowYAML)

	var err error
	captureStdout(t, func() {
		err = executeGraphCompile([]string{path})
	})
	if err == nil {
		t.Fatal("expected a non-nil error for an invalid workflow definition")
	}
}

func TestExecuteGraphCompileRequiresExactlyOneFile(t *testing.T) {
	if err := executeGraphCompile(nil); err == nil {
		t.Fatal("expected an error when no file argument is given")
	}
}
