package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type freewillGoalProposalRequest struct {
	OwnerID     string   `json:"owner_id"`
	ProjectID   string   `json:"project_id"`
	AgentID     string   `json:"agent_id"`
	ChildRunIDs []string `json:"child_run_ids"`
}

type freewillGoalDecisionRequest struct {
	OwnerID   string `json:"owner_id"`
	ProjectID string `json:"project_id"`
	AgentID   string `json:"agent_id"`
	Decision  string `json:"decision"`
}

type freewillGoalOwnerRequest struct {
	OwnerID   string `json:"owner_id"`
	ProjectID string `json:"project_id"`
	AgentID   string `json:"agent_id"`
}

type freewillGoalPauseRequest struct {
	OwnerID   string `json:"owner_id"`
	ProjectID string `json:"project_id"`
	AgentID   string `json:"agent_id"`
	Reason    string `json:"reason"`
}

type freewillGoalProgressRequest struct {
	OwnerID         string `json:"owner_id"`
	ProjectID       string `json:"project_id"`
	AgentID         string `json:"agent_id"`
	GoalID          string `json:"goal_id"`
	GoalRevision    int    `json:"goal_revision"`
	RequestID       string `json:"request_id"`
	ChildRunID      string `json:"child_run_id"`
	OwnerSelfReport string `json:"owner_self_report,omitempty"`
}

func (s *Server) handleFreewillGoals(w http.ResponseWriter, r *http.Request) {
	if s.freewillGoals == nil {
		writeJSONError(w, "Freewill goal proposals are not configured", http.StatusServiceUnavailable)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/freewill/goals/"), "/"), "/")
	if len(parts) == 1 && parts[0] == "proposals" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request freewillGoalProposalRequest
		if err := decodeStrictJSON(w, r, s.maxRequestBytes, &request); err != nil {
			writeJSONError(w, "invalid Freewill goal proposal request", http.StatusBadRequest)
			return
		}
		ownerID, ok := s.freewillPrincipalOwner(r, request.OwnerID)
		if !ok {
			writeJSONError(w, "Freewill goal owner was rejected", http.StatusForbidden)
			return
		}
		proposal, err := s.freewillGoals.Propose(r.Context(), ownerID, request.ProjectID, request.AgentID, request.ChildRunIDs)
		if err != nil {
			writeJSONError(w, "Freewill goal proposal could not be recorded", http.StatusConflict)
			return
		}
		writeJSON(w, proposal)
		return
	}
	if len(parts) == 2 && parts[0] == "proposals" && validMultiAgentRunID(parts[1]) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if s.freewillPrincipalQueryHasDuplicates(r, "owner_id", "project_id", "agent_id") {
			writeJSONError(w, "Freewill goal owner was rejected", http.StatusBadRequest)
			return
		}
		ownerValues := r.URL.Query()["owner_id"]
		requestedOwner := ""
		if len(ownerValues) == 1 {
			requestedOwner = ownerValues[0]
		}
		ownerID, ok := s.freewillPrincipalOwner(r, requestedOwner)
		if !ok {
			writeJSONError(w, "Freewill goal owner was rejected", http.StatusForbidden)
			return
		}
		record, err := s.freewillGoals.Read(r.Context(), ownerID, r.URL.Query().Get("project_id"), r.URL.Query().Get("agent_id"), parts[1])
		if err != nil {
			writeJSONError(w, "Freewill goal proposal is unavailable", http.StatusNotFound)
			return
		}
		writeJSON(w, record)
		return
	}
	if len(parts) == 3 && parts[0] == "proposals" && validMultiAgentRunID(parts[1]) && parts[2] == "activate" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request freewillGoalOwnerRequest
		if err := decodeStrictJSON(w, r, s.maxRequestBytes, &request); err != nil {
			writeJSONError(w, "invalid Freewill goal activation", http.StatusBadRequest)
			return
		}
		ownerID, ok := s.freewillPrincipalOwner(r, request.OwnerID)
		if !ok {
			writeJSONError(w, "Freewill goal owner was rejected", http.StatusForbidden)
			return
		}
		goal, err := s.freewillGoals.Activate(r.Context(), ownerID, request.ProjectID, request.AgentID, parts[1])
		if err != nil {
			writeJSONError(w, "Freewill goal could not be activated", http.StatusConflict)
			return
		}
		writeJSON(w, goal)
		return
	}
	if len(parts) == 3 && parts[0] == "proposals" && validMultiAgentRunID(parts[1]) && parts[2] == "pause" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request freewillGoalPauseRequest
		if err := decodeStrictJSON(w, r, s.maxRequestBytes, &request); err != nil {
			writeJSONError(w, "invalid Freewill goal pause", http.StatusBadRequest)
			return
		}
		ownerID, ok := s.freewillPrincipalOwner(r, request.OwnerID)
		if !ok {
			writeJSONError(w, "Freewill goal owner was rejected", http.StatusForbidden)
			return
		}
		goal, err := s.freewillGoals.Pause(r.Context(), ownerID, request.ProjectID, request.AgentID, parts[1], request.Reason)
		if err != nil {
			writeJSONError(w, "Freewill goal could not be paused", http.StatusConflict)
			return
		}
		writeJSON(w, goal)
		return
	}
	if len(parts) == 3 && parts[0] == "proposals" && validMultiAgentRunID(parts[1]) && parts[2] == "resume" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request freewillGoalPauseRequest
		if err := decodeStrictJSON(w, r, s.maxRequestBytes, &request); err != nil {
			writeJSONError(w, "invalid Freewill goal resume", http.StatusBadRequest)
			return
		}
		ownerID, ok := s.freewillPrincipalOwner(r, request.OwnerID)
		if !ok {
			writeJSONError(w, "Freewill goal owner was rejected", http.StatusForbidden)
			return
		}
		goal, err := s.freewillGoals.Resume(r.Context(), ownerID, request.ProjectID, request.AgentID, parts[1], request.Reason)
		if err != nil {
			writeJSONError(w, "Freewill goal could not be resumed", http.StatusConflict)
			return
		}
		writeJSON(w, goal)
		return
	}
	if len(parts) == 3 && parts[0] == "proposals" && validMultiAgentRunID(parts[1]) && parts[2] == "decision" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request freewillGoalDecisionRequest
		if err := decodeStrictJSON(w, r, s.maxRequestBytes, &request); err != nil {
			writeJSONError(w, "invalid Freewill goal decision", http.StatusBadRequest)
			return
		}
		ownerID, ok := s.freewillPrincipalOwner(r, request.OwnerID)
		if !ok {
			writeJSONError(w, "Freewill goal owner was rejected", http.StatusForbidden)
			return
		}
		decision, err := s.freewillGoals.Decide(r.Context(), ownerID, request.ProjectID, request.AgentID, parts[1], request.Decision)
		if err != nil {
			writeJSONError(w, "Freewill goal decision could not be recorded", http.StatusConflict)
			return
		}
		writeJSON(w, decision)
		return
	}
	if len(parts) == 3 && parts[0] == "proposals" && validMultiAgentRunID(parts[1]) && parts[2] == "progress" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request freewillGoalProgressRequest
		if err := decodeStrictJSON(w, r, s.maxRequestBytes, &request); err != nil {
			writeJSONError(w, "invalid Freewill goal progress check-in", http.StatusBadRequest)
			return
		}
		ownerID, ok := s.freewillPrincipalOwner(r, request.OwnerID)
		if !ok {
			writeJSONError(w, "Freewill goal owner was rejected", http.StatusForbidden)
			return
		}
		goal, err := s.freewillGoals.CheckIn(r.Context(), ownerID, request.ProjectID, request.AgentID, parts[1], request.GoalRevision, request.GoalID, request.RequestID, request.ChildRunID, request.OwnerSelfReport)
		if err != nil {
			writeJSONError(w, "Freewill goal progress check-in rejected", http.StatusConflict)
			return
		}
		writeJSON(w, goal)
		return
	}
	http.NotFound(w, r)
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return io.ErrUnexpectedEOF
	}
	return nil
}
