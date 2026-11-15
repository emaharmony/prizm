package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/memory"
)

const (
	freewillMemoryContextFactSource  = "prizm:freewill-terminal"
	freewillMemoryContextDiarySource = "prizm:freewill-diary"
	freewillMemoryContextConfidence  = freewill.ProjectContextConfidenceOwnerAuthorizedMemory
	freewillMemoryContextScanLimit   = 100
	freewillMemoryContextItemLimit   = 5 << 10
	freewillMemoryContextTotalLimit  = 8 << 10
)

type freewillStoredTerminalFact struct {
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
	Confidence        string    `json:"confidence"`
	RecordedAt        time.Time `json:"recorded_at"`
}

type freewillProjectMemoryFactEvidence struct {
	Version    int       `json:"version"`
	Kind       string    `json:"kind"`
	SourceID   string    `json:"source_id"`
	RecordID   string    `json:"record_id"`
	Source     string    `json:"source"`
	Category   string    `json:"category"`
	RecordedAt time.Time `json:"recorded_at"`
	Confidence string    `json:"confidence"`
	Status     string    `json:"status"`
	Validation string    `json:"validation_summary"`
	EvidenceID string    `json:"evidence_id"`
}

type freewillProjectMemoryDiaryEvidence struct {
	Version        int       `json:"version"`
	Kind           string    `json:"kind"`
	SourceID       string    `json:"source_id"`
	RecordID       string    `json:"record_id"`
	Source         string    `json:"source"`
	Category       string    `json:"category"`
	RecordedAt     time.Time `json:"recorded_at"`
	Confidence     string    `json:"confidence"`
	EvidenceFactID string    `json:"evidence_fact_id"`
	Provider       string    `json:"provider"`
	Model          string    `json:"model"`
	ModelVersion   string    `json:"model_version"`
	Narrative      string    `json:"narrative"`
}

type freewillProjectMemoryPair struct {
	factEntry  memory.Memory
	fact       freewillStoredTerminalFact
	diaryEntry memory.Memory
	diary      freewillDiaryRetrospective
}

func appendFreewillOwnerMemoryContext(ctx context.Context, snapshot freewill.ProjectContextSnapshot, sink *freewillTerminalFactMemorySink,
	ownerID, projectID, workspace, agentID string, sourceAllowlist []string) (freewill.ProjectContextSnapshot, error) {
	if snapshot.ProjectID != projectID {
		return freewill.ProjectContextSnapshot{}, errors.New("freewill: memory context project binding is invalid")
	}
	sources, err := sink.BuildOwnerProjectContext(ctx, ownerID, projectID, workspace, agentID, sourceAllowlist)
	if err != nil {
		return freewill.ProjectContextSnapshot{}, err
	}
	snapshot.Sources = append(snapshot.Sources, sources...)
	snapshot.Digest = ""
	snapshot.Seal()
	return snapshot, nil
}

// BuildOwnerProjectContext returns at most the latest complete local fact and
// diary pair for the exact configured owner, project, workspace, and agent.
// The caller supplies the explicit project source allowlist; record bodies are
// assembled as ephemeral selector input and are never added to durable runs.
func (s *freewillTerminalFactMemorySink) BuildOwnerProjectContext(ctx context.Context, ownerID, projectID, workspace, agentID string, sourceAllowlist []string) ([]freewill.ProjectContextSource, error) {
	if s == nil || s.store == nil || s.store.Local == nil || strings.TrimSpace(ownerID) == "" || ownerID != s.memoryOwner {
		return nil, errors.New("freewill: project memory owner access denied")
	}
	canonical, err := canonicalFreewillWorkspace(workspace)
	if err != nil || projectID != s.projectID || canonical != s.workspace || strings.TrimSpace(agentID) == "" {
		return nil, errors.New("freewill: project memory scope is outside the opted-in binding")
	}
	allowed, err := validateFreewillMemorySourceAllowlist(sourceAllowlist)
	if err != nil {
		return nil, err
	}
	entries, err := s.store.Local.ListRecent(ctx, freewillMemoryContextScanLimit)
	if err != nil {
		return nil, err
	}
	facts := make(map[string]struct {
		entry memory.Memory
		fact  freewillStoredTerminalFact
	})
	diaries := make([]struct {
		entry memory.Memory
		value freewillDiaryRetrospective
	}, 0)
	for _, entry := range entries {
		if entry.ProjectID != projectID || entry.AgentID != agentID || entry.UserID != "" || strings.TrimSpace(entry.TaskID) == "" {
			continue
		}
		if entry.Source == freewillMemoryContextFactSource && allowed[entry.Source] && entry.Category == "fact" {
			var fact freewillStoredTerminalFact
			if json.Unmarshal([]byte(entry.Content), &fact) != nil || fact.Version != freewillTerminalFactVersion ||
				fact.Kind != "host_terminal_outcome" || fact.ProjectID != projectID || fact.WorkspaceID != s.workspaceID ||
				fact.AgentID != agentID || fact.IntentionID == "" || fact.ParentRunID == "" || fact.ChildRunID == "" ||
				!validFreewillFactStatus(fact.Status) || !validFreewillFactSHA256(fact.ReportSHA256) ||
				fact.EvidenceID != "sha256:"+strings.ToLower(fact.ReportSHA256) || fact.RecordedAt.IsZero() ||
				fact.Confidence != "host_validated_terminal_receipt" ||
				entry.TaskID != freewillTerminalTaskID(projectID, s.workspace, fact.IntentionID) {
				continue
			}
			facts[entry.ID] = struct {
				entry memory.Memory
				fact  freewillStoredTerminalFact
			}{entry, fact}
			continue
		}
		if entry.Source == freewillMemoryContextDiarySource && allowed[entry.Source] && entry.Category == "diary" {
			var diary freewillDiaryRetrospective
			if json.Unmarshal([]byte(entry.Content), &diary) != nil || diary.Version != 1 || diary.Kind != "model_retrospective" ||
				diary.EvidenceFactID == "" || diary.IntentionID == "" || diary.ParentRunID == "" || diary.RecordedAt.IsZero() ||
				diary.Provider == "" || diary.Model == "" || diary.ModelVersion == "" || diary.Confidence != "model_self_report" ||
				diary.Narrative == "" || len(diary.Narrative) > 4000 || entry.TaskID != freewillTerminalTaskID(projectID, s.workspace, diary.IntentionID) {
				continue
			}
			diaries = append(diaries, struct {
				entry memory.Memory
				value freewillDiaryRetrospective
			}{entry, diary})
		}
	}
	pairs := make([]freewillProjectMemoryPair, 0, len(diaries))
	for _, diary := range diaries {
		fact, ok := facts[diary.value.EvidenceFactID]
		if !ok || fact.fact.IntentionID != diary.value.IntentionID || fact.fact.ParentRunID != diary.value.ParentRunID ||
			fact.entry.TaskID != diary.entry.TaskID {
			continue
		}
		pairs = append(pairs, freewillProjectMemoryPair{factEntry: fact.entry, fact: fact.fact, diaryEntry: diary.entry, diary: diary.value})
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	sort.Slice(pairs, func(i, j int) bool {
		if !pairs[i].fact.RecordedAt.Equal(pairs[j].fact.RecordedAt) {
			return pairs[i].fact.RecordedAt.After(pairs[j].fact.RecordedAt)
		}
		return pairs[i].factEntry.ID < pairs[j].factEntry.ID
	})
	selected := pairs[0]
	factID := "memory:fact:" + selected.factEntry.ID
	diaryID := "memory:self-report:" + selected.diaryEntry.ID
	factContent, err := json.Marshal(freewillProjectMemoryFactEvidence{
		Version: 1, Kind: "host_verified_fact", SourceID: factID, RecordID: selected.factEntry.ID,
		Source: selected.factEntry.Source, Category: selected.factEntry.Category, RecordedAt: selected.fact.RecordedAt.UTC(),
		Confidence: selected.fact.Confidence, Status: selected.fact.Status, Validation: selected.fact.ValidationSummary,
		EvidenceID: selected.fact.EvidenceID,
	})
	if err != nil {
		return nil, errors.New("freewill: project memory fact could not be encoded")
	}
	diaryContent, err := json.Marshal(freewillProjectMemoryDiaryEvidence{
		Version: 1, Kind: "model_self_report", SourceID: diaryID, RecordID: selected.diaryEntry.ID,
		Source: selected.diaryEntry.Source, Category: selected.diaryEntry.Category, RecordedAt: selected.diary.RecordedAt.UTC(),
		Confidence: selected.diary.Confidence, EvidenceFactID: selected.diary.EvidenceFactID,
		Provider: selected.diary.Provider, Model: selected.diary.Model, ModelVersion: selected.diary.ModelVersion,
		Narrative: selected.diary.Narrative,
	})
	if err != nil {
		return nil, errors.New("freewill: project memory diary could not be encoded")
	}
	if len(factContent) > freewillMemoryContextItemLimit || len(diaryContent) > freewillMemoryContextItemLimit ||
		len(factContent)+len(diaryContent) > freewillMemoryContextTotalLimit {
		return nil, errors.New("freewill: selected project memory context exceeds its limit")
	}
	observedAt := selected.fact.RecordedAt.UTC()
	sources := []freewill.ProjectContextSource{
		{ID: factID, Revision: "", Digest: "", ObservedAt: observedAt, Confidence: freewillMemoryContextConfidence, Content: string(factContent)},
		{ID: diaryID, Revision: "", Digest: "", ObservedAt: observedAt, Confidence: freewillMemoryContextConfidence, Content: string(diaryContent)},
	}
	return sources, nil
}

func validateFreewillMemorySourceAllowlist(sources []string) (map[string]bool, error) {
	allowed := map[string]bool{freewillMemoryContextFactSource: false, freewillMemoryContextDiarySource: false}
	if len(sources) != len(allowed) {
		return nil, errors.New("freewill: memory context source allowlist is incomplete")
	}
	for _, source := range sources {
		if _, ok := allowed[source]; !ok || allowed[source] {
			return nil, errors.New("freewill: memory context source allowlist is invalid")
		}
		allowed[source] = true
	}
	return allowed, nil
}
