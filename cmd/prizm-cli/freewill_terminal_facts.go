package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

const freewillTerminalFactVersion = "freewill-terminal-fact/v1"

type freewillTerminalFactClaimer interface {
	Acquire(context.Context, string) (multiagent.ExecutionClaim, error)
}

// freewillTerminalFactMemorySink translates a validated host terminal receipt
// into local, idempotent memory. Its storage root and opaque task scope are
// both bound to the exact project workspace.
type freewillTerminalFactMemorySink struct {
	store       *memory.Facade
	projectID   string
	workspace   string
	workspaceID string
	memoryOwner string
	factsRoot   string
	claimer     freewillTerminalFactClaimer
}

func newFreewillTerminalFactMemorySink(dataDir, projectID, workspace string, events event.EventStore, memoryOwnerID ...string) (*freewillTerminalFactMemorySink, error) {
	if strings.TrimSpace(dataDir) == "" || strings.TrimSpace(projectID) == "" || events == nil {
		return nil, errors.New("freewill: terminal fact store requires data directory, project, and event store")
	}
	if len(memoryOwnerID) > 1 {
		return nil, errors.New("freewill: terminal fact store received duplicate memory owner identities")
	}
	canonical, err := canonicalFreewillWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	workspaceID := freewillBindingDigest(projectID, canonical)
	factsRoot := filepath.Join(dataDir, "freewill-terminal-facts", workspaceID)
	root := filepath.Join(factsRoot, "memory")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	ownerID := ""
	if len(memoryOwnerID) == 1 {
		ownerID = strings.TrimSpace(memoryOwnerID[0])
	}
	return &freewillTerminalFactMemorySink{
		store:     &memory.Facade{Local: memory.NewMarkdownStore(root), Primary: nil, Events: events, Source: "prizm:freewill-terminal"},
		projectID: projectID, workspace: canonical, workspaceID: workspaceID,
		memoryOwner: ownerID, factsRoot: factsRoot,
		claimer: multiagent.FileRunClaimer{Root: filepath.Join(factsRoot, "capture-claims")},
	}, nil
}

func (s *freewillTerminalFactMemorySink) CaptureFreewillTerminalFact(ctx context.Context, fact multiagent.FreewillTerminalFact) error {
	if s == nil || s.store == nil || fact.ProjectID != s.projectID ||
		strings.TrimSpace(fact.AgentID) == "" || strings.TrimSpace(fact.IntentionID) == "" ||
		strings.TrimSpace(fact.ParentRunID) == "" || strings.TrimSpace(fact.ChildRunID) == "" ||
		fact.RecordedAt.IsZero() {
		return errors.New("freewill: terminal fact binding or identity is incomplete")
	}
	workspace, err := canonicalFreewillWorkspace(fact.Workspace)
	if err != nil || workspace != s.workspace {
		return errors.New("freewill: terminal fact workspace is outside the opted-in binding")
	}
	if fact.ReportRef != "multiagent_report.json" || !validFreewillFactStatus(fact.Status) ||
		!validFreewillFactSHA256(fact.ReportSHA256) || !validFreewillFactValidation(fact.ValidationSummary) ||
		!validFreewillFactFailureCode(fact.Status, fact.FailureCode) {
		return errors.New("freewill: terminal fact evidence is invalid")
	}
	scope := memory.Scope{
		ProjectID: fact.ProjectID, TaskID: freewillTerminalTaskID(fact.ProjectID, s.workspace, fact.IntentionID),
		AgentID: fact.AgentID, CorrelationID: fact.ParentRunID,
	}
	content, err := json.Marshal(struct {
		Version           string    `json:"version"`
		Kind              string    `json:"kind"`
		ProjectID         string    `json:"project_id"`
		WorkspaceID       string    `json:"workspace_id"`
		AgentID           string    `json:"agent_id"`
		IntentionID       string    `json:"intention_id"`
		ParentRunID       string    `json:"parent_run_id"`
		ChildRunID        string    `json:"child_run_id"`
		Status            string    `json:"status"`
		ReportRef         string    `json:"report_ref"`
		ReportSHA256      string    `json:"report_sha256"`
		EvidenceID        string    `json:"evidence_id"`
		ValidationSummary string    `json:"validation_summary"`
		FailureCode       string    `json:"failure_code,omitempty"`
		Confidence        string    `json:"confidence"`
		RecordedAt        time.Time `json:"recorded_at"`
	}{
		Version: freewillTerminalFactVersion, Kind: "host_terminal_outcome", ProjectID: fact.ProjectID,
		WorkspaceID: s.workspaceID, AgentID: fact.AgentID, IntentionID: fact.IntentionID,
		ParentRunID: fact.ParentRunID, ChildRunID: fact.ChildRunID, Status: fact.Status,
		ReportRef: fact.ReportRef, ReportSHA256: fact.ReportSHA256,
		EvidenceID: "sha256:" + strings.ToLower(fact.ReportSHA256), ValidationSummary: fact.ValidationSummary,
		FailureCode: fact.FailureCode, Confidence: "host_validated_terminal_receipt", RecordedAt: fact.RecordedAt.UTC(),
	})
	if err != nil {
		return err
	}
	claimID := freewillBindingDigest(fact.ProjectID, s.workspace, fact.ParentRunID)
	claim, err := s.acquireCaptureClaim(ctx, claimID)
	if err != nil {
		return err
	}
	defer claim.Release()
	// Recheck the local store under the cross-process claim. The parent run ID
	// is part of Content, so an exact match is the same immutable delivery.
	existing, err := s.store.Local.ListRecent(ctx, 0)
	if err != nil {
		return err
	}
	for _, item := range existing {
		if item.ProjectID == scope.ProjectID && item.TaskID == scope.TaskID && item.AgentID == scope.AgentID &&
			item.UserID == "" && item.Source == "prizm:freewill-terminal" && item.Content == string(content) {
			return nil
		}
	}
	_, _, err = s.store.Capture(ctx, memory.CaptureRequest{
		Scope: scope, Content: string(content),
		Summary:  "Host-validated Freewill terminal outcome: " + fact.Status + " (" + fact.ValidationSummary + ")",
		Category: "fact", Tier: "persist", Source: "prizm:freewill-terminal",
		CaptureKey: freewillTerminalFactVersion + ":" + fact.ParentRunID,
	})
	return err
}

func (s *freewillTerminalFactMemorySink) acquireCaptureClaim(ctx context.Context, claimID string) (multiagent.ExecutionClaim, error) {
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		claim, err := s.claimer.Acquire(ctx, claimID)
		if err == nil {
			return claim, nil
		}
		if !errors.Is(err, multiagent.ErrRunClaimed) {
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return nil, err
		case <-timer.C:
		}
	}
}

func (s *freewillTerminalFactMemorySink) scopeFor(projectID, workspace, intentionID, agentID, correlationID string) (memory.Scope, error) {
	canonical, err := canonicalFreewillWorkspace(workspace)
	if err != nil || s == nil || s.store == nil || projectID != s.projectID || canonical != s.workspace ||
		strings.TrimSpace(intentionID) == "" || strings.TrimSpace(agentID) == "" || strings.TrimSpace(correlationID) == "" {
		return memory.Scope{}, errors.New("freewill: terminal fact scope is outside the opted-in binding")
	}
	return memory.Scope{ProjectID: projectID, TaskID: freewillTerminalTaskID(projectID, canonical, intentionID), AgentID: agentID, CorrelationID: correlationID}, nil
}

func canonicalFreewillWorkspace(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("freewill: terminal fact workspace must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("freewill: terminal fact workspace is unavailable")
	}
	canonical := filepath.ToSlash(filepath.Clean(resolved))
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	return canonical, nil
}

func freewillTerminalFactsBindingMatches(projectID, projectPath, approvedProjectID, approvedRepoPath string) bool {
	if strings.TrimSpace(projectID) == "" || projectID != approvedProjectID || strings.TrimSpace(approvedRepoPath) == "" {
		return false
	}
	approved, err := canonicalFreewillWorkspace(approvedRepoPath)
	if err != nil {
		return false
	}
	configured, err := canonicalFreewillWorkspace(projectPath)
	return err == nil && approved == configured
}

func freewillBindingDigest(parts ...string) string {
	data, _ := json.Marshal(parts)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func freewillTerminalTaskID(projectID, workspace, intentionID string) string {
	return "fw-intention-" + freewillBindingDigest(freewillTerminalFactVersion, projectID, workspace, intentionID)
}

func validFreewillFactStatus(status string) bool {
	switch status {
	case "completed", "execution_failed", "execution_cancelled":
		return true
	default:
		return false
	}
}

func validFreewillFactSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validFreewillFactValidation(value string) bool {
	switch value {
	case "passed", "failed", "not_run":
		return true
	default:
		return false
	}
}

func validFreewillFactFailureCode(status, value string) bool {
	if status == "completed" {
		return value == ""
	}
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' {
			return false
		}
	}
	return true
}
