package main

import (
	"strings"
	"testing"
)

func TestExecuteGraphInspectRequiresFileFlag(t *testing.T) {
	err := executeGraphInspect(nil)
	if err == nil {
		t.Fatal("expected an error when --file is omitted")
	}
	if !strings.Contains(err.Error(), "registry") {
		t.Errorf("error = %q, want it to explain registry-backed inspection is unavailable", err.Error())
	}
}

func TestExecuteGraphInspectTextFormat(t *testing.T) {
	dir := t.TempDir()
	path := writeTempWorkflow(t, dir, "valid.yaml", validCLIWorkflowYAML)

	var err error
	out := captureStdout(t, func() {
		err = executeGraphInspect([]string{"--file", path})
	})
	if err != nil {
		t.Fatalf("executeGraphInspect: %v", err)
	}
	for _, want := range []string{"Entry node:", "Fingerprint:", "Nodes:", "Transitions:", "Budgets:", "Unreachable nodes:"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q; got: %s", want, out)
		}
	}
}

func TestExecuteGraphInspectMermaidFormat(t *testing.T) {
	dir := t.TempDir()
	path := writeTempWorkflow(t, dir, "valid.yaml", validCLIWorkflowYAML)

	var err error
	out := captureStdout(t, func() {
		err = executeGraphInspect([]string{"--file", path, "--format", "mermaid"})
	})
	if err != nil {
		t.Fatalf("executeGraphInspect: %v", err)
	}
	if !strings.Contains(out, "```mermaid") || !strings.Contains(out, "flowchart TD") {
		t.Errorf("mermaid output missing expected fences/directive; got: %s", out)
	}
	if !strings.Contains(out, "-->|done|") {
		t.Errorf("mermaid output missing expected edge line; got: %s", out)
	}
}

func TestExecuteGraphInspectJSONFormat(t *testing.T) {
	dir := t.TempDir()
	path := writeTempWorkflow(t, dir, "valid.yaml", validCLIWorkflowYAML)

	var err error
	out := captureStdout(t, func() {
		err = executeGraphInspect([]string{"--file", path, "--format", "json"})
	})
	if err != nil {
		t.Fatalf("executeGraphInspect: %v", err)
	}
	if !strings.Contains(out, "fingerprint") {
		t.Errorf("json output missing fingerprint; got: %s", out)
	}
}

func TestExecuteGraphInspectUnknownFormat(t *testing.T) {
	dir := t.TempDir()
	path := writeTempWorkflow(t, dir, "valid.yaml", validCLIWorkflowYAML)

	var err error
	captureStdout(t, func() {
		err = executeGraphInspect([]string{"--file", path, "--format", "xml"})
	})
	if err == nil {
		t.Fatal("expected an error for an unknown --format value")
	}
}
