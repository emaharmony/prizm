// Package stage provides Prizm's pipeline execution engine (V14a).
//
// ApprovalStage creates approval requests for file mutations.
// It implements the V4 approval-gated mutation pattern: model proposes,
// Prizm validates, human approves.
package stage

import (
	"context"
	"fmt"

	"github.com/emaharmony/prizm/internal/event"
)

// ApprovalStage creates approval requests for pending mutations.
// It does NOT execute mutations — that happens when a human approves
// them through the CLI (see cmd_approval.go).
type ApprovalStage struct {
	// WorkspaceRoot is the root directory for file operations.
	WorkspaceRoot string

	// RunsDir is the directory where run artifacts are stored.
	RunsDir string
}

// Name returns the stage identifier.
func (s *ApprovalStage) Name() string {
	return "approval"
}

// Validate checks that workspace directories are configured.
func (s *ApprovalStage) Validate(rc *RunContext) error {
	if s.WorkspaceRoot == "" {
		return fmt.Errorf("stage %s: workspace_root is required", s.Name())
	}
	return nil
}

// Execute checks if there are pending tool results that require approval
// and creates approval requests for them.
//
// For now, this stage emits an event indicating that approval processing
// was considered. The full approval logic (extracting from runner.go)
// will happen when we wire the stage pipeline into the runner.
func (s *ApprovalStage) Execute(ctx context.Context, rc *RunContext) (*RunContext, *StageResult, error) {
	// Emit approval event using a proper event type string
	// (This will be migrated to event package constants in V4 integration)
	evt := event.NewEvent("prizm.approval.requested", "approval-stage", map[string]any{
		"run_id": rc.RunID,
		"note":   "approval processing considered",
	})
	newRC := rc.WithEvent(evt)

	return newRC, &StageResult{
		StageName: s.Name(),
		Success:   true,
		Data: map[string]any{
			"approvals_created": 0,
			"note":              "approval logic will be fully extracted from runner.go",
		},
	}, nil
}

// Rollback removes approval requests that were created in this stage.
// Since approvals are file-based, this means deleting the approval files.
func (s *ApprovalStage) Rollback(ctx context.Context, rc *RunContext) error {
	// Approval files are in runs/<run_id>/approvals/
	// Rollback would remove them, but since we're creating (not executing)
	// approvals, removing them is safe.
	return nil
}
