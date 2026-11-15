package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/api"
	"github.com/emaharmony/prizm/internal/memory"
)

const (
	freewillDiaryAnnotationSource = "prizm:freewill-diary-annotation"
	freewillDiaryAnnotationCap    = 2000
)

type freewillDiaryAnnotationSink struct {
	facts     *freewillTerminalFactMemorySink
	ownerID   string
	agentID   string
	projectID string
	workspace string
}

type freewillDiaryAnnotationRecord struct {
	Version          int       `json:"version"`
	Kind             string    `json:"kind"`
	EntryID          string    `json:"entry_id"`
	EntryRevision    int       `json:"revision"`
	PreviousRevision int       `json:"previous_revision"`
	OwnerID          string    `json:"owner_id"`
	ProjectID        string    `json:"project_id"`
	WorkspaceID      string    `json:"workspace_id"`
	AgentID          string    `json:"agent_id"`
	IntentionID      string    `json:"intention_id"`
	ParentRunID      string    `json:"parent_run_id"`
	Text             string    `json:"text"`
	TextSHA256       string    `json:"text_sha256"`
	RecordedAt       time.Time `json:"recorded_at"`
}

func newFreewillDiaryAnnotationSink(facts *freewillTerminalFactMemorySink, ownerID, projectID, workspace, agentID string) (*freewillDiaryAnnotationSink, error) {
	if facts == nil || facts.store == nil || facts.claimer == nil || ownerID == "" || ownerID != strings.TrimSpace(ownerID) ||
		projectID == "" || projectID != facts.projectID || agentID == "" || agentID != strings.TrimSpace(agentID) {
		return nil, errors.New("freewill: diary annotations require an exact local diary binding and owner")
	}
	canonical, err := canonicalFreewillWorkspace(workspace)
	if err != nil || canonical != facts.workspace {
		return nil, errors.New("freewill: diary annotations require the configured workspace")
	}
	return &freewillDiaryAnnotationSink{facts: facts, ownerID: ownerID, projectID: projectID, workspace: canonical, agentID: agentID}, nil
}

func (s *freewillDiaryAnnotationSink) ReadDiaryAnnotations(ctx context.Context, ownerID, projectID, workspace, agentID, intentionID, parentRunID, entryID string) (api.FreewillDiaryAnnotationEntry, error) {
	scope, diary, original, err := s.loadSource(ctx, ownerID, projectID, workspace, agentID, intentionID, parentRunID, entryID)
	if err != nil {
		return api.FreewillDiaryAnnotationEntry{}, err
	}
	records, err := s.loadAnnotations(ctx, scope, diary, original)
	if err != nil {
		return api.FreewillDiaryAnnotationEntry{}, err
	}
	return s.view(diary, original, records), nil
}

func (s *freewillDiaryAnnotationSink) AppendDiaryAnnotation(ctx context.Context, ownerID, projectID, workspace, agentID, intentionID, parentRunID, entryID string, expectedRevision int, text string) (api.FreewillDiaryAnnotationEntry, error) {
	if len(text) == 0 || len(text) > freewillDiaryAnnotationCap || strings.TrimSpace(text) == "" || expectedRevision < 1 {
		return api.FreewillDiaryAnnotationEntry{}, errors.New("freewill: annotation text or expected revision is invalid")
	}
	_, _, _, err := s.loadSource(ctx, ownerID, projectID, workspace, agentID, intentionID, parentRunID, entryID)
	if err != nil {
		return api.FreewillDiaryAnnotationEntry{}, err
	}
	claimID := freewillBindingDigest("freewill-diary-annotation-chain/v1", ownerID, projectID, s.facts.workspaceID, agentID, entryID)
	claim, err := s.facts.acquireCaptureClaim(ctx, claimID)
	if err != nil {
		return api.FreewillDiaryAnnotationEntry{}, err
	}
	defer claim.Release()

	// Reload under the cross-process claim so different request payloads cannot
	// both append the same next revision.
	scope, diary, original, err := s.loadSource(ctx, ownerID, projectID, workspace, agentID, intentionID, parentRunID, entryID)
	if err != nil {
		return api.FreewillDiaryAnnotationEntry{}, err
	}
	records, err := s.loadAnnotations(ctx, scope, diary, original)
	if err != nil {
		return api.FreewillDiaryAnnotationEntry{}, err
	}
	textDigest := freewillDiaryTextDigest(text)
	for _, record := range records {
		if record.PreviousRevision == expectedRevision {
			if record.TextSHA256 != textDigest || record.Text != text {
				return api.FreewillDiaryAnnotationEntry{}, errors.New("freewill: annotation revision already contains a different request")
			}
			return s.view(diary, original, records), nil
		}
	}
	currentRevision := 1
	if len(records) > 0 {
		currentRevision = records[len(records)-1].EntryRevision
	}
	if expectedRevision != currentRevision {
		return api.FreewillDiaryAnnotationEntry{}, errors.New("freewill: diary annotation expected revision is stale")
	}
	annotation := freewillDiaryAnnotationRecord{
		Version: 1, Kind: "owner_annotation", EntryID: entryID, EntryRevision: currentRevision + 1,
		PreviousRevision: currentRevision, OwnerID: ownerID, ProjectID: projectID,
		WorkspaceID: s.facts.workspaceID, AgentID: agentID, IntentionID: intentionID,
		ParentRunID: parentRunID, Text: text, TextSHA256: textDigest, RecordedAt: time.Now().UTC(),
	}
	encoded, err := json.Marshal(annotation)
	if err != nil {
		return api.FreewillDiaryAnnotationEntry{}, err
	}
	key := "freewill-diary-annotation/v1:" + freewillBindingDigest(ownerID, projectID, s.facts.workspaceID, agentID, entryID, fmt.Sprint(currentRevision))
	_, _, err = s.facts.store.Capture(ctx, memory.CaptureRequest{
		Scope: scope, Content: string(encoded), Summary: "Owner-authored Freewill diary annotation",
		Category: "diary_annotation", Tier: "persist", Source: freewillDiaryAnnotationSource,
		CaptureKey: key,
	})
	if err != nil {
		return api.FreewillDiaryAnnotationEntry{}, err
	}
	records = append(records, annotation)
	return s.view(diary, original, records), nil
}

func (s *freewillDiaryAnnotationSink) loadSource(ctx context.Context, ownerID, projectID, workspace, agentID, intentionID, parentRunID, entryID string) (memory.Scope, memory.Memory, freewillDiaryRetrospective, error) {
	var zero memory.Memory
	if s == nil || s.facts == nil || s.facts.store == nil || ownerID != s.ownerID || projectID != s.projectID || agentID != s.agentID ||
		intentionID == "" || intentionID != strings.TrimSpace(intentionID) || parentRunID == "" || parentRunID != strings.TrimSpace(parentRunID) || entryID == "" {
		return memory.Scope{}, zero, freewillDiaryRetrospective{}, errors.New("freewill: diary owner or scope is unavailable")
	}
	scope, err := s.facts.scopeFor(projectID, workspace, intentionID, agentID, parentRunID)
	if err != nil {
		return memory.Scope{}, zero, freewillDiaryRetrospective{}, err
	}
	scope.UserID = ownerID
	scope.IncludeUserScope = true
	entry, err := s.facts.store.Local.Get(ctx, entryID)
	if err != nil || entry == nil || entry.ID != entryID || entry.Source != freewillDiarySource || entry.Category != "diary" || entry.ProjectID != projectID ||
		entry.TaskID != scope.TaskID || entry.AgentID != agentID || entry.UserID != "" {
		return memory.Scope{}, zero, freewillDiaryRetrospective{}, errors.New("freewill: exact source diary entry is unavailable")
	}
	var retrospective freewillDiaryRetrospective
	if json.Unmarshal([]byte(entry.Content), &retrospective) != nil || retrospective.Version != 1 || retrospective.Kind != "model_retrospective" ||
		retrospective.IntentionID != intentionID || retrospective.ParentRunID != parentRunID || retrospective.EvidenceFactID == "" || retrospective.Confidence != "model_self_report" ||
		strings.TrimSpace(retrospective.Provider) == "" || strings.TrimSpace(retrospective.Model) == "" || strings.TrimSpace(retrospective.ModelVersion) == "" ||
		strings.TrimSpace(retrospective.Narrative) == "" || retrospective.RecordedAt.IsZero() {
		return memory.Scope{}, zero, freewillDiaryRetrospective{}, errors.New("freewill: source diary provenance is invalid")
	}
	fact, err := s.facts.store.Local.Get(ctx, retrospective.EvidenceFactID)
	if err != nil || fact == nil || fact.Source != "prizm:freewill-terminal" || fact.Category != "fact" || fact.ProjectID != projectID ||
		fact.TaskID != scope.TaskID || fact.AgentID != agentID || fact.UserID != "" {
		return memory.Scope{}, zero, freewillDiaryRetrospective{}, errors.New("freewill: source host fact is unavailable")
	}
	var hostFact struct {
		Version     string `json:"version"`
		Kind        string `json:"kind"`
		ProjectID   string `json:"project_id"`
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		IntentionID string `json:"intention_id"`
		ParentRunID string `json:"parent_run_id"`
		Confidence  string `json:"confidence"`
	}
	if json.Unmarshal([]byte(fact.Content), &hostFact) != nil || hostFact.Version != freewillTerminalFactVersion || hostFact.Kind != "host_terminal_outcome" ||
		hostFact.ProjectID != projectID || hostFact.WorkspaceID != s.facts.workspaceID || hostFact.AgentID != agentID || hostFact.IntentionID != intentionID ||
		hostFact.ParentRunID != parentRunID || hostFact.Confidence != "host_validated_terminal_receipt" {
		return memory.Scope{}, zero, freewillDiaryRetrospective{}, errors.New("freewill: source host fact provenance is invalid")
	}
	return scope, *entry, retrospective, nil
}

func (s *freewillDiaryAnnotationSink) loadAnnotations(ctx context.Context, scope memory.Scope, diary memory.Memory, original freewillDiaryRetrospective) ([]freewillDiaryAnnotationRecord, error) {
	all, err := s.facts.store.Local.ListRecent(ctx, 0)
	if err != nil {
		return nil, err
	}
	records := make([]freewillDiaryAnnotationRecord, 0)
	for _, entry := range all {
		if entry.Source != freewillDiaryAnnotationSource || entry.Category != "diary_annotation" || entry.UserID != s.ownerID || entry.ProjectID != scope.ProjectID ||
			entry.TaskID != scope.TaskID || entry.AgentID != scope.AgentID {
			continue
		}
		var record freewillDiaryAnnotationRecord
		if json.Unmarshal([]byte(entry.Content), &record) != nil {
			return nil, errors.New("freewill: diary annotation record is unreadable")
		}
		if record.EntryID != diary.ID {
			continue
		}
		if record.Version != 1 || record.Kind != "owner_annotation" || record.OwnerID != s.ownerID || record.ProjectID != scope.ProjectID || record.WorkspaceID != s.facts.workspaceID || record.AgentID != scope.AgentID ||
			record.IntentionID != original.IntentionID || record.ParentRunID != original.ParentRunID || record.Text == "" || len(record.Text) > freewillDiaryAnnotationCap ||
			record.TextSHA256 != freewillDiaryTextDigest(record.Text) || record.EntryRevision < 2 || record.PreviousRevision+1 != record.EntryRevision {
			return nil, errors.New("freewill: diary annotation provenance is invalid")
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].EntryRevision < records[j].EntryRevision })
	for index, record := range records {
		if record.EntryRevision != index+2 || record.PreviousRevision != index+1 {
			return nil, errors.New("freewill: diary annotation lineage is inconsistent")
		}
	}
	return records, nil
}

func (s *freewillDiaryAnnotationSink) view(diary memory.Memory, original freewillDiaryRetrospective, records []freewillDiaryAnnotationRecord) api.FreewillDiaryAnnotationEntry {
	out := api.FreewillDiaryAnnotationEntry{
		EntryID: diary.ID, Revision: 1 + len(records), ProjectID: diary.ProjectID, WorkspaceID: s.facts.workspaceID,
		AgentID: diary.AgentID, IntentionID: original.IntentionID, ParentRunID: original.ParentRunID,
		OriginalText: original.Narrative, Annotations: make([]api.FreewillDiaryAnnotation, 0, len(records)),
	}
	for _, record := range records {
		out.Annotations = append(out.Annotations, api.FreewillDiaryAnnotation{
			AnnotationID: freewillBindingDigest("freewill-diary-annotation-id/v1", record.OwnerID, record.EntryID, fmt.Sprint(record.EntryRevision)),
			Revision:     record.EntryRevision, Previous: record.PreviousRevision, Text: record.Text,
			TextSHA256: record.TextSHA256, RecordedAt: record.RecordedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out
}

func freewillDiaryTextDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
