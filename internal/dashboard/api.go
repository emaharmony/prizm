package dashboard

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/emaharmony/prizm/internal/projection"
	approvalproj "github.com/emaharmony/prizm/internal/projection/builtin/approval"
	"github.com/emaharmony/prizm/internal/projection/builtin/runstatus"
	"github.com/emaharmony/prizm/internal/projection/builtin/toolhistory"
	"github.com/emaharmony/prizm/internal/safety"

	"gopkg.in/yaml.v3"
)

// handleRuns returns a list of all runs with their status.
// GET /api/runs
//
// Response: JSON array of run objects, sorted newest first.
// Each object contains: run_id, status, task, agent, project, started_at, completed_at.
func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	entries, err := os.ReadDir(s.runDir)
	if err != nil {
		writeJSONError(w, "failed to read runs directory", http.StatusInternalServerError)
		return
	}

	type runInfo struct {
		RunID       string `json:"run_id"`
		Status      string `json:"status"`
		Task        string `json:"task,omitempty"`
		Agent       string `json:"agent,omitempty"`
		Project     string `json:"project,omitempty"`
		StartedAt   string `json:"started_at,omitempty"`
		CompletedAt string `json:"completed_at,omitempty"`
	}

	var runs []runInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "policy" {
			continue
		}

		// Try to read the run_status projection
		projFile := filepath.Join(s.runDir, name, "projections", "run_status.json")
		data, err := os.ReadFile(projFile)
		if err != nil {
			// No projection yet — try summary.json
			summaryFile := filepath.Join(s.runDir, name, "summary.json")
			sdata, serr := os.ReadFile(summaryFile)
			if serr != nil {
				runs = append(runs, runInfo{RunID: name, Status: "unknown"})
				continue
			}
			var summary map[string]any
			if err := json.Unmarshal(sdata, &summary); err != nil {
				runs = append(runs, runInfo{RunID: name, Status: "unknown"})
				continue
			}
			ri := runInfo{RunID: name}
			if s, ok := summary["status"].(string); ok {
				ri.Status = s
			}
			if t, ok := summary["task"].(string); ok {
				ri.Task = t
			}
			if a, ok := summary["agent"].(string); ok {
				ri.Agent = a
			}
			if p, ok := summary["project"].(string); ok {
				ri.Project = p
			}
			runs = append(runs, ri)
			continue
		}

		var proj map[string]any
		if err := json.Unmarshal(data, &proj); err != nil {
			runs = append(runs, runInfo{RunID: name, Status: "unknown"})
			continue
		}

		ri := runInfo{RunID: name}
		if s, ok := proj["status"].(string); ok {
			ri.Status = s
		}
		if t, ok := proj["task"].(string); ok {
			ri.Task = t
		}
		if a, ok := proj["agent"].(string); ok {
			ri.Agent = a
		}
		if p, ok := proj["project"].(string); ok {
			ri.Project = p
		}
		if sa, ok := proj["started_at"].(string); ok {
			ri.StartedAt = sa
		}
		if ca, ok := proj["completed_at"].(string); ok {
			ri.CompletedAt = ca
		}
		runs = append(runs, ri)
	}

	// Sort newest first (run IDs contain timestamps via ULID)
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].RunID > runs[j].RunID
	})

	writeJSON(w, runs)
}

// handleRunDetail returns details for a specific run.
// GET /api/runs/{run_id}
//
// Response: JSON object with summary + available projection names.
func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	runID := runIDFromPath(r.URL.Path, "/api/runs/")
	if runID == "" {
		writeJSONError(w, "run ID required", http.StatusBadRequest)
		return
	}

	runPath, err := safety.ResolveAndContain(s.runDir, runID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	if _, err := os.Stat(runPath); os.IsNotExist(err) {
		writeJSONError(w, "run not found", http.StatusNotFound)
		return
	}

	// Read summary.json
	summaryFile := filepath.Join(runPath, "summary.json")
	summaryData, err := os.ReadFile(summaryFile)
	var summary map[string]any
	if err == nil {
		json.Unmarshal(summaryData, &summary)
	}

	// List available projections
	projDir := filepath.Join(runPath, "projections")
	var projections []string
	if entries, err := os.ReadDir(projDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				name := strings.TrimSuffix(e.Name(), ".json")
				projections = append(projections, name)
			}
		}
	}

	// Count events
	eventsFile := filepath.Join(runPath, "events.jsonl")
	eventCount := 0
	if data, err := os.ReadFile(eventsFile); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) != "" {
				eventCount++
			}
		}
	}

	result := map[string]any{
		"run_id":      runID,
		"summary":     summary,
		"projections": projections,
		"event_count": eventCount,
	}

	writeJSON(w, result)
}

// handleEvents returns events for a specific run.
// GET /api/events/{run_id}
//
// Response: JSON array of event objects parsed from events.jsonl.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	runID := runIDFromPath(r.URL.Path, "/api/events/")
	if runID == "" {
		writeJSONError(w, "run ID required", http.StatusBadRequest)
		return
	}

	runPath, err := safety.ResolveAndContain(s.runDir, runID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	eventsFile := filepath.Join(runPath, "events.jsonl")
	events, err := projection.ReadEventsFromJSONL(eventsFile)
	if err != nil {
		writeJSONError(w, "failed to read events", http.StatusInternalServerError)
		return
	}

	writeJSON(w, events)
}

// handleProjections returns a projection snapshot for a specific run.
// GET /api/projections/{run_id}/{projection_name}
//
// Response: JSON object from the projection snapshot file.
func (s *Server) handleProjections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse: /api/projections/{run_id}/{name}
	path := strings.TrimPrefix(r.URL.Path, "/api/projections/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		writeJSONError(w, "run ID and projection name required", http.StatusBadRequest)
		return
	}

	runID := parts[0]
	projName := parts[1]

	runPath, err := safety.ResolveAndContain(s.runDir, runID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	projFile := filepath.Join(runPath, "projections", projName+".json")
	data, err := os.ReadFile(projFile)
	if err != nil {
		writeJSONError(w, "projection not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// handlePolicies returns the current policy configuration.
// GET /api/policies
//
// Response: JSON array of policy rules parsed from YAML.
func (s *Server) handlePolicies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	type policyRule struct {
		ID          string `yaml:"id" json:"id"`
		Description string `yaml:"description" json:"description"`
		Decision    string `yaml:"decision" json:"decision"`
		Reason      string `yaml:"reason" json:"reason,omitempty"`
		Severity    string `yaml:"severity" json:"severity,omitempty"`
	}

	type policyFile struct {
		Policies []policyRule `yaml:"policies" json:"policies"`
	}

	var allPolicies []policyRule

	entries, err := os.ReadDir(s.policyDir)
	if err != nil {
		writeJSONError(w, "failed to read policies directory", http.StatusInternalServerError)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(s.policyDir, name))
		if err != nil {
			continue
		}

		var pf policyFile
		if err := yaml.Unmarshal(data, &pf); err != nil {
			continue
		}

		allPolicies = append(allPolicies, pf.Policies...)
	}

	writeJSON(w, allPolicies)
}

// handleAdapters returns the list of registered adapters.
// GET /api/adapters
//
// Response: JSON array of adapter info objects.
func (s *Server) handleAdapters(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Build a minimal adapter list from manifests
	adapterDir := "adapters"
	type adapterInfo struct {
		Name         string `json:"name"`
		Version      string `json:"version,omitempty"`
		Description  string `json:"description,omitempty"`
		FromManifest bool   `json:"from_manifest"`
	}

	var adapters []adapterInfo

	// Check for adapter manifests
	if entries, err := os.ReadDir(adapterDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			manifestFile := filepath.Join(adapterDir, entry.Name(), "manifest.yaml")
			data, err := os.ReadFile(manifestFile)
			if err != nil {
				continue
			}
			var m struct {
				Name        string `yaml:"name"`
				Version     string `yaml:"version"`
				Description string `yaml:"description"`
			}
			if err := yaml.Unmarshal(data, &m); err != nil {
				continue
			}
			adapters = append(adapters, adapterInfo{
				Name:         m.Name,
				Version:      m.Version,
				Description:  m.Description,
				FromManifest: true,
			})
		}
	}

	// Add built-in adapters (echo)
	adapters = append(adapters, adapterInfo{
		Name: "echo", Version: "1.0", Description: "Built-in echo adapter for testing",
	})

	writeJSON(w, adapters)
}

// writeJSON writes a JSON response with proper headers.
func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(data)
}

// writeJSONError writes a JSON error response.
func writeJSONError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// compile-time checks
var _ = runstatus.New
var _ = approvalproj.New
var _ = toolhistory.New
var _ = projection.NewRunner
