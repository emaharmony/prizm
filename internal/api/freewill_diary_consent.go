package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/freewill"
)

// LocalFreewillDiaryPrincipalCredential maps one header credential hash to a
// synthetic principal. The credential itself is never stored in this config.
type LocalFreewillDiaryPrincipalCredential struct {
	PrincipalID string `json:"principal_id" yaml:"principal_id"`
	TokenSHA256 string `json:"token_sha256" yaml:"token_sha256"`
}

// LocalFreewillDiaryPrincipalAuth is an opt-in, loopback-only POC identity map.
// Exactly two synthetic principals are supported; there is no client-set role.
type LocalFreewillDiaryPrincipalAuth struct {
	Enabled          bool                                    `json:"enabled" yaml:"enabled"`
	OwnerPrincipalID string                                  `json:"owner_principal_id" yaml:"owner_principal_id"`
	Credentials      []LocalFreewillDiaryPrincipalCredential `json:"credentials" yaml:"credentials"`
}

type freewillDiaryPrincipalContextKey struct{}

type FreewillDiaryShareGrant struct {
	GrantID                    string     `json:"grant_id"`
	RecipientPrincipalID       string     `json:"recipient_principal_id"`
	EntryID                    string     `json:"entry_id"`
	Revision                   int        `json:"revision"`
	ProjectID                  string     `json:"project_id"`
	WorkspaceID                string     `json:"workspace_id"`
	AgentID                    string     `json:"agent_id"`
	IntentionID                string     `json:"intention_id"`
	ParentRunID                string     `json:"parent_run_id"`
	EntrySHA256                string     `json:"entry_sha256"`
	GrantedAt                  time.Time  `json:"granted_at"`
	ExpiresAt                  time.Time  `json:"expires_at"`
	RevokedAt                  *time.Time `json:"revoked_at,omitempty"`
	RelationshipCleanupPending bool       `json:"relationship_cleanup_pending,omitempty"`
}

// FreewillDiaryShareReply is an attributed but unverified recipient comment.
// Callers must treat Text as untrusted evidence, not a fact or instruction.
type FreewillDiaryShareReply struct {
	ReplyID              string    `json:"reply_id"`
	RequestID            string    `json:"request_id"`
	GrantID              string    `json:"grant_id"`
	RecipientPrincipalID string    `json:"recipient_principal_id"`
	EntryID              string    `json:"entry_id"`
	Revision             int       `json:"revision"`
	ProjectID            string    `json:"project_id"`
	WorkspaceID          string    `json:"workspace_id"`
	AgentID              string    `json:"agent_id"`
	IntentionID          string    `json:"intention_id"`
	ParentRunID          string    `json:"parent_run_id"`
	EntrySHA256          string    `json:"entry_sha256"`
	Text                 string    `json:"text"`
	CreatedAt            time.Time `json:"created_at"`
}

// FreewillDiaryConsentController owns durable consent invariants and metadata-only audit.
type FreewillDiaryConsentController interface {
	CreateDiaryShare(context.Context, string, string, FreewillDiaryAnnotationEntry, time.Time, time.Time) (FreewillDiaryShareGrant, error)
	RevokeDiaryShare(context.Context, string, string, time.Time) (FreewillDiaryShareGrant, error)
	ReadDiaryShare(context.Context, string, string, time.Time) (FreewillDiaryShareGrant, FreewillDiaryAnnotationEntry, error)
	RecordDiaryConsentDecision(context.Context, string, string, string, string, int, string, time.Time) error
	CreateDiaryShareReply(context.Context, string, FreewillDiaryShareGrant, string, string, time.Time) (FreewillDiaryShareReply, error)
	ReadDiaryShareReplies(context.Context, string, string, time.Time) ([]FreewillDiaryShareReply, error)
}

type freewillDiaryShareReplyRequest struct {
	RequestID   string `json:"request_id"`
	GrantID     string `json:"grant_id"`
	EntryID     string `json:"entry_id"`
	Revision    int    `json:"revision"`
	EntrySHA256 string `json:"entry_sha256"`
	ProjectID   string `json:"project_id"`
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	IntentionID string `json:"intention_id"`
	ParentRunID string `json:"parent_run_id"`
	Text        string `json:"text"`
}

type freewillDiaryShareRequest struct {
	RecipientPrincipalID string    `json:"recipient_principal_id"`
	ProjectID            string    `json:"project_id"`
	Workspace            string    `json:"workspace"`
	WorkspaceID          string    `json:"workspace_id"`
	AgentID              string    `json:"agent_id"`
	IntentionID          string    `json:"intention_id"`
	ParentRunID          string    `json:"parent_run_id"`
	EntryID              string    `json:"entry_id"`
	ExpectedRevision     int       `json:"expected_revision"`
	ExpiresAt            time.Time `json:"expires_at"`
}

func validateLocalDiaryPrincipalAuth(addr, serviceToken string, cfg LocalFreewillDiaryPrincipalAuth, annotations FreewillDiaryAnnotationController, consent FreewillDiaryConsentController) error {
	if !cfg.Enabled || !validSyntheticDiaryPrincipal(cfg.OwnerPrincipalID) || len(cfg.Credentials) != 2 || annotations == nil || consent == nil {
		return errors.New("enabled local diary principal mode requires an owner, two synthetic credentials, and diary controllers")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("local diary principal mode requires a loopback listener address")
	}
	listenerIP := net.ParseIP(strings.Trim(host, "[]"))
	if listenerIP == nil || !listenerIP.IsLoopback() {
		return errors.New("local diary principal mode requires an IP loopback listener")
	}
	seenIDs := make(map[string]bool, 2)
	var seenHashes [][]byte
	ownerFound := false
	serviceDigest := sha256.Sum256([]byte(serviceToken))
	for _, credential := range cfg.Credentials {
		if !validSyntheticDiaryPrincipal(credential.PrincipalID) || seenIDs[credential.PrincipalID] {
			return errors.New("local diary principal mode accepts exactly two distinct synthetic identities")
		}
		hash, err := hex.DecodeString(credential.TokenSHA256)
		if err != nil || len(hash) != sha256.Size {
			return errors.New("local diary principal credential must be a unique SHA-256 digest")
		}
		for _, prior := range seenHashes {
			if subtle.ConstantTimeCompare(hash, prior) == 1 {
				return errors.New("local diary principal credential must be a unique SHA-256 digest")
			}
		}
		if serviceToken != "" && subtle.ConstantTimeCompare(hash, serviceDigest[:]) == 1 {
			return errors.New("service bearer credential cannot also be a diary principal credential")
		}
		seenIDs[credential.PrincipalID] = true
		seenHashes = append(seenHashes, hash)
		if credential.PrincipalID == cfg.OwnerPrincipalID {
			ownerFound = true
		}
	}
	if !ownerFound {
		return errors.New("local diary owner must be one of the two configured synthetic principals")
	}
	return nil
}

func validSyntheticDiaryPrincipal(value string) bool {
	return strings.HasPrefix(value, "synthetic-") && len(value) <= 128 && value == strings.TrimSpace(value)
}

func isFreewillDiaryAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/freewill/diary/")
}

// isFreewillPrincipalBoundAPIPath selects owner-sensitive Freewill APIs that
// use the local synthetic-principal credential when opt-in mode is enabled.
// Run approval APIs intentionally remain on their existing service auth path.
func isFreewillPrincipalBoundAPIPath(path string) bool {
	return isFreewillDiaryAPIPath(path) ||
		path == "/api/v1/freewill/relationships" ||
		strings.HasPrefix(path, "/api/v1/freewill/relationships/") ||
		strings.HasPrefix(path, "/api/v1/freewill/preferences/") ||
		strings.HasPrefix(path, "/api/v1/freewill/goals/") ||
		strings.HasPrefix(path, "/api/v1/freewill/promises/") ||
		strings.HasPrefix(path, "/api/v1/freewill/strategy-experiments/")
}

// freewillPrincipalOwner resolves the immutable owner for goal and promise
// handlers. In opt-in mode these records are owner-private; a configured
// recipient principal has no goal or promise sharing grant.
func (s *Server) freewillPrincipalOwner(r *http.Request, requested string) (string, bool) {
	if s == nil || s.localDiaryPrincipalAuth == nil {
		return requested, true
	}
	principal := s.freewillDiaryPrincipal(r)
	if principal == "" || principal != s.localDiaryPrincipalAuth.OwnerPrincipalID {
		return "", false
	}
	if requested != "" && requested != principal {
		return "", false
	}
	return principal, true
}

func (s *Server) freewillPrincipalQueryHasDuplicates(r *http.Request, keys ...string) bool {
	if s == nil || s.localDiaryPrincipalAuth == nil {
		return false
	}
	query := r.URL.Query()
	for _, key := range keys {
		if len(query[key]) > 1 {
			return true
		}
	}
	return false
}

func diaryRouteEntryID(path string) string {
	if strings.HasPrefix(path, "/api/v1/freewill/diary/entries/") {
		id := strings.Trim(strings.TrimPrefix(path, "/api/v1/freewill/diary/entries/"), "/")
		if !strings.Contains(id, "/") {
			return id
		}
	}
	return ""
}

func (s *Server) localFreewillDiaryPrincipal(r *http.Request) (string, bool) {
	if s == nil || s.localDiaryPrincipalAuth == nil {
		return "", false
	}
	if len(r.Header.Values("Authorization")) != 1 {
		return "", false
	}
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if token == "" {
		return "", false
	}
	digest := sha256.Sum256([]byte(token))
	principal, matched := "", 0
	for _, credential := range s.localDiaryPrincipalAuth.Credentials {
		configured, err := hex.DecodeString(credential.TokenSHA256)
		if err != nil || len(configured) != sha256.Size {
			continue
		}
		match := subtle.ConstantTimeCompare(digest[:], configured)
		if match == 1 {
			principal = credential.PrincipalID
			matched = 1
		}
	}
	return principal, matched == 1
}

func (s *Server) freewillDiaryPrincipal(r *http.Request) string {
	principal, _ := r.Context().Value(freewillDiaryPrincipalContextKey{}).(string)
	return principal
}

func (s *Server) localDiaryRecipientPrincipal() string {
	if s.localDiaryPrincipalAuth == nil {
		return ""
	}
	for _, credential := range s.localDiaryPrincipalAuth.Credentials {
		if credential.PrincipalID != s.localDiaryPrincipalAuth.OwnerPrincipalID {
			return credential.PrincipalID
		}
	}
	return ""
}

func (s *Server) auditDiaryDecision(r *http.Request, action, grantID, entryID string, revision int, decision string) bool {
	if s.localDiaryPrincipalAuth == nil {
		return true
	}
	if s.freewillDiaryConsent == nil {
		return false
	}
	return s.freewillDiaryConsent.RecordDiaryConsentDecision(r.Context(), s.freewillDiaryPrincipal(r), action, grantID, entryID, revision, decision, time.Now().UTC()) == nil
}

func (s *Server) requireDiaryAudit(w http.ResponseWriter, r *http.Request, action, grantID, entryID string, revision int, decision string) bool {
	if s.auditDiaryDecision(r, action, grantID, entryID, revision, decision) {
		return true
	}
	writeJSONError(w, "Freewill diary audit is unavailable", http.StatusServiceUnavailable)
	return false
}

func (s *Server) handleFreewillDiaryShareCollection(w http.ResponseWriter, r *http.Request) {
	if s.freewillDiaryConsent == nil || s.freewillDiaryAnnotations == nil || s.localDiaryPrincipalAuth == nil {
		writeJSONError(w, "Freewill diary sharing is not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	principal := s.freewillDiaryPrincipal(r)
	if principal != s.localDiaryPrincipalAuth.OwnerPrincipalID {
		if !s.requireDiaryAudit(w, r, "share.grant", "", "", 0, "denied") {
			return
		}
		writeJSONError(w, "diary share was rejected", http.StatusForbidden)
		return
	}
	var req freewillDiaryShareRequest
	if err := decodeStrictJSON(w, r, s.maxRequestBytes, &req); err != nil {
		if !s.requireDiaryAudit(w, r, "share.grant", "", "", 0, "denied") {
			return
		}
		writeJSONError(w, "invalid diary share request", http.StatusBadRequest)
		return
	}
	entryID := req.EntryID
	if req.RecipientPrincipalID != s.localDiaryRecipientPrincipal() || req.ExpectedRevision < 1 || req.EntryID == "" || !req.ExpiresAt.After(time.Now().UTC()) {
		if !s.requireDiaryAudit(w, r, "share.grant", "", entryID, req.ExpectedRevision, "denied") {
			return
		}
		writeJSONError(w, "diary share was rejected", http.StatusConflict)
		return
	}
	now := time.Now().UTC()
	entry, err := s.freewillDiaryAnnotations.ReadDiaryAnnotations(r.Context(), principal, req.ProjectID, req.Workspace, req.AgentID, req.IntentionID, req.ParentRunID, req.EntryID)
	if err != nil || entry.EntryID != req.EntryID || entry.ProjectID != req.ProjectID || entry.WorkspaceID != req.WorkspaceID || entry.AgentID != req.AgentID || entry.IntentionID != req.IntentionID || entry.ParentRunID != req.ParentRunID || entry.Revision != req.ExpectedRevision {
		if !s.requireDiaryAudit(w, r, "share.grant", "", entryID, req.ExpectedRevision, "denied") {
			return
		}
		writeJSONError(w, "diary entry revision is unavailable", http.StatusConflict)
		return
	}
	grant, err := s.freewillDiaryConsent.CreateDiaryShare(r.Context(), principal, req.RecipientPrincipalID, entry, req.ExpiresAt.UTC(), now)
	if err != nil {
		if errors.Is(err, freewill.ErrDiaryConsentAuditUnavailable) {
			writeJSONError(w, "Freewill diary audit is unavailable", http.StatusServiceUnavailable)
			return
		}
		writeJSONError(w, "diary share was rejected", http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"grant": grant})
}

func (s *Server) handleFreewillDiaryShare(w http.ResponseWriter, r *http.Request) {
	if s.freewillDiaryConsent == nil || s.localDiaryPrincipalAuth == nil {
		writeJSONError(w, "Freewill diary sharing is not configured", http.StatusServiceUnavailable)
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/freewill/diary/shares/"), "/")
	parts := strings.Split(path, "/")
	grantID := parts[0]
	if grantID == "" || len(parts) > 2 || (len(parts) == 2 && parts[1] != "replies") {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 {
		s.handleFreewillDiaryShareReplies(w, r, grantID)
		return
	}
	principal := s.freewillDiaryPrincipal(r)
	now := time.Now().UTC()
	switch r.Method {
	case http.MethodGet:
		grant, entry, err := s.freewillDiaryConsent.ReadDiaryShare(r.Context(), principal, grantID, now)
		if err != nil {
			if errors.Is(err, freewill.ErrDiaryConsentAuditUnavailable) {
				writeJSONError(w, "Freewill diary audit is unavailable", http.StatusServiceUnavailable)
				return
			}
			writeJSONError(w, "diary share is unavailable", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"grant": grant, "entry": entry})
	case http.MethodDelete:
		grant, err := s.freewillDiaryConsent.RevokeDiaryShare(r.Context(), principal, grantID, now)
		if err != nil {
			if errors.Is(err, freewill.ErrDiaryConsentAuditUnavailable) {
				writeJSONError(w, "Freewill diary audit is unavailable", http.StatusServiceUnavailable)
				return
			}
			writeJSONError(w, "diary share is unavailable", http.StatusNotFound)
			return
		}
		if grant.RelationshipCleanupPending {
			w.WriteHeader(http.StatusAccepted)
			writeJSON(w, map[string]any{"grant": grant, "cleanup_pending": true})
			return
		}
		writeJSON(w, map[string]any{"grant": grant})
	default:
		w.Header().Set("Allow", "GET, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleFreewillDiaryShareReplies(w http.ResponseWriter, r *http.Request, grantID string) {
	principal := s.freewillDiaryPrincipal(r)
	switch r.Method {
	case http.MethodPost:
		var req freewillDiaryShareReplyRequest
		if err := decodeStrictJSON(w, r, s.maxRequestBytes, &req); err != nil {
			if !s.requireDiaryAudit(w, r, "share.reply", grantID, "", 0, "denied") {
				return
			}
			writeJSONError(w, "invalid diary share reply", http.StatusBadRequest)
			return
		}
		if req.GrantID != grantID {
			if !s.requireDiaryAudit(w, r, "share.reply", grantID, req.EntryID, req.Revision, "denied") {
				return
			}
			writeJSONError(w, "diary share reply was rejected", http.StatusConflict)
			return
		}
		grant := FreewillDiaryShareGrant{GrantID: req.GrantID, EntryID: req.EntryID, Revision: req.Revision, EntrySHA256: req.EntrySHA256,
			ProjectID: req.ProjectID, WorkspaceID: req.WorkspaceID, AgentID: req.AgentID, IntentionID: req.IntentionID, ParentRunID: req.ParentRunID}
		reply, err := s.freewillDiaryConsent.CreateDiaryShareReply(r.Context(), principal, grant, req.RequestID, req.Text, time.Now().UTC())
		if err != nil {
			if errors.Is(err, freewill.ErrDiaryConsentAuditUnavailable) {
				writeJSONError(w, "Freewill diary audit is unavailable", http.StatusServiceUnavailable)
				return
			}
			writeJSONError(w, "diary share reply was rejected", http.StatusConflict)
			return
		}
		writeJSON(w, map[string]any{"reply": reply})
	case http.MethodGet:
		replies, err := s.freewillDiaryConsent.ReadDiaryShareReplies(r.Context(), principal, grantID, time.Now().UTC())
		if err != nil {
			if errors.Is(err, freewill.ErrDiaryConsentAuditUnavailable) {
				writeJSONError(w, "Freewill diary audit is unavailable", http.StatusServiceUnavailable)
				return
			}
			writeJSONError(w, "diary share replies are unavailable", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"replies": replies})
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
