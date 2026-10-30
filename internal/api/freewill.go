package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/emaharmony/prizm/internal/freewill"
)

type freewillControlRequest struct {
	AgentID   string `json:"agent_id"`
	ProjectID string `json:"project_id"`
	Workspace string `json:"workspace"`
	Halted    *bool  `json:"halted"`
}

func (s *Server) handleFreewillControl(w http.ResponseWriter, r *http.Request) {
	if s.freewillControl == nil {
		writeJSONError(w, "Freewill operator control is not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodGet {
		query := r.URL.Query()
		state, err := s.freewillControl.Status(r.Context(), query.Get("agent_id"), freewill.Scope{
			ProjectID: query.Get("project_id"), Workspace: query.Get("workspace"),
		})
		if err != nil {
			writeJSONError(w, "Freewill operator control unavailable", http.StatusBadRequest)
			return
		}
		writeJSON(w, state)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request freewillControlRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSONError(w, "invalid Freewill operator control", http.StatusBadRequest)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeJSONError(w, "invalid Freewill operator control", http.StatusBadRequest)
		return
	}
	if request.Halted == nil {
		writeJSONError(w, "halted must be explicitly set", http.StatusBadRequest)
		return
	}
	state, err := s.freewillControl.SetHalted(r.Context(), request.AgentID, freewill.Scope{
		ProjectID: request.ProjectID, Workspace: request.Workspace,
	}, *request.Halted, "api:operator")
	if err != nil {
		writeJSONError(w, "Freewill operator control could not be recorded", http.StatusConflict)
		return
	}
	writeJSON(w, state)
}

type freewillDecisionRequest struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

func (s *Server) handleFreewillApproval(w http.ResponseWriter, r *http.Request) {
	if s.freewillApprovals == nil {
		writeJSONError(w, "Freewill approval service is not configured", http.StatusServiceUnavailable)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/freewill/runs/"), "/"), "/")
	if len(parts) == 1 && validMultiAgentRunID(parts[0]) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		status, err := s.freewillApprovals.Status(r.Context(), parts[0])
		if err != nil {
			writeJSONError(w, "Freewill status unavailable", http.StatusNotFound)
			return
		}
		writeJSON(w, status)
		return
	}
	if len(parts) != 4 || !validMultiAgentRunID(parts[0]) || parts[1] != "approvals" || strings.TrimSpace(parts[2]) == "" || parts[3] != "decision" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request freewillDecisionRequest
	decoder := json.NewDecoder(io.LimitReader(http.MaxBytesReader(w, r.Body, s.maxRequestBytes), 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSONError(w, "invalid Freewill decision", http.StatusBadRequest)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeJSONError(w, "invalid Freewill decision", http.StatusBadRequest)
		return
	}
	if (request.Decision != "grant" && request.Decision != "deny") || len(request.Reason) > 256 {
		writeJSONError(w, "invalid Freewill decision", http.StatusBadRequest)
		return
	}
	actor := r.URL.Query().Get("by")
	if actor == "" {
		actor = "api:user"
	}
	status, err := s.freewillApprovals.DecideStatus(r.Context(), parts[0], parts[2], request.Decision, actor, request.Reason)
	if err != nil {
		writeJSONError(w, "Freewill decision could not be recorded", http.StatusConflict)
		return
	}
	writeJSON(w, status)
}
