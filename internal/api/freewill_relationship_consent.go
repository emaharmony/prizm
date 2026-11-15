package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/freewill"
)

type FreewillRelationshipObservation struct {
	RelationshipID string    `json:"relationship_id"`
	AuthorID       string    `json:"author_id"`
	Hypothesis     string    `json:"hypothesis"`
	SourceRef      string    `json:"source_ref"`
	CreatedAt      time.Time `json:"created_at"`
	Label          string    `json:"label"`
}

type FreewillRelationshipController interface {
	ProposeRelationship(context.Context, string, string, string, string, time.Time) (string, error)
	ConsentRelationship(context.Context, string, string, time.Time) error
	WriteRelationshipHypothesis(context.Context, string, string, string, string, string, time.Time) error
	ReadRelationshipHypothesis(context.Context, string, string, time.Time) (FreewillRelationshipObservation, error)
	WithdrawRelationship(context.Context, string, string, time.Time) error
}

type FreewillRelationshipAssessmentController interface {
	ReadRelationshipAssessments(context.Context, string, string, time.Time) ([]freewill.RelationshipAssessment, error)
}

type FreewillRelationshipChallengeController interface {
	ChallengeRelationshipOpinion(context.Context, string, string, string, string, int, string, string, time.Time) (freewill.RelationshipAssessment, error)
	ReadRelationshipChallenges(context.Context, string, string, string, time.Time) ([]freewill.RelationshipChallenge, error)
}

type freewillRelationshipProposalRequest struct {
	OtherPrincipalID string `json:"other_principal_id"`
	GrantID          string `json:"grant_id"`
	ReplyID          string `json:"reply_id"`
}
type freewillRelationshipObservationRequest struct {
	Hypothesis string `json:"hypothesis"`
	GrantID    string `json:"grant_id"`
	ReplyID    string `json:"reply_id"`
}
type freewillRelationshipChallengeRequest struct {
	ChallengeID      string `json:"challenge_id"`
	ExpectedRevision int    `json:"expected_revision"`
	Rationale        string `json:"rationale"`
	Evidence         string `json:"evidence"`
}

func (s *Server) handleFreewillRelationshipCollection(w http.ResponseWriter, r *http.Request) {
	if s.freewillRelationships == nil {
		writeJSONError(w, "Freewill relationship consent is not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req freewillRelationshipProposalRequest
	if err := decodeStrictJSON(w, r, s.maxRequestBytes, &req); err != nil {
		writeJSONError(w, "invalid relationship consent request", http.StatusBadRequest)
		return
	}
	id, err := s.freewillRelationships.ProposeRelationship(r.Context(), s.freewillDiaryPrincipal(r), req.OtherPrincipalID, req.GrantID, req.ReplyID, time.Now().UTC())
	if err != nil {
		writeJSONError(w, "relationship consent was rejected", http.StatusConflict)
		return
	}
	writeJSON(w, map[string]string{"relationship_id": id})
}

func (s *Server) handleFreewillRelationship(w http.ResponseWriter, r *http.Request) {
	if s.freewillRelationships == nil {
		writeJSONError(w, "Freewill relationship consent is not configured", http.StatusServiceUnavailable)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/freewill/relationships/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || parts[0] == "" || len(parts) > 4 {
		http.NotFound(w, r)
		return
	}
	id, actor := parts[0], s.freewillDiaryPrincipal(r)
	if len(parts) >= 2 {
		switch parts[1] {
		case "consent":
			if len(parts) != 2 {
				http.NotFound(w, r)
				return
			}
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", "POST")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if err := s.freewillRelationships.ConsentRelationship(r.Context(), actor, id, time.Now().UTC()); err != nil {
				if errors.Is(err, freewill.ErrRelationshipPurgePending) {
					writeRelationshipPurgePending(w, id)
					return
				}
				writeJSONError(w, "relationship consent was rejected", http.StatusConflict)
				return
			}
			writeJSON(w, map[string]string{"relationship_id": id, "status": "consented"})
			return
		case "observations":
			if len(parts) != 2 {
				http.NotFound(w, r)
				return
			}
			switch r.Method {
			case http.MethodPost:
				var req freewillRelationshipObservationRequest
				if err := decodeStrictJSON(w, r, s.maxRequestBytes, &req); err != nil {
					writeJSONError(w, "invalid relationship observation", http.StatusBadRequest)
					return
				}
				if err := s.freewillRelationships.WriteRelationshipHypothesis(r.Context(), actor, id, req.Hypothesis, req.GrantID, req.ReplyID, time.Now().UTC()); err != nil {
					if errors.Is(err, freewill.ErrRelationshipPurgePending) {
						writeRelationshipPurgePending(w, id)
						return
					}
					writeJSONError(w, "relationship observation was rejected", http.StatusConflict)
					return
				}
				writeJSON(w, map[string]string{"relationship_id": id, "label": "hypothesis", "status": "stored"})
				return
			case http.MethodGet:
				observation, err := s.freewillRelationships.ReadRelationshipHypothesis(r.Context(), actor, id, time.Now().UTC())
				if err != nil {
					if errors.Is(err, freewill.ErrRelationshipPurgePending) {
						writeRelationshipPurgePending(w, id)
						return
					}
					writeJSONError(w, "relationship observation is unavailable", http.StatusNotFound)
					return
				}
				writeJSON(w, map[string]FreewillRelationshipObservation{"observation": {RelationshipID: observation.RelationshipID, AuthorID: observation.AuthorID, Hypothesis: observation.Hypothesis, SourceRef: observation.SourceRef, CreatedAt: observation.CreatedAt, Label: observation.Label}})
				return
			default:
				w.Header().Set("Allow", "GET, POST")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
		case "assessments":
			if len(parts) == 4 && parts[2] != "" && parts[3] == "challenges" {
				controller, ok := s.freewillRelationships.(FreewillRelationshipChallengeController)
				if !ok {
					writeJSONError(w, "relationship challenges are not configured", http.StatusServiceUnavailable)
					return
				}
				switch r.Method {
				case http.MethodPost:
					var req freewillRelationshipChallengeRequest
					if err := decodeStrictJSON(w, r, s.maxRequestBytes, &req); err != nil {
						writeJSONError(w, "invalid relationship challenge", http.StatusBadRequest)
						return
					}
					item, err := controller.ChallengeRelationshipOpinion(r.Context(), actor, id, parts[2], req.ChallengeID, req.ExpectedRevision, req.Rationale, req.Evidence, time.Now().UTC())
					if err != nil {
						writeJSONError(w, "relationship challenge was rejected", http.StatusConflict)
						return
					}
					writeJSON(w, map[string]any{"revision": item, "status": "recorded", "challenge_verified": false})
					return
				case http.MethodGet:
					items, err := controller.ReadRelationshipChallenges(r.Context(), actor, id, parts[2], time.Now().UTC())
					if err != nil {
						if errors.Is(err, freewill.ErrRelationshipPurgePending) {
							writeRelationshipPurgePending(w, id)
							return
						}
						writeJSONError(w, "relationship challenges are unavailable", http.StatusNotFound)
						return
					}
					if items == nil {
						items = []freewill.RelationshipChallenge{}
					}
					writeJSON(w, map[string]any{"challenges": items})
					return
				default:
					w.Header().Set("Allow", "GET, POST")
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
					return
				}
			}
			if len(parts) != 2 {
				http.NotFound(w, r)
				return
			}
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			reader, ok := s.freewillRelationships.(FreewillRelationshipAssessmentController)
			if !ok {
				writeJSONError(w, "relationship assessment is not configured", http.StatusServiceUnavailable)
				return
			}
			items, err := reader.ReadRelationshipAssessments(r.Context(), actor, id, time.Now().UTC())
			if err != nil {
				if errors.Is(err, freewill.ErrRelationshipPurgePending) {
					writeRelationshipPurgePending(w, id)
					return
				}
				writeJSONError(w, "relationship assessments are unavailable", http.StatusNotFound)
				return
			}
			if items == nil {
				items = []freewill.RelationshipAssessment{}
			}
			writeJSON(w, map[string]any{"assessments": items})
			return
		default:
			http.NotFound(w, r)
			return
		}
	}
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.freewillRelationships.WithdrawRelationship(r.Context(), actor, id, time.Now().UTC()); err != nil {
		if errors.Is(err, freewill.ErrRelationshipPurgePending) {
			writeRelationshipPurgePending(w, id)
			return
		}
		if errors.Is(err, freewill.ErrRelationshipUnavailable) {
			writeJSONError(w, "relationship is unavailable", http.StatusNotFound)
		} else {
			writeJSONError(w, "relationship withdrawal failed", http.StatusServiceUnavailable)
		}
		return
	}
	writeJSON(w, map[string]string{"relationship_id": id, "status": "withdrawn"})
}

func writeRelationshipPurgePending(w http.ResponseWriter, id string) {
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"relationship_id": id, "status": "withdrawn", "cleanup_pending": true,
		"message": "relationship content is denied and deleted from SQLite; WAL checkpoint cleanup will be retried"})
}
