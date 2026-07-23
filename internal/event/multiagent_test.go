package event

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMultiAgentEventTypesAreUniqueAndNamespaced(t *testing.T) {
	eventTypes := multiAgentEventTypes()
	seen := make(map[string]struct{}, len(eventTypes))
	for _, eventType := range eventTypes {
		if !strings.HasPrefix(eventType, "prism.workflow.multi_agent.") {
			t.Errorf("event type %q is outside the multi-agent workflow namespace", eventType)
		}
		if _, exists := seen[eventType]; exists {
			t.Errorf("duplicate event type %q", eventType)
		}
		seen[eventType] = struct{}{}
	}
}

func TestMultiAgentPayloadSerialization(t *testing.T) {
	payloads := []any{
		MultiAgentRunEventPayload{
			RunID:      "run-1",
			WorkflowID: "workflow-1",
			Status:     "running",
		},
		MultiAgentRoleEventPayload{
			RunID:      "run-1",
			WorkflowID: "workflow-1",
			Role:       "developer",
			Status:     "completed",
			Visit:      2,
			Iteration:  3,
			Outcome:    "implementation_ready",
			TokenUsage: &TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
		MultiAgentHandoffEventPayload{
			RunID:           "run-1",
			WorkflowID:      "workflow-1",
			HandoffID:       "handoff-1",
			SourceRole:      "planner",
			DestinationRole: "developer",
			TaskRef:         "task-1",
			Outcome:         "plan_ready",
		},
		MultiAgentTransitionEventPayload{
			RunID:           "run-1",
			WorkflowID:      "workflow-1",
			SourceRole:      "tester",
			DestinationRole: "developer",
			Outcome:         "tests_failed",
			TransitionCount: 4,
		},
		MultiAgentBudgetEventPayload{
			RunID:      "run-1",
			WorkflowID: "workflow-1",
			Budget:     "transitions",
			Used:       9,
			Limit:      10,
		},
	}

	for _, payload := range payloads {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal %T: %v", payload, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal %T: %v", payload, err)
		}
		if decoded["run_id"] != "run-1" {
			t.Errorf("%T run_id = %v, want run-1", payload, decoded["run_id"])
		}
		if decoded["workflow_id"] != "workflow-1" {
			t.Errorf("%T workflow_id = %v, want workflow-1", payload, decoded["workflow_id"])
		}
	}
}

func TestMultiAgentSchemas(t *testing.T) {
	payloads := map[string]map[string]any{
		EventMultiAgentRunCreated: {
			"run_id": "run-1", "workflow_id": "workflow-1", "status": "created",
		},
		EventMultiAgentRunStarted: {
			"run_id": "run-1", "workflow_id": "workflow-1", "status": "running",
		},
		EventMultiAgentRoleEntered: {
			"run_id": "run-1", "workflow_id": "workflow-1", "role": "planner", "status": "running", "visit": 1,
		},
		EventMultiAgentRoleIterationStart: {
			"run_id": "run-1", "workflow_id": "workflow-1", "role": "planner", "status": "running", "iteration": 1,
		},
		EventMultiAgentRoleIterationDone: {
			"run_id": "run-1", "workflow_id": "workflow-1", "role": "planner", "status": "running", "iteration": 1,
		},
		EventMultiAgentRoleCompleted: {
			"run_id": "run-1", "workflow_id": "workflow-1", "role": "planner", "status": "completed", "outcome": "plan_ready",
		},
		EventMultiAgentHandoffCreated: {
			"run_id": "run-1", "workflow_id": "workflow-1", "handoff_id": "handoff-1", "source_role": "planner",
			"destination_role": "developer", "task_ref": "task-1", "outcome": "plan_ready",
		},
		EventMultiAgentTransitionSelected: {
			"run_id": "run-1", "workflow_id": "workflow-1", "source_role": "planner", "outcome": "plan_ready", "transition_count": 1,
		},
		EventMultiAgentLoopTraversal: {
			"run_id": "run-1", "workflow_id": "workflow-1", "source_role": "tester", "outcome": "tests_failed", "transition_count": 3,
		},
		EventMultiAgentBudgetWarning: {
			"run_id": "run-1", "workflow_id": "workflow-1", "budget": "transitions", "used": 9, "limit": 10,
		},
		EventMultiAgentBudgetExhausted: {
			"run_id": "run-1", "workflow_id": "workflow-1", "budget": "transitions", "used": 10, "limit": 10,
		},
		EventMultiAgentRunPaused: {
			"run_id": "run-1", "workflow_id": "workflow-1", "status": "paused",
		},
		EventMultiAgentRunResumed: {
			"run_id": "run-1", "workflow_id": "workflow-1", "status": "running",
		},
		EventMultiAgentRunCompleted: {
			"run_id": "run-1", "workflow_id": "workflow-1", "status": "completed", "terminal_condition": "completed",
		},
		EventMultiAgentRunFailed: {
			"run_id": "run-1", "workflow_id": "workflow-1", "status": "failed", "terminal_condition": "failed", "reason": "runner error",
		},
		EventMultiAgentRunCancelled: {
			"run_id": "run-1", "workflow_id": "workflow-1", "status": "cancelled", "terminal_condition": "cancelled", "reason": "user request",
		},
	}

	for eventType, payload := range payloads {
		t.Run(eventType, func(t *testing.T) {
			if _, exists := Schemas[eventType]; !exists {
				t.Fatalf("schema is not registered")
			}
			if err := Validate(NewEvent(eventType, "test", payload)); err != nil {
				t.Fatalf("valid payload rejected: %v", err)
			}
			schema := Schemas[eventType]
			invalid := make(map[string]any, len(payload))
			for key, value := range payload {
				invalid[key] = value
			}
			delete(invalid, schema.Required[0])
			if err := Validate(NewEvent(eventType, "test", invalid)); err == nil {
				t.Fatalf("payload missing %q was accepted", schema.Required[0])
			}
		})
	}
}

func multiAgentEventTypes() []string {
	return []string{
		EventMultiAgentRunCreated,
		EventMultiAgentRunStarted,
		EventMultiAgentRoleEntered,
		EventMultiAgentRoleIterationStart,
		EventMultiAgentRoleIterationDone,
		EventMultiAgentRoleCompleted,
		EventMultiAgentHandoffCreated,
		EventMultiAgentTransitionSelected,
		EventMultiAgentLoopTraversal,
		EventMultiAgentBudgetWarning,
		EventMultiAgentBudgetExhausted,
		EventMultiAgentRunPaused,
		EventMultiAgentRunResumed,
		EventMultiAgentRunCompleted,
		EventMultiAgentRunFailed,
		EventMultiAgentRunCancelled,
	}
}
