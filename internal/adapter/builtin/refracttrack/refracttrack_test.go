package refracttrack

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/adapter"
)

func TestRefractTrackName(t *testing.T) {
	a := New()
	if a.Name() != "refract-track" {
		t.Errorf("Name() = %q, want refract-track", a.Name())
	}
}

func TestRefractTrackVersion(t *testing.T) {
	a := New()
	if a.Version() != "1.0.0" {
		t.Errorf("Version() = %q, want 1.0.0", a.Version())
	}
}

func TestRefractTrackCapabilities(t *testing.T) {
	a := New()
	caps := a.Capabilities()
	if len(caps) != 3 {
		t.Fatalf("Capabilities() returned %d, want 3", len(caps))
	}
	actions := map[string]bool{
		"log_progress":  false,
		"query_status":  false,
		"list_projects": false,
	}
	for _, c := range caps {
		actions[c.Action] = true
	}
	for action, found := range actions {
		if !found {
			t.Errorf("missing capability: %s", action)
		}
	}
}

func TestRefractTrackHealth(t *testing.T) {
	a := NewWithPath(t.TempDir())
	health, err := a.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if !health.Ready {
		t.Error("Health() should report ready")
	}
}

func TestRefractTrackLogProgress(t *testing.T) {
	tmpDir := t.TempDir()
	a := NewWithPath(tmpDir)

	result, err := a.Execute(context.Background(), "log_progress", map[string]any{
		"project": "test-project",
		"run_id":  "run_123",
		"task":    "implement feature X",
		"status":  "completed",
		"agent":   "lumi",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Output["logged"] != true {
		t.Error("log_progress should return logged=true")
	}

	// Verify the file was created
	entryPath := filepath.Join(tmpDir, "test-project", "progress.jsonl")
	if _, err := os.Stat(entryPath); os.IsNotExist(err) {
		t.Error("progress.jsonl not created")
	}
}

func TestRefractTrackQueryStatus(t *testing.T) {
	tmpDir := t.TempDir()
	a := NewWithPath(tmpDir)

	// Log some progress first
	a.Execute(context.Background(), "log_progress", map[string]any{
		"project": "test-project",
		"run_id":  "run_1",
		"task":    "task 1",
		"status":  "completed",
	})
	a.Execute(context.Background(), "log_progress", map[string]any{
		"project": "test-project",
		"run_id":  "run_2",
		"task":    "task 2",
		"status":  "failed",
	})

	result, err := a.Execute(context.Background(), "query_status", map[string]any{
		"project": "test-project",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Output["entries"] != 2 {
		t.Errorf("entries = %v, want 2", result.Output["entries"])
	}
	if result.Output["completed"] != 1 {
		t.Errorf("completed = %v, want 1", result.Output["completed"])
	}
	if result.Output["failed"] != 1 {
		t.Errorf("failed = %v, want 1", result.Output["failed"])
	}
}

func TestRefractTrackListProjects(t *testing.T) {
	tmpDir := t.TempDir()
	a := NewWithPath(tmpDir)

	// Create some projects
	a.Execute(context.Background(), "log_progress", map[string]any{
		"project": "project-alpha",
		"task":    "task 1",
	})
	a.Execute(context.Background(), "log_progress", map[string]any{
		"project": "project-beta",
		"task":    "task 2",
	})

	result, err := a.Execute(context.Background(), "list_projects", nil)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	projects, ok := result.Output["projects"].([]string)
	if !ok {
		t.Fatalf("projects type = %T, want []string", result.Output["projects"])
	}
	if len(projects) != 2 {
		t.Errorf("projects count = %d, want 2", len(projects))
	}
}

func TestRefractTrackQueryStatusEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	a := NewWithPath(tmpDir)

	result, err := a.Execute(context.Background(), "query_status", map[string]any{
		"project": "nonexistent",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Output["entries"] != 0 {
		t.Errorf("entries = %v, want 0 for nonexistent project", result.Output["entries"])
	}
}

func TestRefractTrackUnknownAction(t *testing.T) {
	a := New()
	_, err := a.Execute(context.Background(), "unknown_action", nil)
	if err == nil {
		t.Error("unknown action should return error")
	}
}

func TestRefractTrackAdapterInterface(t *testing.T) {
	// Verify RefractTrackAdapter implements the adapter.Adapter interface
	var _ adapter.Adapter = New()
}
