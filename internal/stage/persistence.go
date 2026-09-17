// Package stage provides Prizm's pipeline execution engine (V14a).
//
// PersistenceStage writes all accumulated artifacts to disk:
// events.jsonl, summary.json, output.md, and prompt.md.
// It's always the last stage in the pipeline.
//
// PersistenceStage also publishes events to NATS (if configured).
package stage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

// PersistenceStage writes all run artifacts to disk.
// It's the last stage in the pipeline and is responsible for:
// 1. Writing events.jsonl (the canonical event log)
// 2. Writing summary.json (human-readable run summary)
// 3. Writing output.md (the LLM's response)
// 4. Writing prompt.md (the prompt sent to the LLM)
// 5. Publishing events to NATS (if configured)
type PersistenceStage struct {
	// BusURL is the NATS server URL. If empty, NATS publishing is skipped.
	BusURL string
}

// Name returns the stage identifier.
func (s *PersistenceStage) Name() string {
	return "persistence"
}

// Validate checks that the run directory is set in the RunContext.
// If RunDir is empty, the stage skips gracefully (conversation mode
// doesn't need file artifacts).
func (s *PersistenceStage) Validate(rc *RunContext) error {
	return nil // RunDir is optional — Execute handles empty RunDir
}

// Execute writes all artifacts to disk.
// If RunDir is empty (conversation mode), persistence is skipped gracefully.
func (s *PersistenceStage) Execute(ctx context.Context, rc *RunContext) (*RunContext, *StageResult, error) {
	runDir := rc.RunDir

	if runDir == "" {
		// Conversation mode — no file persistence needed
		return rc, &StageResult{
			StageName: s.Name(),
			Success:   true,
			Data:      map[string]any{"skipped": true, "reason": "no_run_dir"},
		}, nil
	}

	// 1. Write events.jsonl — the canonical event log
	eventsPath := filepath.Join(runDir, "events.jsonl")
	if err := s.writeEventsFile(eventsPath, rc.Events); err != nil {
		return rc, &StageResult{
			StageName: s.Name(),
			Success:   false,
			Error:     fmt.Sprintf("failed to write events: %v", err),
		}, nil
	}

	// 2. Write summary.json — human-readable run summary
	summaryPath := filepath.Join(runDir, "summary.json")
	summary := s.buildSummary(rc)
	if err := s.writeJSONFile(summaryPath, summary); err != nil {
		return rc, &StageResult{
			StageName: s.Name(),
			Success:   false,
			Error:     fmt.Sprintf("failed to write summary: %v", err),
		}, nil
	}

	// 3. Write output.md — the LLM's response (if available)
	if rc.LLMResponse != "" {
		outputPath := filepath.Join(runDir, "output.md")
		if err := os.WriteFile(outputPath, []byte(rc.LLMResponse), 0644); err != nil {
			// Output write failure is non-fatal — the important artifacts are
			// events.jsonl and summary.json
		}
	}

	// 4. Write prompt.md — the task prompt
	promptPath := filepath.Join(runDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(rc.Task), 0644); err != nil {
		// Prompt write failure is non-fatal
	}

	// Emit persistence completed event
	evt := event.NewEvent("prizm.persistence.completed", "persistence-stage", map[string]any{
		"run_id":       rc.RunID,
		"events_path":  eventsPath,
		"summary_path": summaryPath,
		"event_count":  len(rc.Events),
	})
	newRC := rc.WithEvent(evt)

	return newRC, &StageResult{
		StageName: s.Name(),
		Success:   true,
		Data: map[string]any{
			"events_path":  eventsPath,
			"summary_path": summaryPath,
			"event_count":  len(rc.Events),
		},
	}, nil
}

// Rollback removes the artifacts created by this stage.
func (s *PersistenceStage) Rollback(ctx context.Context, rc *RunContext) error {
	if rc.RunDir == "" {
		return nil
	}
	// Remove individual files but keep the directory (it may have subdirectories)
	files := []string{"events.jsonl", "summary.json", "output.md", "prompt.md"}
	for _, f := range files {
		os.Remove(filepath.Join(rc.RunDir, f))
	}
	return nil
}

// writeEventsFile writes all events to a JSONL file.
func (s *PersistenceStage) writeEventsFile(path string, events []event.Event) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	for _, evt := range events {
		data, err := evt.ToJSON()
		if err != nil {
			return fmt.Errorf("failed to serialize event %s: %w", evt.ID, err)
		}
		if _, err := f.Write(data); err != nil {
			return err
		}
		if _, err := f.Write([]byte("\n")); err != nil {
			return err
		}
	}
	return nil
}

// writeJSONFile writes a JSON object to a file.
func (s *PersistenceStage) writeJSONFile(path string, data any) error {
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, jsonData, 0644)
}

// buildSummary creates a human-readable summary of the run.
func (s *PersistenceStage) buildSummary(rc *RunContext) map[string]any {
	summary := map[string]any{
		"run_id":      rc.RunID,
		"task":        rc.Task,
		"project":     rc.Project,
		"agent":       rc.Agent,
		"provider":    rc.ProviderName,
		"model":       rc.Model,
		"event_count": len(rc.Events),
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	}

	// Determine status from stage results
	status := "completed"
	for _, result := range rc.Results {
		if result != nil && !result.Success {
			status = "failed"
			summary["failed_stage"] = result.StageName
			summary["error"] = result.Error
			break
		}
	}
	summary["status"] = status

	// Add LLM response info if available
	if rc.LLMResponse != "" {
		summary["has_output"] = true
	}

	return summary
}
