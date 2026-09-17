package workflow

import (
	"context"
	"fmt"
	"log"

	"github.com/emaharmony/prizm/internal/event"
)

// ToolExecuteFunc executes a tool by name with the given input.
// The caller provides the implementation (e.g., connecting to Prizm's tool.Executor).
type ToolExecuteFunc func(ctx context.Context, toolName string, input map[string]any) (map[string]any, error)

// GateEvaluateFunc evaluates a gate by name with the given input.
// The caller provides the implementation (e.g., connecting to Prizm's gate.Evaluator).
type GateEvaluateFunc func(ctx context.Context, gateName string, input map[string]any) (map[string]any, error)

// DispatchRunFunc runs a dispatch adapter with the given input.
// The caller provides the implementation (e.g., connecting to Prizm's dispatch.Dispatcher).
type DispatchRunFunc func(ctx context.Context, adapterName string, input map[string]any) (map[string]any, error)

// StepHandlers provides function-based handlers for each step type.
// Only set the handlers for step types your workflows use.
// If a handler is nil and a workflow step requires it, the step will fail with a clear error.
type StepHandlers struct {
	ToolExecute  ToolExecuteFunc
	GateEvaluate GateEvaluateFunc
	DispatchRun  DispatchRunFunc
}

// RunResult holds the outcome of a workflow run.
type RunResult struct {
	RunID         string
	CorrelationID string
	WorkflowName  string
	Status        string // completed, failed, paused
	State         WorkflowState
}

// Runner executes a workflow step by step, emitting lifecycle events automatically.
type Runner struct {
	handlers StepHandlers
	emitter  EventEmitter
	runDir   string
}

// EventEmitter is the function signature for emitting events.
type EventEmitter func(eventType, source string, payload map[string]any)

// NewRunner creates a workflow runner with the given step handlers.
func NewRunner(handlers StepHandlers) *Runner {
	return &Runner{
		handlers: handlers,
	}
}

// SetEmitter sets the event emission callback.
func (r *Runner) SetEmitter(emit EventEmitter) {
	r.emitter = emit
}

// SetRunDir sets the directory for persisting run artifacts.
func (r *Runner) SetRunDir(dir string) {
	r.runDir = dir
}

// Run executes a workflow definition with the given input.
// It emits workflow and step lifecycle events automatically.
func (r *Runner) Run(ctx context.Context, w *Workflow, input map[string]any) (*RunResult, error) {
	runID := event.NewRunID()
	correlationID := event.NewCorrelationID()

	// Initialize state
	state := WorkflowState{
		WorkflowName:  w.Name,
		Version:       w.Version,
		Status:        "started",
		RunID:         runID,
		CorrelationID: correlationID,
		StepStates:    make([]StepState, 0, len(w.Steps)),
	}

	// Emit workflow.started
	r.emit(V7EventTypes.WorkflowStarted, map[string]any{
		"workflow_name":    w.Name,
		"workflow_version": w.Version,
		"run_id":           runID,
		"correlation_id":   correlationID,
		"step_count":       len(w.Steps),
	})

	// Track step outputs for condition evaluation
	stepOutputs := make(map[string]map[string]any)

	// Execute steps in order
	for _, step := range w.Steps {
		// Update current step
		stepID := step.ID
		state.CurrentStep = &stepID

		// Evaluate `when` condition before each step
		if step.When != "" {
			conditionMet, err := EvaluateCondition(step.When, stepOutputs)
			if err != nil {
				log.Printf("workflow: condition evaluation error for step %q: %v", step.ID, err)
				conditionMet = false
			}

			if !conditionMet {
				// Emit step.skipped and move on
				stepState := StepState{
					ID:     step.ID,
					Type:   step.Type,
					Status: "skipped",
				}
				state.StepStates = append(state.StepStates, stepState)

				r.emit(V7EventTypes.WorkflowStepSkipped, map[string]any{
					"workflow_name":    w.Name,
					"workflow_version": w.Version,
					"step_id":          step.ID,
					"step_type":        step.Type,
					"run_id":           runID,
					"correlation_id":   correlationID,
					"reason":           fmt.Sprintf("condition not met: %s", step.When),
				})
				continue
			}
		}

		// Emit step.started
		r.emit(V7EventTypes.WorkflowStepStarted, map[string]any{
			"workflow_name":    w.Name,
			"workflow_version": w.Version,
			"step_id":          step.ID,
			"step_type":        step.Type,
			"run_id":           runID,
			"correlation_id":   correlationID,
		})

		stepState := StepState{
			ID:     step.ID,
			Type:   step.Type,
			Status: "started",
		}

		// Execute the step
		output, err := r.executeStep(ctx, step, input, stepOutputs, runID, correlationID)

		if err == ErrWorkflowStop {
			// workflow.stop — halt with reason
			stepState.Status = "completed"
			stepState.Output = map[string]any{"reason": output["reason"]}
			state.StepStates = append(state.StepStates, stepState)

			r.emit(V7EventTypes.WorkflowStepCompleted, map[string]any{
				"workflow_name":    w.Name,
				"workflow_version": w.Version,
				"step_id":          step.ID,
				"step_type":        step.Type,
				"run_id":           runID,
				"correlation_id":   correlationID,
			})

			state.Status = "completed"
			state.CurrentStep = nil
			r.emit(V7EventTypes.WorkflowCompleted, map[string]any{
				"workflow_name":    w.Name,
				"workflow_version": w.Version,
				"run_id":           runID,
				"correlation_id":   correlationID,
				"stopped_at_step":  step.ID,
			})

			return &RunResult{
				RunID:         runID,
				CorrelationID: correlationID,
				WorkflowName:  w.Name,
				Status:        "completed",
				State:         state,
			}, nil
		}

		if err == ErrWorkflowPaused {
			// Pause — emit paused event, set state
			stepState.Status = "completed"
			state.StepStates = append(state.StepStates, stepState)

			state.Status = "paused"
			state.CurrentStep = &stepID

			r.emit(V7EventTypes.WorkflowPaused, map[string]any{
				"workflow_name":    w.Name,
				"workflow_version": w.Version,
				"step_id":          step.ID,
				"step_type":        step.Type,
				"run_id":           runID,
				"correlation_id":   correlationID,
				"reason":           "step requires approval that is not yet granted",
			})

			return &RunResult{
				RunID:         runID,
				CorrelationID: correlationID,
				WorkflowName:  w.Name,
				Status:        "paused",
				State:         state,
			}, nil
		}

		if err != nil {
			// Step failed — workflow fails
			stepState.Status = "failed"
			stepState.Output = map[string]any{"error": err.Error()}
			state.StepStates = append(state.StepStates, stepState)

			r.emit(V7EventTypes.WorkflowStepFailed, map[string]any{
				"workflow_name":    w.Name,
				"workflow_version": w.Version,
				"step_id":          step.ID,
				"step_type":        step.Type,
				"run_id":           runID,
				"correlation_id":   correlationID,
				"error":            err.Error(),
			})

			state.Status = "failed"
			state.CurrentStep = nil

			r.emit(V7EventTypes.WorkflowFailed, map[string]any{
				"workflow_name":    w.Name,
				"workflow_version": w.Version,
				"run_id":           runID,
				"correlation_id":   correlationID,
				"failed_at_step":   step.ID,
				"error":            err.Error(),
			})

			return &RunResult{
				RunID:         runID,
				CorrelationID: correlationID,
				WorkflowName:  w.Name,
				Status:        "failed",
				State:         state,
			}, err
		}

		// Step succeeded
		stepState.Status = "completed"
		stepState.Output = output
		state.StepStates = append(state.StepStates, stepState)

		// Record step output for condition evaluation
		if output != nil {
			stepOutputs[step.ID] = output
		}

		r.emit(V7EventTypes.WorkflowStepCompleted, map[string]any{
			"workflow_name":    w.Name,
			"workflow_version": w.Version,
			"step_id":          step.ID,
			"step_type":        step.Type,
			"run_id":           runID,
			"correlation_id":   correlationID,
		})
	}

	// All steps completed
	state.Status = "completed"
	state.CurrentStep = nil

	r.emit(V7EventTypes.WorkflowCompleted, map[string]any{
		"workflow_name":    w.Name,
		"workflow_version": w.Version,
		"run_id":           runID,
		"correlation_id":   correlationID,
	})

	return &RunResult{
		RunID:         runID,
		CorrelationID: correlationID,
		WorkflowName:  w.Name,
		Status:        "completed",
		State:         state,
	}, nil
}

// emit sends an event through the emitter if one is set.
func (r *Runner) emit(eventType string, payload map[string]any) {
	if r.emitter != nil {
		r.emitter(eventType, "prizm-workflow", payload)
	}
}
