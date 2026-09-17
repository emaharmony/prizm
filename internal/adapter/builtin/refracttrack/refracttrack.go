// Package refracttrack implements the Refract Track adapter (V14c).
//
// Refract Track is Prizm's first real adapter. It auto-logs project progress
// when tasks complete. The model doesn't track progress — the event system does.
//
// When a `prizm.task.completed` event fires, Refract Track automatically
// creates a progress entry with the task description, status, agent, and
// timestamp. No manual action needed.
//
// This IS the thesis in action: state changes trigger actions automatically.
// The event bus is the render loop. React::setState → re-render ::
// Prizm::emitEvent → runActions.
//
// Storage is JSONL at ~/.prizm/refract-track/<project>/progress.jsonl.
// Each entry is a JSON object with task, status, agent, and timestamp.
package refracttrack

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/emaharmony/prizm/internal/adapter"
)

// RefractTrackAdapter auto-logs project progress from task completion events.
type RefractTrackAdapter struct {
	storagePath string // ~/.prizm/refract-track/
}

// progressEntry is a single progress log entry.
type progressEntry struct {
	Timestamp string `json:"timestamp"`
	RunID     string `json:"run_id"`
	Task      string `json:"task"`
	Status    string `json:"status"`
	Agent     string `json:"agent"`
	Project   string `json:"project"`
}

// New creates a new RefractTrackAdapter.
// Storage path defaults to ~/.prizm/refract-track/.
func New() *RefractTrackAdapter {
	homeDir, _ := os.UserHomeDir()
	storagePath := filepath.Join(homeDir, ".prizm", "refract-track")
	return &RefractTrackAdapter{storagePath: storagePath}
}

// NewWithPath creates a RefractTrackAdapter with a custom storage path.
// Use this for testing or custom configurations.
func NewWithPath(storagePath string) *RefractTrackAdapter {
	return &RefractTrackAdapter{storagePath: storagePath}
}

// Name returns the adapter name.
func (a *RefractTrackAdapter) Name() string {
	return "refract-track"
}

// Version returns the adapter version.
func (a *RefractTrackAdapter) Version() string {
	return "1.0.0"
}

// Capabilities returns what this adapter can do.
func (a *RefractTrackAdapter) Capabilities() []adapter.Capability {
	return []adapter.Capability{
		{Action: "log_progress", Description: "Auto-log task progress when tasks complete"},
		{Action: "query_status", Description: "Query the status of a project's progress"},
		{Action: "list_projects", Description: "List all tracked projects"},
	}
}

// Execute runs a Refract Track action.
// Supported actions: log_progress, query_status, list_projects.
func (a *RefractTrackAdapter) Execute(ctx context.Context, action string, input map[string]any) (*adapter.Result, error) {
	switch action {
	case "log_progress":
		return a.logProgress(input)
	case "query_status":
		return a.queryStatus(input)
	case "list_projects":
		return a.listProjects()
	default:
		return nil, fmt.Errorf("refract-track: unknown action %q", action)
	}
}

// Health checks if the adapter is ready.
// Refract Track always reports healthy — it writes to local files.
func (a *RefractTrackAdapter) Health(ctx context.Context) (*adapter.HealthResult, error) {
	// Ensure storage directory exists
	if err := os.MkdirAll(a.storagePath, 0755); err != nil {
		return &adapter.HealthResult{
			Ready:   false,
			Message: fmt.Sprintf("cannot create storage directory: %v", err),
		}, nil
	}

	return &adapter.HealthResult{
		Ready:   true,
		Message: "refract-track is healthy",
	}, nil
}

// logProgress writes a progress entry to the project's JSONL file.
func (a *RefractTrackAdapter) logProgress(input map[string]any) (*adapter.Result, error) {
	project, _ := input["project"].(string)
	if project == "" {
		project = "default"
	}
	runID, _ := input["run_id"].(string)
	task, _ := input["task"].(string)
	status, _ := input["status"].(string)
	agent, _ := input["agent"].(string)

	if status == "" {
		status = "completed"
	}

	entry := progressEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		RunID:     runID,
		Task:      task,
		Status:    status,
		Agent:     agent,
		Project:   project,
	}

	// Ensure project directory exists
	projectDir := filepath.Join(a.storagePath, sanitizeName(project))
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		return nil, fmt.Errorf("refract-track: create project dir: %w", err)
	}

	// Append entry to project JSONL
	entryPath := filepath.Join(projectDir, "progress.jsonl")
	data, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("refract-track: marshal entry: %w", err)
	}

	f, err := os.OpenFile(entryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("refract-track: open progress file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return nil, fmt.Errorf("refract-track: write entry: %w", err)
	}

	return &adapter.Result{
		Output: map[string]any{
			"logged":     true,
			"project":    project,
			"entry_path": entryPath,
		},
	}, nil
}

// queryStatus reads the progress entries for a project.
func (a *RefractTrackAdapter) queryStatus(input map[string]any) (*adapter.Result, error) {
	project, _ := input["project"].(string)
	if project == "" {
		project = "default"
	}

	projectDir := filepath.Join(a.storagePath, sanitizeName(project))
	entryPath := filepath.Join(projectDir, "progress.jsonl")

	data, err := os.ReadFile(entryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &adapter.Result{
				Output: map[string]any{
					"project": project,
					"entries": 0,
					"message": "no progress entries found",
				},
			}, nil
		}
		return nil, fmt.Errorf("refract-track: read progress: %w", err)
	}

	lines := splitLines(data)
	entries := make([]progressEntry, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry progressEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip malformed entries
		}
		entries = append(entries, entry)
	}

	// Count statuses
	completed := 0
	failed := 0
	for _, e := range entries {
		switch e.Status {
		case "completed":
			completed++
		case "failed":
			failed++
		}
	}

	return &adapter.Result{
		Output: map[string]any{
			"project":   project,
			"entries":   len(entries),
			"completed": completed,
			"failed":    failed,
		},
	}, nil
}

// listProjects lists all tracked projects.
func (a *RefractTrackAdapter) listProjects() (*adapter.Result, error) {
	entries, err := os.ReadDir(a.storagePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &adapter.Result{
				Output: map[string]any{
					"projects": []string{},
				},
			}, nil
		}
		return nil, fmt.Errorf("refract-track: read projects: %w", err)
	}

	projects := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			projects = append(projects, entry.Name())
		}
	}

	return &adapter.Result{
		Output: map[string]any{
			"projects": projects,
			"count":    len(projects),
		},
	}, nil
}

// sanitizeName removes characters that are unsafe for file paths.
func sanitizeName(name string) string {
	safe := make([]byte, 0, len(name))
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			safe = append(safe, byte(c))
		}
	}
	if len(safe) == 0 {
		return "default"
	}
	return string(safe)
}

// splitLines splits byte data into lines.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			line := data[start:i]
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
