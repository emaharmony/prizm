package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/remembrance"
	"github.com/emaharmony/prizm/internal/session"
)

// trustedPromptMemoryScope derives every prompt-memory authority field from
// serve-owned configuration and identities. Prompt text never supplies scope.
func (cc *conversationContext) trustedPromptMemoryScope(agentCfg *orchestrator.AgentConfig, runID, ownerID string, sess *session.Session) (memory.Scope, error) {
	if cc == nil || cc.cfg == nil || agentCfg == nil || sess == nil {
		return memory.Scope{}, fmt.Errorf("scoped prompt memory requires serve configuration, agent, and session")
	}
	workspace := strings.TrimSpace(cc.cfg.Prizm.Workspace)
	if workspace == "" || strings.TrimSpace(runID) == "" || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(sess.ID) == "" || strings.TrimSpace(agentCfg.ID) == "" {
		return memory.Scope{}, fmt.Errorf("scoped prompt memory requires trusted workspace, run, user, session, and agent identity")
	}
	projectID, err := canonicalMemoryProjectID(workspace)
	if err != nil {
		return memory.Scope{}, err
	}
	return memory.Scope{
		UserID: ownerID, ProjectID: projectID, TaskID: runID, SessionID: sess.ID, AgentID: agentCfg.ID,
		IncludeUserScope: true, CorrelationID: runID,
	}, nil
}

func canonicalMemoryProjectID(workspace string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(workspace))
	if err != nil {
		return "", fmt.Errorf("resolve memory workspace: %w", err)
	}
	return filepath.Clean(abs), nil
}

// remembranceContextCacheKey includes the entire authorization boundary plus
// the request text digest, so a context pack cannot cross runs or users.
func remembranceContextCacheKey(scope memory.Scope, task string) string {
	userID := ""
	if scope.IncludeUserScope {
		userID = scope.UserID
	}
	hash := sha256.Sum256([]byte(strings.Join([]string{scope.ProjectID, scope.TaskID, scope.SessionID, scope.AgentID, userID, task}, "\x00")))
	return "scope:" + hex.EncodeToString(hash[:])
}

// remembranceContextMatchesScope rejects context packs that do not echo the
// request's full trusted boundary. Older Recall servers therefore fail closed.
func remembranceContextMatchesScope(pack *remembrance.ContextPackResponse, scope memory.Scope) bool {
	if pack == nil || pack.ProjectID != scope.ProjectID || pack.TaskID != scope.TaskID || pack.SessionID != scope.SessionID || pack.AgentID != scope.AgentID {
		return false
	}
	if scope.IncludeUserScope && pack.OwnerID != scope.UserID {
		return false
	}
	if pack.ContextJSON != nil && (pack.ContextJSON.ProjectID != scope.ProjectID || pack.ContextJSON.TaskID != scope.TaskID || pack.ContextJSON.SessionID != scope.SessionID || pack.ContextJSON.AgentID != scope.AgentID) {
		return false
	}
	return true
}
