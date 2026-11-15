package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// FreewillPreferenceChallengeController exposes only the bounded owner-scoped
// challenge dialogue. The implementation owns citation and revision checks.
type FreewillPreferenceChallengeController interface {
	ChallengePreference(context.Context, string, string, string, string, string, string, string, string, string, time.Time) (json.RawMessage, json.RawMessage, error)
	ReadPreferenceChallenges(context.Context, string, string, string, string, string) ([]json.RawMessage, error)
	ReadPreferenceEvidence(context.Context, string, string, string, string, string) (any, error)
	WithdrawPreferenceEvidence(context.Context, string, string, string, string, time.Time) (string, error)
}

type freewillPreferenceChallengeRequest struct {
	OwnerID          string `json:"owner_id"`
	ProjectID        string `json:"project_id"`
	Workspace        string `json:"workspace"`
	AgentID          string `json:"agent_id"`
	HypothesisID     string `json:"hypothesis_id"`
	ExpectedRevision string `json:"expected_revision"`
	ChallengeID      string `json:"challenge_id"`
	SourceID         string `json:"source_id"`
	Rationale        string `json:"rationale"`
}

type freewillPreferenceWithdrawalRequest struct {
	OwnerID   string `json:"owner_id"`
	ProjectID string `json:"project_id"`
	Workspace string `json:"workspace"`
	AgentID   string `json:"agent_id"`
}

func (s *Server) handleFreewillPreferenceChallenges(w http.ResponseWriter, r *http.Request) {
	controller := s.freewillPreferenceChallenges
	if controller == nil {
		writeJSONError(w, "Freewill preference challenges are not configured", http.StatusServiceUnavailable)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/freewill/preferences/"), "/"), "/")
	if len(parts) == 1 && parts[0] == "withdrawal" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request freewillPreferenceWithdrawalRequest
		if err := decodeStrictJSON(w, r, s.maxRequestBytes, &request); err != nil {
			writeJSONError(w, "invalid preference withdrawal", http.StatusBadRequest)
			return
		}
		ownerID, ok := s.freewillPrincipalOwner(r, request.OwnerID)
		if !ok {
			writeJSONError(w, "preference owner was rejected", http.StatusForbidden)
			return
		}
		status, err := controller.WithdrawPreferenceEvidence(r.Context(), ownerID, request.ProjectID, request.Workspace, request.AgentID, time.Now().UTC())
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			writeJSON(w, map[string]string{"status": "pending", "error": "preference purge is pending"})
			return
		}
		writeJSON(w, map[string]string{"status": status})
		return
	}
	if len(parts) < 2 || parts[0] != "hypotheses" || parts[1] == "" || len(parts) > 3 || len(parts) == 3 && parts[2] != "challenges" && parts[2] != "evidence" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet && len(parts) == 3 {
		if s.freewillPrincipalQueryHasDuplicates(r, "owner_id", "project_id", "workspace", "agent_id") {
			writeJSONError(w, "preference owner was rejected", http.StatusBadRequest)
			return
		}
		ownerID, ok := s.freewillPrincipalOwner(r, r.URL.Query().Get("owner_id"))
		if !ok {
			writeJSONError(w, "preference owner was rejected", http.StatusForbidden)
			return
		}
		if parts[2] == "evidence" {
			view, err := controller.ReadPreferenceEvidence(r.Context(), ownerID, r.URL.Query().Get("project_id"), r.URL.Query().Get("workspace"), r.URL.Query().Get("agent_id"), parts[1])
			if err != nil {
				writeJSONError(w, "preference evidence is unavailable", http.StatusNotFound)
				return
			}
			writeJSON(w, view)
			return
		}
		rows, err := controller.ReadPreferenceChallenges(r.Context(), ownerID, r.URL.Query().Get("project_id"), r.URL.Query().Get("workspace"), r.URL.Query().Get("agent_id"), parts[1])
		if err != nil {
			writeJSONError(w, "preference challenges are unavailable", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"challenges": rows})
		return
	}
	if r.Method != http.MethodPost || len(parts) != 3 || parts[2] != "challenges" {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request freewillPreferenceChallengeRequest
	if err := decodeStrictJSON(w, r, s.maxRequestBytes, &request); err != nil {
		writeJSONError(w, "invalid preference challenge", http.StatusBadRequest)
		return
	}
	ownerID, ok := s.freewillPrincipalOwner(r, request.OwnerID)
	if !ok {
		writeJSONError(w, "preference owner was rejected", http.StatusForbidden)
		return
	}
	if request.HypothesisID != parts[1] {
		writeJSONError(w, "preference hypothesis was rejected", http.StatusConflict)
		return
	}
	challenge, revision, err := controller.ChallengePreference(r.Context(), ownerID, request.ProjectID, request.Workspace, request.AgentID, request.HypothesisID, request.ExpectedRevision, request.ChallengeID, request.SourceID, request.Rationale, time.Now().UTC())
	if err != nil {
		writeJSONError(w, "preference challenge was rejected", http.StatusConflict)
		return
	}
	writeJSON(w, map[string]json.RawMessage{"challenge": challenge, "revision": revision})
}
