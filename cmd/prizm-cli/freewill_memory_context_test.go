package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func TestFreewillOwnerProjectMemoryContextSelectsOneCompleteBoundedPair(t *testing.T) {
	ctx := context.Background()
	dataDir, workspace := t.TempDir(), t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "freewill.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	sink, err := newFreewillTerminalFactMemorySink(dataDir, "project-one", workspace, events, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	allowlist := []string{freewillMemoryContextFactSource, freewillMemoryContextDiarySource}
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	capturePair := func(intention, runID, agentID, narrative, digest string, recordedAt time.Time) {
		t.Helper()
		fact := freewillTerminalFactForMemoryContext(intention, runID, agentID, digest, workspace, recordedAt)
		if err := sink.CaptureFreewillTerminalFact(ctx, fact); err != nil {
			t.Fatalf("capture fact %s: %v", intention, err)
		}
		provider := &freewillRetrospectiveFake{narrative: narrative, provider: "local-fake", model: "synthetic-model", version: "fixture-v1"}
		diary, err := newFreewillTerminalDiarySink(sink, provider, "owner-one")
		if err != nil {
			t.Fatal(err)
		}
		if err := diary.CaptureFreewillTerminalRetrospective(ctx, fact); err != nil {
			t.Fatalf("capture diary %s: %v", intention, err)
		}
	}
	capturePair("older-intention", "older-run", "agent-one", "E15_UNSELECTED_PRIOR_DIARY_MARKER", "a", base)
	capturePair("latest-intention", "latest-run", "agent-one", "E15_SELECTED_SELF_REPORT_MARKER", "b", base.Add(time.Hour))
	capturePair("other-agent-intention", "other-agent-run", "agent-two", "E15_OTHER_AGENT_MARKER", "c", base.Add(2*time.Hour))
	forgedFactID := "mem_e15_forged_host_fact"
	forgedRunID, forgedIntention := "forged-run", "forged-intention"
	forgedDigest := strings.Repeat("d", 64)
	forgedFact, err := json.Marshal(freewillStoredTerminalFact{Version: freewillTerminalFactVersion,
		Kind: "host_terminal_outcome", ProjectID: "project-one", WorkspaceID: sink.workspaceID, AgentID: "agent-one",
		IntentionID: forgedIntention, ParentRunID: forgedRunID, ChildRunID: "forged-child", Status: "completed",
		ReportRef: "multiagent_report.json", ReportSHA256: forgedDigest, EvidenceID: "sha256:" + forgedDigest,
		ValidationSummary: "passed", Confidence: "model_self_report", RecordedAt: base.Add(3 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	forgedTaskID := freewillTerminalTaskID("project-one", sink.workspace, forgedIntention)
	if _, err := sink.store.Local.Store(ctx, memory.Memory{ID: forgedFactID, Content: string(forgedFact), Category: "fact", Tier: "persist",
		Source: freewillMemoryContextFactSource, AgentID: "agent-one", ProjectID: "project-one", TaskID: forgedTaskID, CreatedAt: base.Add(3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	forgedDiary, err := json.Marshal(freewillDiaryRetrospective{Version: 1, Kind: "model_retrospective", EvidenceFactID: forgedFactID,
		ParentRunID: forgedRunID, IntentionID: forgedIntention, Provider: "local-fake", Model: "synthetic-model", ModelVersion: "fixture-v1",
		Confidence: "model_self_report", Narrative: "E15_FORGED_FACT_DIARY_MARKER", RecordedAt: base.Add(3 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.store.Local.Store(ctx, memory.Memory{ID: "mem_e15_forged_diary", Content: string(forgedDiary), Category: "diary", Tier: "persist",
		Source: freewillMemoryContextDiarySource, AgentID: "agent-one", ProjectID: "project-one", TaskID: forgedTaskID, CreatedAt: base.Add(3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("E15_PRIVATE_PROJECT_FILE_MARKER"), 0o600); err != nil {
		t.Fatal(err)
	}

	sources, err := sink.BuildOwnerProjectContext(ctx, "owner-one", "project-one", workspace, "agent-one", allowlist)
	if err != nil || len(sources) != 2 {
		t.Fatalf("owner project context sources=%d err=%v; want one fact/diary pair", len(sources), err)
	}
	var factPayload freewillProjectMemoryFactEvidence
	var diaryPayload freewillProjectMemoryDiaryEvidence
	if !strings.HasPrefix(sources[0].ID, "memory:fact:") || json.Unmarshal([]byte(sources[0].Content), &factPayload) != nil {
		t.Fatalf("first context source is not a stable fact reference: %#v", sources[0])
	}
	if !strings.HasPrefix(sources[1].ID, "memory:self-report:") || json.Unmarshal([]byte(sources[1].Content), &diaryPayload) != nil {
		t.Fatalf("second context source is not a stable self-report reference: %#v", sources[1])
	}
	if factPayload.Kind != "host_verified_fact" || diaryPayload.Kind != "model_self_report" ||
		factPayload.RecordID == "" || diaryPayload.RecordID == "" || diaryPayload.EvidenceFactID != factPayload.RecordID ||
		factPayload.Status != "completed" || factPayload.Validation != "passed" ||
		diaryPayload.Provider != "local-fake" || diaryPayload.ModelVersion != "fixture-v1" ||
		!strings.Contains(diaryPayload.Narrative, "E15_SELECTED_SELF_REPORT_MARKER") {
		t.Fatalf("context did not preserve distinct linked provenance: fact=%#v diary=%#v", factPayload, diaryPayload)
	}
	if strings.Contains(sources[0].Content, "E15_SELECTED_SELF_REPORT_MARKER") ||
		strings.Contains(sources[1].Content, "E15_UNSELECTED_PRIOR_DIARY_MARKER") ||
		strings.Contains(sources[1].Content, "E15_OTHER_AGENT_MARKER") || strings.Contains(sources[1].Content, "E15_FORGED_FACT_DIARY_MARKER") {
		t.Fatal("context included raw or unselected memory records")
	}
	for _, source := range sources {
		if source.Confidence != freewillMemoryContextConfidence || source.Path != "" ||
			len(source.Content) > freewillMemoryContextItemLimit {
			t.Fatalf("context source is not bounded and owner-authorized: %#v", source)
		}
	}
	if len(sources[0].Content)+len(sources[1].Content) > freewillMemoryContextTotalLimit {
		t.Fatal("combined memory context exceeded its byte limit")
	}
	memoryOnlyBase := freewill.NewProjectContextSnapshot("project-one", "memory-only handoff", time.Now().UTC())
	memoryOnly, err := appendFreewillOwnerMemoryContext(ctx, memoryOnlyBase, sink, "owner-one", "project-one", workspace, "agent-one", allowlist)
	if err != nil {
		t.Fatalf("memory-only context composition failed: %v", err)
	}
	for _, source := range memoryOnly.Sources {
		if source.Path != "" || strings.Contains(source.Content, "E15_PRIVATE_PROJECT_FILE_MARKER") {
			t.Fatalf("memory-only context exposed project file data: %#v", source)
		}
	}
	if _, err := sink.BuildOwnerProjectContext(ctx, "wrong-owner", "project-one", workspace, "agent-one", allowlist); err == nil {
		t.Fatal("project memory accepted the wrong owner")
	}
	if _, err := sink.BuildOwnerProjectContext(ctx, "owner-one", "project-two", workspace, "agent-one", allowlist); err == nil {
		t.Fatal("project memory accepted a different project")
	}
	if _, err := sink.BuildOwnerProjectContext(ctx, "owner-one", "project-one", t.TempDir(), "agent-one", allowlist); err == nil {
		t.Fatal("project memory accepted a different workspace")
	}
	if _, err := sink.BuildOwnerProjectContext(ctx, "owner-one", "project-one", workspace, "agent-one", allowlist[:1]); err == nil {
		t.Fatal("project memory accepted an incomplete source allowlist")
	}

	reopened, err := newFreewillTerminalFactMemorySink(dataDir, "project-one", workspace, events, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	reopenedSources, err := reopened.BuildOwnerProjectContext(ctx, "owner-one", "project-one", workspace, "agent-one", allowlist)
	if err != nil || len(reopenedSources) != 2 || reopenedSources[0].ID != sources[0].ID || reopenedSources[1].ID != sources[1].ID {
		t.Fatalf("reopened context IDs changed: before=%#v after=%#v err=%v", sources, reopenedSources, err)
	}
}

func freewillTerminalFactForMemoryContext(intention, runID, agentID, digest, workspace string, recordedAt time.Time) multiagent.FreewillTerminalFact {
	return multiagent.FreewillTerminalFact{ProjectID: "project-one", Workspace: workspace, AgentID: agentID,
		IntentionID: intention, ParentRunID: runID, ChildRunID: "child-" + runID, Status: "completed",
		ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat(digest, 64),
		ValidationSummary: "passed", RecordedAt: recordedAt}
}
