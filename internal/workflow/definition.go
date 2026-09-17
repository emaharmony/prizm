// Package workflow implements the V7 Workflow Runtime for Prizm.
//
// A workflow is a named sequence of Prizm capabilities (tools, gates, etc.)
// that automatically emits lifecycle events at every step. Workflows compose
// existing V1–V6 primitives without rewriting them.
//
// Before V7, Prizm had independent capabilities that you called one at a time.
// After V7, you define a workflow and Prizm runs the full sequence, emitting
// events automatically.
package workflow

import (
	"fmt"
	"gopkg.in/yaml.v3"
)

// Supported step types for V7.
const (
	StepTypeToolExecute  = "tool.execute"
	StepTypeGateEvaluate = "gate.evaluate"
	StepTypeDispatchRun  = "dispatch.run"
	StepTypeWorkflowStop = "workflow.stop"
)

// Workflow is a named sequence of steps that composes Prizm capabilities.
type Workflow struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	Version     int    `yaml:"version" json:"version"`
	Steps       []Step `yaml:"steps" json:"steps"`
}

// Step is a single step in a workflow.
type Step struct {
	ID      string         `yaml:"id" json:"id"`
	Type    string         `yaml:"type" json:"type"`
	Tool    string         `yaml:"tool,omitempty" json:"tool,omitempty"`
	Gate    string         `yaml:"gate,omitempty" json:"gate,omitempty"`
	Adapter string         `yaml:"adapter,omitempty" json:"adapter,omitempty"`
	Action  string         `yaml:"action,omitempty" json:"action,omitempty"`
	Input   map[string]any `yaml:"input,omitempty" json:"input,omitempty"`
	When    string         `yaml:"when,omitempty" json:"when,omitempty"`
}

// Validate checks that a workflow definition is well-formed.
func (w *Workflow) Validate() error {
	if w.Name == "" {
		return fmt.Errorf("workflow: name is required")
	}
	if len(w.Steps) == 0 {
		return fmt.Errorf("workflow: at least one step is required")
	}

	seen := make(map[string]bool)
	for _, step := range w.Steps {
		if step.ID == "" {
			return fmt.Errorf("workflow: step id is required")
		}
		if seen[step.ID] {
			return fmt.Errorf("workflow: duplicate step id %q", step.ID)
		}
		seen[step.ID] = true

		if !isSupportedStepType(step.Type) {
			return fmt.Errorf("workflow: unsupported step type %q", step.Type)
		}
	}

	return nil
}

// isSupportedStepType returns true if the step type is recognized.
func isSupportedStepType(t string) bool {
	for _, supported := range supportedStepTypeList() {
		if t == supported {
			return true
		}
	}
	return false
}

// supportedStepTypeList returns all supported step types.
func supportedStepTypeList() []string {
	return []string{
		StepTypeToolExecute,
		StepTypeGateEvaluate,
		StepTypeDispatchRun,
		StepTypeWorkflowStop,
	}
}

// LoadFromYAML parses a workflow definition from YAML bytes.
func LoadFromYAML(data []byte) (*Workflow, error) {
	var w Workflow
	if err := yaml.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("workflow: failed to parse YAML: %w", err)
	}
	if err := w.Validate(); err != nil {
		return nil, err
	}
	return &w, nil
}
