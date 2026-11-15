package api

import (
	"context"
	"net/http"
	"strings"
)

// FreewillStrategyExperimentRun is a metadata-only view of one immutable
// host-validated task receipt under a host-predeclared strategy variant.
type FreewillStrategyExperimentRun struct {
	TaskID                   string `json:"task_id"`
	ParentRunID              string `json:"parent_run_id"`
	ChildRunID               string `json:"child_run_id"`
	StrategyID               string `json:"strategy_id"`
	StrategyVersion          string `json:"strategy_version"`
	Role                     string `json:"role"`
	ReceiptID                string `json:"receipt_id"`
	ReportSHA256             string `json:"report_sha256"`
	ApprovedPatchSHA256      string `json:"approved_patch_sha256"`
	StatusClassification     string `json:"status_classification"`
	ValidationClassification string `json:"validation_classification"`
}

// FreewillStrategyExperimentRecord contains only the two host-bound receipts
// and one bounded qualitative comparison. It grants no execution authority.
type FreewillStrategyExperimentRecord struct {
	Version                string                          `json:"version"`
	ExperimentID           string                          `json:"experiment_id"`
	OwnerID                string                          `json:"owner_id"`
	ProjectID              string                          `json:"project_id"`
	WorkspaceID            string                          `json:"workspace_id"`
	AgentID                string                          `json:"agent_id"`
	SourceSnapshotTree     string                          `json:"source_snapshot_tree"`
	TitleGoalBindingSHA256 string                          `json:"title_goal_binding_sha256"`
	Runs                   []FreewillStrategyExperimentRun `json:"runs"`
	Comparison             string                          `json:"comparison"`
	Uncertainty            string                          `json:"uncertainty"`
	RecordedAt             string                          `json:"recorded_at"`
}

// FreewillStrategyExperimentController exposes an explicit finalize/read seam
// over a single configured pair. Implementations must not dispatch work.
type FreewillStrategyExperimentController interface {
	Finalize(context.Context, string, string, string, string, []string) (FreewillStrategyExperimentRecord, error)
	ReadStrategyExperiment(context.Context, string, string, string, string, []string) (FreewillStrategyExperimentRecord, error)
}

type freewillStrategyExperimentScopeRequest struct {
	OwnerID   string   `json:"owner_id"`
	ProjectID string   `json:"project_id"`
	Workspace string   `json:"workspace"`
	AgentID   string   `json:"agent_id"`
	TaskIDs   []string `json:"task_ids"`
}

func (s *Server) handleFreewillStrategyExperiments(w http.ResponseWriter, r *http.Request) {
	if s.freewillStrategyExperiments == nil {
		writeJSONError(w, "Freewill strategy experiments are not configured", http.StatusServiceUnavailable)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/freewill/strategy-experiments/"), "/")
	switch path {
	case "finalize":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request freewillStrategyExperimentScopeRequest
		if err := decodeStrictJSON(w, r, s.maxRequestBytes, &request); err != nil {
			writeJSONError(w, "invalid Freewill strategy experiment scope", http.StatusBadRequest)
			return
		}
		ownerID, ok := s.freewillPrincipalOwner(r, request.OwnerID)
		if !ok {
			writeJSONError(w, "Freewill strategy experiment owner was rejected", http.StatusForbidden)
			return
		}
		record, err := s.freewillStrategyExperiments.Finalize(r.Context(), ownerID, request.ProjectID, request.Workspace, request.AgentID, request.TaskIDs)
		if err != nil {
			writeJSONError(w, "Freewill strategy comparison is unavailable", http.StatusConflict)
			return
		}
		writeJSON(w, record)
	case "current":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if s.freewillPrincipalQueryHasDuplicates(r, "owner_id", "project_id", "workspace", "agent_id") {
			writeJSONError(w, "Freewill strategy experiment owner was rejected", http.StatusBadRequest)
			return
		}
		ownerValues := r.URL.Query()["owner_id"]
		requestedOwner := ""
		if len(ownerValues) == 1 {
			requestedOwner = ownerValues[0]
		}
		ownerID, ok := s.freewillPrincipalOwner(r, requestedOwner)
		if !ok {
			writeJSONError(w, "Freewill strategy experiment owner was rejected", http.StatusForbidden)
			return
		}
		record, err := s.freewillStrategyExperiments.ReadStrategyExperiment(r.Context(), ownerID, r.URL.Query().Get("project_id"), r.URL.Query().Get("workspace"), r.URL.Query().Get("agent_id"), r.URL.Query()["task_id"])
		if err != nil {
			writeJSONError(w, "Freewill strategy comparison is unavailable", http.StatusNotFound)
			return
		}
		writeJSON(w, record)
	default:
		http.NotFound(w, r)
	}
}
