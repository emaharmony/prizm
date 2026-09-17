package tool

import (
	"context"
	"testing"
)

// mockDelegator implements Delegator for testing.
type mockDelegator struct {
	taskID string
	err    error
}

func (m *mockDelegator) Delegate(ctx context.Context, delegatedBy, delegatedTo string, taskType, description string, contextData map[string]any) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.taskID, nil
}

func TestDelegateTool_NilDelegator(t *testing.T) {
	tool := NewDelegateTool(nil, "lumi")
	result, err := tool.Execute(context.Background(), map[string]any{
		"agent":       "mango",
		"task_type":   "review",
		"description": "review this code",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure with nil delegator")
	}
}

func TestDelegateTool_Success(t *testing.T) {
	tool := NewDelegateTool(&mockDelegator{taskID: "task_123"}, "lumi")
	result, err := tool.Execute(context.Background(), map[string]any{
		"agent":       "mango",
		"task_type":   "review",
		"description": "review the auth module",
		"files":       "auth.go,auth_test.go",
		"context":     "focus on error handling",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	taskID, _ := result.Output["task_id"].(string)
	if taskID != "task_123" {
		t.Errorf("expected task_id 'task_123', got %q", taskID)
	}
}

func TestDelegateTool_MissingAgent(t *testing.T) {
	tool := NewDelegateTool(&mockDelegator{}, "lumi")
	result, _ := tool.Execute(context.Background(), map[string]any{
		"task_type":   "review",
		"description": "review this",
	})
	if result.Success {
		t.Error("expected failure when agent is missing")
	}
}

func TestDelegateTool_MissingDescription(t *testing.T) {
	tool := NewDelegateTool(&mockDelegator{}, "lumi")
	result, _ := tool.Execute(context.Background(), map[string]any{
		"agent":     "mango",
		"task_type": "review",
	})
	if result.Success {
		t.Error("expected failure when description is missing")
	}
}