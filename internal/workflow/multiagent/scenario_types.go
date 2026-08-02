// scenario_types.go defines the data shape of a "workflow test" scenario
// fixture: a YAML/JSON document (NOT Go code) that names a workflow
// definition and a list of scripted routing scenarios to run against it,
// entirely without any real model call. This is the data half of PR5's
// scenario-based testing framework; scenario_runner.go is the execution
// half (ScriptedRoleRunner, RunScenario).
//
// Keeping scenarios as data, decodable the same way LoadDefinition decodes a
// WorkflowDefinition, is deliberate: an external developer authoring a
// workflow template gets `prism graph test` for free, with zero access to
// this Go package's test suite or any Go toolchain at all.
package multiagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ScenarioFile is one decoded scenario fixture document: a reference to the
// workflow definition it exercises, plus every scenario to run against it.
type ScenarioFile struct {
	// Workflow is a path to the .yaml/.json workflow definition this file's
	// scenarios are compiled and run against, resolved relative to the
	// scenario file's own location (not the process's working directory) —
	// see resolveScenarioWorkflowPath in cmd/prism-cli/cmd_graph_test.go.
	Workflow  string     `json:"workflow"`
	Scenarios []Scenario `json:"scenarios"`
}

// Scenario is one scripted routing exercise: a task input, a fixed sequence
// of scripted role results (consumed in order by ScriptedRoleRunner, no
// model call involved), and the expectations the resulting run must satisfy.
type Scenario struct {
	Name   string               `json:"name"`
	Input  ScenarioTask         `json:"input,omitempty"`
	Script []ScriptedRoleResult `json:"script"`
	Expect ScenarioExpectation  `json:"expect"`
}

// ScenarioTask supplies the TaskReference a scenario's run is created with.
// Both fields are optional: RunScenario defaults ID to a stable
// scenario-derived value when empty (Supervisor.Run requires a non-empty
// task ID).
type ScenarioTask struct {
	ID          string `json:"id,omitempty"`
	Description string `json:"description,omitempty"`
}

// ScriptedRoleResult is one scripted entry in a Scenario's Script: the
// canned RoleRunResult ScriptedRoleRunner.RunRole returns for the Nth call,
// asserting along the way that the role the supervisor actually requested
// matches Role (see scenario_runner.go).
type ScriptedRoleResult struct {
	Role             string          `json:"role"`
	Outcome          string          `json:"outcome"`
	LocalIterations  int             `json:"localIterations,omitempty"`
	TokenUsage       *ScenarioTokens `json:"tokenUsage,omitempty"`
	ValidationStatus string          `json:"validationStatus,omitempty"`
	ApprovalStatus   string          `json:"approvalStatus,omitempty"`
	// FailWith, when non-empty, makes RunRole return an error instead of a
	// result — simulating a role execution failure. Outcome/TokenUsage/etc.
	// are ignored when this is set.
	FailWith string `json:"failWith,omitempty"`
	// Cancel, when true, cancels the run's context before RunRole returns —
	// simulating an external cancellation. Outcome/TokenUsage/etc. are
	// ignored when this is set.
	Cancel bool `json:"cancel,omitempty"`
}

// ScenarioTokens is the scripted token usage for one ScriptedRoleResult.
type ScenarioTokens struct {
	Prompt     int `json:"prompt,omitempty"`
	Completion int `json:"completion,omitempty"`
}

// ScenarioExpectation is the full assertion surface RunScenario checks the
// resulting run against. Every field is optional; an unset field is simply
// not checked (a scenario need not exercise every assertion dimension).
//
// Field-to-signal mapping (see scenario_runner.go's diffScenarioExpectation
// for the exact comparisons):
//
//   - FinalStatus is compared against the resulting RunState.Status
//     (case-sensitive, e.g. "completed", "failed", "cancelled",
//     "budget_exhausted").
//   - TerminalNode is compared against the resolved terminal node's stable
//     compiled-graph ID (e.g. "node:terminal:completed" — the same ID
//     format CompiledGraph.TerminalNodeIDs()/EntryNodeID() and `graph
//     inspect --format json` use), not the raw terminal condition string.
//   - Transitions is compared against the sequence of stable edge IDs (e.g.
//     "edge:tester:tests_failed") the run actually traversed, in order,
//     reconstructed from emitted transition.selected events.
//   - NodeVisits is keyed by role name (e.g. "developer") against
//     RunState.RoleStates[role].Visits.
//   - EdgeTraversals is keyed by stable edge ID against per-edge traversal
//     counts derived the same way graph_projection.go's BuildRunGraph does
//     (scanTransitionEvents), not just the two legacy correction-loop
//     counters.
//   - BudgetExhausted and LoopExhausted are both compared against
//     (*BudgetExceededError).Budget on the error Supervisor.Run returned —
//     BudgetExhausted for a non-loop budget name (e.g. "max_transitions",
//     "max_tokens") and LoopExhausted for a loop budget name (e.g.
//     "max_tester_to_developer_loops", or "max_<edge-id>_loops" for a
//     developer-defined loop) — the two fields read the same underlying
//     error field and exist separately only to let a scenario author name
//     their intent.
//   - InvalidOutcome is satisfied when Supervisor.Run returned an
//     *InvalidTransitionError.
//   - ApprovalWaiting and ValidationFailed are best-effort: Supervisor.Run
//     (the only execution path RunScenario drives) executes a role
//     synchronously start-to-finish and never itself pauses a role in
//     RoleStatusWaiting for approval — that is a DurableRuntime-only
//     concept, out of scope for this PR — so ApprovalWaiting can only be
//     satisfied by a role state DurableRuntime itself would have left
//     waiting, which RunScenario's in-memory Supervisor path cannot
//     produce today. ValidationFailed checks whether any role's
//     RoleState.ValidationStatus == "failed" (settable via a
//     ScriptedRoleResult's ValidationStatus field).
type ScenarioExpectation struct {
	FinalStatus      string         `json:"finalStatus"`
	TerminalNode     string         `json:"terminalNode,omitempty"`
	Transitions      []string       `json:"transitions,omitempty"`
	NodeVisits       map[string]int `json:"nodeVisits,omitempty"`
	EdgeTraversals   map[string]int `json:"edgeTraversals,omitempty"`
	BudgetExhausted  string         `json:"budgetExhausted,omitempty"`
	InvalidOutcome   bool           `json:"invalidOutcome,omitempty"`
	ApprovalWaiting  bool           `json:"approvalWaiting,omitempty"`
	ValidationFailed bool           `json:"validationFailed,omitempty"`
	LoopExhausted    string         `json:"loopExhausted,omitempty"`
}

// LoadScenarioFile reads and decodes a scenario fixture file (YAML or JSON,
// format detected by extension exactly like LoadDefinition). It uses the
// same YAML->generic-map->JSON bridge technique loader.go's loadYAML
// establishes (normalizeYAMLValue, reused directly — same package, not
// reimplemented), and the same DisallowUnknownFields strict-decode
// discipline, so a typo in a scenario file is a clear decode error rather
// than a silently-ignored field.
//
// Unlike LoadDefinition, this returns a plain error (not a Diagnostics
// list): scenario files are a developer-testing tool, not a document
// developers hand-author against a schema editor expecting line/column
// diagnostics, so the extra position-aware diagnostic machinery is not
// justified here.
func LoadScenarioFile(path string) (ScenarioFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ScenarioFile{}, err
	}

	format := "json"
	if ext := strings.ToLower(filepath.Ext(path)); ext == ".yaml" || ext == ".yml" {
		format = "yaml"
	}
	return decodeScenarioFileBytes(data, format, path)
}

func decodeScenarioFileBytes(data []byte, format, filename string) (ScenarioFile, error) {
	switch format {
	case "yaml":
		var raw any
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return ScenarioFile{}, fmt.Errorf("multiagent: parse scenario file %s: %w", filename, err)
		}
		normalized, ok := normalizeYAMLValue(raw).(map[string]any)
		if !ok {
			return ScenarioFile{}, fmt.Errorf("multiagent: scenario file %s: document root must be a mapping/object with \"workflow\" and \"scenarios\"", filename)
		}
		jsonData, err := json.Marshal(normalized)
		if err != nil {
			return ScenarioFile{}, fmt.Errorf("multiagent: scenario file %s: convert yaml to json: %w", filename, err)
		}
		return decodeStrictScenarioJSON(jsonData, filename)
	case "json":
		return decodeStrictScenarioJSON(data, filename)
	default:
		return ScenarioFile{}, fmt.Errorf("multiagent: unsupported scenario file format %q (want \"yaml\" or \"json\")", format)
	}
}

func decodeStrictScenarioJSON(data []byte, filename string) (ScenarioFile, error) {
	var sf ScenarioFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sf); err != nil {
		return ScenarioFile{}, fmt.Errorf("multiagent: decode scenario file %s: %w", filename, err)
	}
	return sf, nil
}
