package multiagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// scenarioFixtureYAML mirrors the plan's illustrative scenario shape: a
// workflow reference plus a small set of scripted scenarios exercising a
// happy path and a budget/loop-exhaustion path.
const scenarioFixtureYAML = `
workflow: ../schema/valid/software-delivery.yaml
scenarios:
  - name: happy-path
    input:
      id: task-1
      description: ship the feature
    script:
      - role: planner
        outcome: plan_ready
      - role: developer
        outcome: implementation_ready
      - role: tester
        outcome: tests_passed
      - role: reviewer
        outcome: review_approved
    expect:
      finalStatus: completed
      terminalNode: node:terminal:completed
      nodeVisits:
        planner: 1
        developer: 1
        tester: 1
        reviewer: 1
  - name: tester-loop-exhaustion
    script:
      - role: planner
        outcome: plan_ready
      - role: developer
        outcome: implementation_ready
      - role: tester
        outcome: tests_failed
      - role: developer
        outcome: implementation_ready
      - role: tester
        outcome: tests_failed
      - role: developer
        outcome: implementation_ready
      - role: tester
        outcome: tests_failed
      - role: developer
        outcome: implementation_ready
      - role: tester
        outcome: tests_failed
    expect:
      finalStatus: budget_exhausted
      loopExhausted: max_tester_to_developer_loops
`

func TestLoadScenarioFileYAMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "software-delivery.scenario.yaml")
	if err := os.WriteFile(path, []byte(scenarioFixtureYAML), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	sf, err := LoadScenarioFile(path)
	if err != nil {
		t.Fatalf("LoadScenarioFile: %v", err)
	}

	if sf.Workflow != "../schema/valid/software-delivery.yaml" {
		t.Errorf("Workflow = %q", sf.Workflow)
	}
	if len(sf.Scenarios) != 2 {
		t.Fatalf("len(Scenarios) = %d, want 2", len(sf.Scenarios))
	}

	happy := sf.Scenarios[0]
	if happy.Name != "happy-path" {
		t.Errorf("Scenarios[0].Name = %q", happy.Name)
	}
	if happy.Input.ID != "task-1" || happy.Input.Description != "ship the feature" {
		t.Errorf("Scenarios[0].Input = %+v", happy.Input)
	}
	if len(happy.Script) != 4 {
		t.Fatalf("len(Scenarios[0].Script) = %d, want 4", len(happy.Script))
	}
	if happy.Script[0].Role != "planner" || happy.Script[0].Outcome != "plan_ready" {
		t.Errorf("Scenarios[0].Script[0] = %+v", happy.Script[0])
	}
	if happy.Expect.FinalStatus != "completed" {
		t.Errorf("Scenarios[0].Expect.FinalStatus = %q", happy.Expect.FinalStatus)
	}
	if happy.Expect.TerminalNode != "node:terminal:completed" {
		t.Errorf("Scenarios[0].Expect.TerminalNode = %q", happy.Expect.TerminalNode)
	}
	if happy.Expect.NodeVisits["developer"] != 1 {
		t.Errorf("Scenarios[0].Expect.NodeVisits[developer] = %d, want 1", happy.Expect.NodeVisits["developer"])
	}

	loop := sf.Scenarios[1]
	if loop.Expect.FinalStatus != "budget_exhausted" {
		t.Errorf("Scenarios[1].Expect.FinalStatus = %q", loop.Expect.FinalStatus)
	}
	if loop.Expect.LoopExhausted == "" {
		t.Error("Scenarios[1].Expect.LoopExhausted is empty, want it set")
	}
}

// TestLoadScenarioFileJSONEquivalent proves the JSON path decodes to the
// exact same ScenarioFile value as the YAML path, for the same content —
// the same bridging guarantee loader.go's LoadDefinition already provides
// for WorkflowDefinition.
func TestLoadScenarioFileJSONEquivalent(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "fixture.yaml")
	if err := os.WriteFile(yamlPath, []byte(scenarioFixtureYAML), 0o644); err != nil {
		t.Fatalf("write yaml fixture: %v", err)
	}
	fromYAML, err := LoadScenarioFile(yamlPath)
	if err != nil {
		t.Fatalf("LoadScenarioFile(yaml): %v", err)
	}

	jsonBytes, err := json.Marshal(fromYAML)
	if err != nil {
		t.Fatalf("marshal to json: %v", err)
	}
	jsonPath := filepath.Join(dir, "fixture.json")
	if err := os.WriteFile(jsonPath, jsonBytes, 0o644); err != nil {
		t.Fatalf("write json fixture: %v", err)
	}

	fromJSON, err := LoadScenarioFile(jsonPath)
	if err != nil {
		t.Fatalf("LoadScenarioFile(json): %v", err)
	}

	fromYAMLJSON, err := json.Marshal(fromYAML)
	if err != nil {
		t.Fatalf("marshal fromYAML: %v", err)
	}
	fromJSONJSON, err := json.Marshal(fromJSON)
	if err != nil {
		t.Fatalf("marshal fromJSON: %v", err)
	}
	if string(fromYAMLJSON) != string(fromJSONJSON) {
		t.Errorf("YAML- and JSON-decoded ScenarioFile values differ:\nyaml-derived: %s\njson-derived: %s", fromYAMLJSON, fromJSONJSON)
	}
}

func TestLoadScenarioFileUnknownFieldRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	content := "workflow: some.yaml\nscenarios: []\nbogusField: true\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if _, err := LoadScenarioFile(path); err == nil {
		t.Fatal("expected an error for an unknown top-level field, got nil")
	}
}

func TestLoadScenarioFileMissingReturnsError(t *testing.T) {
	if _, err := LoadScenarioFile(filepath.Join(t.TempDir(), "does-not-exist.yaml")); err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}
