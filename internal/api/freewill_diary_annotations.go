package api

import (
	"context"
	"net/http"
	"strings"
)

// FreewillDiaryAnnotationEntry is a sanitized identity for the immutable source
// diary record and its current append-only revision.
type FreewillDiaryAnnotationEntry struct {
	EntryID      string                    `json:"entry_id"`
	Revision     int                       `json:"revision"`
	ProjectID    string                    `json:"project_id"`
	WorkspaceID  string                    `json:"workspace_id"`
	AgentID      string                    `json:"agent_id"`
	IntentionID  string                    `json:"intention_id"`
	ParentRunID  string                    `json:"parent_run_id"`
	OriginalText string                    `json:"original_text"`
	Annotations  []FreewillDiaryAnnotation `json:"annotations"`
}

// FreewillDiaryAnnotation is one owner-authored immutable annotation version.
type FreewillDiaryAnnotation struct {
	AnnotationID string `json:"annotation_id"`
	Revision     int    `json:"revision"`
	Previous     int    `json:"previous_revision"`
	Text         string `json:"text"`
	TextSHA256   string `json:"text_sha256"`
	RecordedAt   string `json:"recorded_at"`
}

// FreewillDiaryAnnotationController accesses an exact configured local diary.
// It does not call a model or grant workflow authority.
type FreewillDiaryAnnotationController interface {
	ReadDiaryAnnotations(context.Context, string, string, string, string, string, string, string) (FreewillDiaryAnnotationEntry, error)
	AppendDiaryAnnotation(context.Context, string, string, string, string, string, string, string, int, string) (FreewillDiaryAnnotationEntry, error)
}

type freewillDiaryAnnotationRequest struct {
	OwnerID          string `json:"owner_id"`
	ProjectID        string `json:"project_id"`
	Workspace        string `json:"workspace"`
	AgentID          string `json:"agent_id"`
	IntentionID      string `json:"intention_id"`
	ParentRunID      string `json:"parent_run_id"`
	ExpectedRevision int    `json:"expected_revision"`
	Text             string `json:"text"`
}

func (s *Server) handleFreewillDiaryAnnotations(w http.ResponseWriter, r *http.Request) {
	if s.freewillDiaryAnnotations == nil {
		writeJSONError(w, "Freewill diary annotations are not configured", http.StatusServiceUnavailable)
		return
	}
	entryID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/freewill/diary/entries/"), "/")
	if entryID == "" || strings.Contains(entryID, "/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		ownerID := r.URL.Query().Get("owner_id")
		if s.localDiaryPrincipalAuth != nil {
			principal := s.freewillDiaryPrincipal(r)
			if principal != s.localDiaryPrincipalAuth.OwnerPrincipalID || (ownerID != "" && ownerID != principal) {
				if !s.requireDiaryAudit(w, r, "entry.read", "", entryID, 0, "denied") {
					return
				}
				writeJSONError(w, "Freewill diary entry is unavailable", http.StatusForbidden)
				return
			}
			ownerID = principal
		}
		entry, err := s.freewillDiaryAnnotations.ReadDiaryAnnotations(r.Context(), ownerID, r.URL.Query().Get("project_id"), r.URL.Query().Get("workspace"), r.URL.Query().Get("agent_id"), r.URL.Query().Get("intention_id"), r.URL.Query().Get("parent_run_id"), entryID)
		if err != nil {
			if !s.requireDiaryAudit(w, r, "entry.read", "", entryID, 0, "denied") {
				return
			}
			writeJSONError(w, "Freewill diary entry is unavailable", http.StatusNotFound)
			return
		}
		if s.localDiaryPrincipalAuth != nil {
			if !s.requireDiaryAudit(w, r, "entry.read", "", entryID, entry.Revision, "allowed") {
				return
			}
		}
		writeJSON(w, entry)
	case http.MethodPost:
		var req freewillDiaryAnnotationRequest
		if err := decodeStrictJSON(w, r, s.maxRequestBytes, &req); err != nil {
			if !s.requireDiaryAudit(w, r, "entry.annotate", "", entryID, 0, "denied") {
				return
			}
			writeJSONError(w, "invalid Freewill diary annotation request", http.StatusBadRequest)
			return
		}
		if s.localDiaryPrincipalAuth != nil {
			principal := s.freewillDiaryPrincipal(r)
			if principal != s.localDiaryPrincipalAuth.OwnerPrincipalID || (req.OwnerID != "" && req.OwnerID != principal) {
				if !s.requireDiaryAudit(w, r, "entry.annotate", "", entryID, req.ExpectedRevision, "denied") {
					return
				}
				writeJSONError(w, "Freewill diary annotation was rejected", http.StatusForbidden)
				return
			}
			req.OwnerID = principal
			current, readErr := s.freewillDiaryAnnotations.ReadDiaryAnnotations(r.Context(), req.OwnerID, req.ProjectID, req.Workspace, req.AgentID, req.IntentionID, req.ParentRunID, entryID)
			if readErr != nil || current.Revision != req.ExpectedRevision {
				if !s.requireDiaryAudit(w, r, "entry.annotate", "", entryID, req.ExpectedRevision, "denied") {
					return
				}
				writeJSONError(w, "Freewill diary annotation was rejected", http.StatusConflict)
				return
			}
			if !s.requireDiaryAudit(w, r, "entry.annotate", "", entryID, req.ExpectedRevision, "allowed") {
				return
			}
		}
		entry, err := s.freewillDiaryAnnotations.AppendDiaryAnnotation(r.Context(), req.OwnerID, req.ProjectID, req.Workspace, req.AgentID, req.IntentionID, req.ParentRunID, entryID, req.ExpectedRevision, req.Text)
		if err != nil {
			if !s.requireDiaryAudit(w, r, "entry.annotate", "", entryID, req.ExpectedRevision, "denied") {
				return
			}
			writeJSONError(w, "Freewill diary annotation was rejected", http.StatusConflict)
			return
		}
		writeJSON(w, entry)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
