package api

import (
	"context"
	"net/http"
	"strings"
)

type FreewillPromiseRevision struct {
	PromiseID   string `json:"promise_id"`
	Revision    int    `json:"revision"`
	OwnerID     string `json:"owner_id"`
	ProjectID   string `json:"project_id"`
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	Scope       string `json:"scope"`
	Deadline    string `json:"deadline"`
	RecordedAt  string `json:"recorded_at"`
}

type FreewillPromiseProposal struct {
	ProposalID       string `json:"proposal_id"`
	PromiseID        string `json:"promise_id"`
	RequestID        string `json:"request_id"`
	Proposer         string `json:"proposer"`
	ExpectedRevision int    `json:"expected_revision"`
	ProposedScope    string `json:"proposed_scope"`
	ProposedDeadline string `json:"proposed_deadline"`
	ImpactHypothesis string `json:"impact_hypothesis"`
	Status           string `json:"status"`
	RecordedAt       string `json:"recorded_at"`
}

type FreewillPromiseDecision struct {
	DecisionID string `json:"decision_id"`
	ProposalID string `json:"proposal_id"`
	Decision   string `json:"decision"`
	RecordedAt string `json:"recorded_at"`
}

type FreewillPromiseConfirmation struct {
	Current   FreewillPromiseRevision   `json:"current"`
	Proposals []FreewillPromiseProposal `json:"proposals"`
	Decisions []FreewillPromiseDecision `json:"decisions"`
	History   []FreewillPromiseRevision `json:"history"`
}

type FreewillPromiseConfirmationController interface {
	ReadPromise(context.Context, string, string, string, string, string) (FreewillPromiseConfirmation, error)
	ProposePromiseRevision(context.Context, string, string, string, string, string, string, int, string, string, string) (FreewillPromiseConfirmation, error)
	DecidePromiseRevision(context.Context, string, string, string, string, string, string, string) (FreewillPromiseConfirmation, error)
}

type freewillPromiseProposalRequest struct {
	OwnerID          string `json:"owner_id"`
	ProjectID        string `json:"project_id"`
	Workspace        string `json:"workspace"`
	AgentID          string `json:"agent_id"`
	RequestID        string `json:"request_id"`
	ExpectedRevision int    `json:"expected_revision"`
	Scope            string `json:"scope"`
	Deadline         string `json:"deadline"`
	ImpactHypothesis string `json:"impact_hypothesis"`
}

type freewillPromiseDecisionRequest struct {
	OwnerID    string `json:"owner_id"`
	ProjectID  string `json:"project_id"`
	Workspace  string `json:"workspace"`
	AgentID    string `json:"agent_id"`
	ProposalID string `json:"proposal_id"`
	Decision   string `json:"decision"`
}

func (s *Server) handleFreewillPromiseConfirmation(w http.ResponseWriter, r *http.Request) {
	if s.freewillPromises == nil {
		writeJSONError(w, "Freewill promise confirmation is not configured", http.StatusServiceUnavailable)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/freewill/promises/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" || len(parts) > 3 {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	switch r.Method {
	case http.MethodGet:
		if len(parts) != 1 {
			http.NotFound(w, r)
			return
		}
		if s.freewillPrincipalQueryHasDuplicates(r, "owner_id", "project_id", "workspace", "agent_id") {
			writeJSONError(w, "Freewill promise owner was rejected", http.StatusBadRequest)
			return
		}
		ownerValues := r.URL.Query()["owner_id"]
		requestedOwner := ""
		if len(ownerValues) == 1 {
			requestedOwner = ownerValues[0]
		}
		ownerID, ok := s.freewillPrincipalOwner(r, requestedOwner)
		if !ok {
			writeJSONError(w, "Freewill promise owner was rejected", http.StatusForbidden)
			return
		}
		v, err := s.freewillPromises.ReadPromise(r.Context(), ownerID, r.URL.Query().Get("project_id"), r.URL.Query().Get("workspace"), r.URL.Query().Get("agent_id"), id)
		if err != nil {
			writeJSONError(w, "Freewill promise is unavailable", http.StatusNotFound)
			return
		}
		writeJSON(w, v)
	case http.MethodPost:
		if len(parts) == 2 && parts[1] == "proposals" {
			var q freewillPromiseProposalRequest
			if decodeStrictJSON(w, r, s.maxRequestBytes, &q) != nil {
				writeJSONError(w, "invalid Freewill promise proposal", http.StatusBadRequest)
				return
			}
			ownerID, ok := s.freewillPrincipalOwner(r, q.OwnerID)
			if !ok {
				writeJSONError(w, "Freewill promise owner was rejected", http.StatusForbidden)
				return
			}
			v, err := s.freewillPromises.ProposePromiseRevision(r.Context(), ownerID, q.ProjectID, q.Workspace, q.AgentID, id, q.RequestID, q.ExpectedRevision, q.Scope, q.Deadline, q.ImpactHypothesis)
			if err != nil {
				writeJSONError(w, "Freewill promise proposal was rejected", http.StatusConflict)
				return
			}
			writeJSON(w, v)
			return
		}
		if len(parts) != 3 || parts[1] != "decisions" {
			http.NotFound(w, r)
			return
		}
		var q freewillPromiseDecisionRequest
		if decodeStrictJSON(w, r, s.maxRequestBytes, &q) != nil {
			writeJSONError(w, "invalid Freewill promise decision", http.StatusBadRequest)
			return
		}
		if q.ProposalID != parts[2] {
			writeJSONError(w, "proposal path does not match request", http.StatusConflict)
			return
		}
		ownerID, ok := s.freewillPrincipalOwner(r, q.OwnerID)
		if !ok {
			writeJSONError(w, "Freewill promise owner was rejected", http.StatusForbidden)
			return
		}
		v, err := s.freewillPromises.DecidePromiseRevision(r.Context(), ownerID, q.ProjectID, q.Workspace, q.AgentID, id, q.ProposalID, q.Decision)
		if err != nil {
			writeJSONError(w, "Freewill promise decision was rejected", http.StatusConflict)
			return
		}
		writeJSON(w, v)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
