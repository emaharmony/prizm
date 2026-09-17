// Package stage provides Prizm's pipeline execution engine (V14a).
//
// ConnectionStage validates configuration, creates the run directory,
// and establishes the NATS connection. It's the first stage in every
// pipeline run — if config is bad or the directory can't be created,
// nothing else runs.
package stage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emaharmony/prizm/internal/event"
)

// ConnectionStage validates configuration and creates the run directory.
// It does NOT connect to NATS — that responsibility belongs to the
// PersistenceStage, which publishes events after all other stages complete.
//
// This separation is intentional: connecting to NATS at the start of a run
// and then disconnecting at the end would waste connections for short runs.
// Instead, we connect only when we need to publish events.
type ConnectionStage struct {
	// RunDir is the base directory for run artifacts.
	// Each run creates a subdirectory: runs/<run_id>/
	RunDir string

	// BusURL is the NATS server URL. If empty, NATS is disabled.
	BusURL string
}

// Name returns the stage identifier.
func (s *ConnectionStage) Name() string {
	return "connection"
}

// Validate checks that required configuration is present.
func (s *ConnectionStage) Validate(rc *RunContext) error {
	if rc.RunID == "" {
		return fmt.Errorf("stage %s: run_id is required", s.Name())
	}
	if rc.Task == "" {
		return fmt.Errorf("stage %s: task is required", s.Name())
	}
	return nil
}

// Execute creates the run directory and emits a task.created event.
func (s *ConnectionStage) Execute(ctx context.Context, rc *RunContext) (*RunContext, *StageResult, error) {
	// Create run directory
	runPath := filepath.Join(s.RunDir, rc.RunID)
	if err := os.MkdirAll(runPath, 0755); err != nil {
		return rc, &StageResult{
			StageName: s.Name(),
			Success:   false,
			Error:     fmt.Sprintf("failed to create run directory: %v", err),
		}, nil
	}

	// Update RunContext with the resolved path using copy-on-write
	newRC := rc.WithRunDir(runPath)

	// Emit task.created event
	evt := event.NewEvent(event.V1EventTypes.TaskCreated, "prizm-cli", map[string]any{
		"run_id":   rc.RunID,
		"task":     rc.Task,
		"project":  rc.Project,
		"agent":    rc.Agent,
		"provider": rc.ProviderName,
		"model":    rc.Model,
	})
	newRC = newRC.WithEvent(evt)

	return newRC, &StageResult{
		StageName: s.Name(),
		Success:   true,
		Data:      map[string]any{"run_path": runPath},
	}, nil
}

// Rollback removes the run directory if it was created.
func (s *ConnectionStage) Rollback(ctx context.Context, rc *RunContext) error {
	runPath := filepath.Join(s.RunDir, rc.RunID)
	return os.RemoveAll(runPath)
}
