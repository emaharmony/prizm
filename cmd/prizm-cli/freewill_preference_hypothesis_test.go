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
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

type freewillPreferenceFake struct {
	calls int
	input string
}

func (p *freewillPreferenceFake) DerivePreference(_ context.Context, input string) (string, string, string, string, error) {
	p.calls++
	p.input = input
	return `{"hypothesis":"The two appraisals may indicate interest in exploratory tasks.","uncertainty":"Only two synthetic tasks support this tentative hypothesis."}`, "local-fake", "synthetic-preference", "fixture-v1", nil
}

func TestFreewillPreferenceHypothesisUsesTwoMetadataPairsAndReopensExactlyScoped(t *testing.T) {
	ctx := context.Background()
	dataDir, workspace := t.TempDir(), t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	facts, err := newFreewillTerminalFactMemorySink(dataDir, "project-one", workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{narrative: "private source narrative that must not reach provider", provider: "local-fake", model: "appraisal", version: "v1"}, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	selectedTaskIDs := make([]string, 0, 2)
	for i, kind := range []string{"curiosity", "satisfaction", "meaning"} {
		fact := multiagent.FreewillTerminalFact{ProjectID: "project-one", Workspace: workspace, AgentID: "agent-one", ParentRunID: "parent-" + string(rune('1'+i)), ChildRunID: "task-" + string(rune('1'+i)), Status: "completed", ValidationSummary: "passed"}
		taskID := freewillTaskScopeID(fact.ProjectID, facts.workspace, fact.ChildRunID)
		if i < 2 {
			selectedTaskIDs = append(selectedTaskIDs, taskID)
		}
		outcome := freewillTaskOutcome{Version: "freewill-task-outcome/v1", Kind: "host_task_outcome", ProjectID: fact.ProjectID, WorkspaceID: facts.workspaceID, Workspace: facts.workspace, AgentID: fact.AgentID, TaskID: taskID, ParentRunID: fact.ParentRunID, ChildRunID: fact.ChildRunID, Status: fact.Status, Validation: fact.ValidationSummary, Confidence: "host_validated_terminal_receipt"}
		ob, _ := json.Marshal(outcome)
		oid, err := facts.store.Local.Store(ctx, memory.Memory{Content: string(ob), Summary: "Freewill task outcome", Category: "fact", Tier: "persist", Source: freewillTaskOutcomeSource, UserID: "owner-one", ProjectID: fact.ProjectID, TaskID: taskID, AgentID: fact.AgentID, Metadata: map[string]string{"correlation_id": fact.ParentRunID}})
		if err != nil {
			t.Fatal(err)
		}
		report := freewillTaskAppraisal{Version: 1, Kind: "model_task_self_report", OutcomeID: oid, ProjectID: fact.ProjectID, WorkspaceID: facts.workspaceID, TaskID: taskID, ParentRunID: fact.ParentRunID, ChildRunID: fact.ChildRunID, Confidence: "model_self_report", AppraisalKind: kind, Narrative: "private source narrative that must not reach provider"}
		rb, _ := json.Marshal(report)
		if _, err := facts.store.Local.Store(ctx, memory.Memory{Content: string(rb), Summary: "Qualitative appraisal", Category: "appraisal", Tier: "persist", Source: freewillTaskAppraisalSource, UserID: "owner-one", ProjectID: fact.ProjectID, TaskID: taskID, AgentID: fact.AgentID, Metadata: map[string]string{"correlation_id": fact.ParentRunID}}); err != nil {
			t.Fatal(err)
		}
	}
	provider := &freewillPreferenceFake{}
	sink, err := newFreewillPreferenceHypothesisSink(appraisals, provider, "owner-one", selectedTaskIDs)
	if err != nil {
		t.Fatal(err)
	}
	trigger := multiagent.FreewillTerminalFact{ProjectID: "project-one", Workspace: workspace, AgentID: "agent-one", ParentRunID: "parent-2", ChildRunID: "task-2"}
	if err := sink.DerivePreference(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || strings.Contains(provider.input, "private source narrative") || !strings.Contains(provider.input, "curiosity") || !strings.Contains(provider.input, "satisfaction") {
		t.Fatalf("provider input/calls violate metadata contract: calls=%d input=%s", provider.calls, provider.input)
	}
	var in freewillPreferenceInput
	if err := json.Unmarshal([]byte(provider.input), &in); err != nil || len(in.Pairs) != 2 || in.Pairs[0].TaskID == in.Pairs[1].TaskID {
		t.Fatalf("input pairs=%#v err=%v", in, err)
	}
	facts, err = newFreewillTerminalFactMemorySink(dataDir, "project-one", workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err = newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{narrative: "unused", provider: "local-fake", model: "appraisal", version: "v1"}, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	restartProvider := &freewillPreferenceFake{}
	sink, err = newFreewillPreferenceHypothesisSink(appraisals, restartProvider, "owner-one", selectedTaskIDs)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.DerivePreference(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	if restartProvider.calls != 0 {
		t.Fatalf("replay called provider %d times", restartProvider.calls)
	}
	entries, err := sink.ReadOwnerPreference(ctx, "owner-one", "project-one", workspace, "agent-one")
	if err != nil || len(entries) != 1 {
		t.Fatalf("exact scope records=%d err=%v", len(entries), err)
	}
	preferenceID := entries[0].ID
	var stored freewillPreferenceHypothesis
	if err := json.Unmarshal([]byte(entries[0].Content), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.PairCitations) != 2 || stored.PairCitations[0].OutcomeID == "" || stored.PairCitations[0].SelfReportID == "" || stored.PairCitations[1].OutcomeID == "" || stored.PairCitations[1].SelfReportID == "" || stored.Confidence != "model_preference_hypothesis" {
		t.Fatalf("citation/provenance lost: %#v", stored)
	}
	challengeRevision := freewillBindingDigest(entries[0].ID, entries[0].Content, entries[0].Source)
	challenge, revision, err := sink.ChallengePreference(ctx, "owner-one", "project-one", workspace, "agent-one", entries[0].ID, challengeRevision, "challenge-1", stored.PairCitations[0].OutcomeID, "This misses that I preferred the task with more exploratory work.", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var attributed freewillPreferenceChallenge
	if json.Unmarshal(challenge, &attributed) != nil || attributed.Verified || attributed.OwnerID != "owner-one" || attributed.HypothesisRevision != challengeRevision || attributed.SourceID != stored.PairCitations[0].OutcomeID {
		t.Fatalf("challenge attribution/source binding invalid: %s", challenge)
	}
	var revised freewillPreferenceRevision
	if json.Unmarshal(revision, &revised) != nil || revised.PreviousID != entries[0].ID || revised.PreviousRevision != challengeRevision || revised.ChallengeID != "challenge-1" || revised.Confidence != "host_revision_high_uncertainty" || revised.Uncertainty == "" || revised.Explanation == "" {
		t.Fatalf("revision lineage/explanation invalid: %s", revision)
	}
	replayedChallenge, replayedRevision, err := sink.ChallengePreference(ctx, "owner-one", "project-one", workspace, "agent-one", entries[0].ID, challengeRevision, "challenge-1", stored.PairCitations[0].OutcomeID, "This misses that I preferred the task with more exploratory work.", time.Now().Add(time.Minute))
	if err != nil || string(replayedChallenge) != string(challenge) || string(replayedRevision) != string(revision) {
		t.Fatalf("exact replay was not idempotent: err=%v", err)
	}
	// Model a process stop after the first append. An exact retry must repair
	// the missing revision without duplicating or changing the challenge body.
	partial := freewillPreferenceChallenge{Version: 1, Kind: "attributed_preference_challenge", ChallengeID: "crash-gap", OwnerID: "owner-one", ProjectID: "project-one", WorkspaceID: facts.workspaceID, AgentID: "agent-one", HypothesisID: entries[0].ID, HypothesisRevision: challengeRevision, SourceID: stored.PairCitations[1].SelfReportID, Rationale: "The second citation also deserves inspection.", Verified: false, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	partialBytes, _ := json.Marshal(partial)
	if _, err := facts.store.Local.Store(ctx, memory.Memory{Content: string(partialBytes), Summary: "Unverified attributed preference challenge", Category: "preference_challenge", Tier: "persist", Source: freewillPreferenceChallengeSource, UserID: "owner-one", ProjectID: "project-one", TaskID: freewillPreferenceScopeID("project-one", facts.workspaceID, "agent-one"), AgentID: "agent-one"}); err != nil {
		t.Fatal(err)
	}
	if _, repaired, err := sink.ChallengePreference(ctx, "owner-one", "project-one", workspace, "agent-one", entries[0].ID, challengeRevision, "crash-gap", stored.PairCitations[1].SelfReportID, partial.Rationale, time.Now()); err != nil || len(repaired) == 0 {
		t.Fatalf("exact retry did not repair interrupted append: %v %s", err, repaired)
	}
	if _, _, err := sink.ChallengePreference(ctx, "other-owner", "project-one", workspace, "agent-one", entries[0].ID, challengeRevision, "challenge-1", stored.PairCitations[0].OutcomeID, "This misses that I preferred the task with more exploratory work.", time.Now()); err == nil {
		t.Fatal("wrong owner challenge accepted")
	}
	if _, _, err := sink.ChallengePreference(ctx, "owner-one", "project-one", workspace, "agent-one", entries[0].ID, "stale-revision", "stale-challenge", stored.PairCitations[0].OutcomeID, "Challenge", time.Now()); err == nil {
		t.Fatal("stale revision accepted")
	}
	if _, _, err := sink.ChallengePreference(ctx, "owner-one", "project-one", workspace, "agent-one", entries[0].ID, challengeRevision, "wrong-source", "uncited-source", "Challenge", time.Now()); err == nil {
		t.Fatal("uncited source accepted")
	}
	if _, _, err := sink.ChallengePreference(ctx, "owner-one", "project-one", workspace, "agent-one", entries[0].ID, challengeRevision, "challenge-1", stored.PairCitations[0].OutcomeID, "changed replay", time.Now()); err == nil {
		t.Fatal("changed replay accepted")
	}
	visible, err := sink.ReadPreferenceChallenges(ctx, "owner-one", "project-one", workspace, "agent-one", entries[0].ID)
	if err != nil || len(visible) != 4 {
		t.Fatalf("challenge dialogue inspection rows=%d err=%v", len(visible), err)
	}
	if _, err := sink.ReadPreferenceChallenges(ctx, "other-owner", "project-one", workspace, "agent-one", entries[0].ID); err == nil {
		t.Fatal("wrong owner challenge inspection accepted")
	}
	forged := stored
	forged.PairCitations = append([]freewillPreferencePairMetadata(nil), stored.PairCitations...)
	forged.PairCitations[0].OutcomeID = "forged-unrelated-source"
	forgedBytes, _ := json.Marshal(forged)
	preferenceScopeID := freewillPreferenceScopeID("project-one", facts.workspaceID, "agent-one")
	forgedID, err := facts.store.Local.Store(ctx, memory.Memory{Content: string(forgedBytes), Summary: "Forged preference citation", Category: "preference_hypothesis", Tier: "persist", Source: freewillPreferenceHypothesisSource, UserID: "owner-one", ProjectID: "project-one", TaskID: preferenceScopeID, AgentID: "agent-one", Metadata: map[string]string{"correlation_id": preferenceScopeID}})
	if err != nil {
		t.Fatal(err)
	}
	viewValue, err := sink.ReadPreferenceEvidence(ctx, "owner-one", "project-one", workspace, "agent-one", forgedID)
	if err != nil {
		t.Fatal(err)
	}
	view, ok := viewValue.(freewillPreferenceEvidenceView)
	if !ok || view.SourceState != "stale" || view.SelectionEligible || len(view.ObservedPairs) != 2 {
		t.Fatalf("stale evidence was not explicit or became eligible: %#v", viewValue)
	}
	malformed := stored
	malformed.Version = 99
	malformedBytes, _ := json.Marshal(malformed)
	malformedID, err := facts.store.Local.Store(ctx, memory.Memory{Content: string(malformedBytes), Summary: "Malformed scoped preference", Category: "preference_hypothesis", Tier: "persist", Source: freewillPreferenceHypothesisSource, UserID: "owner-one", ProjectID: "project-one", TaskID: preferenceScopeID, AgentID: "agent-one", Metadata: map[string]string{"correlation_id": preferenceScopeID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.ReadPreferenceEvidence(ctx, "owner-one", "project-one", workspace, "agent-one", malformedID); err == nil {
		t.Fatal("malformed scoped preference record was exposed")
	}
	entries, err = sink.ReadOwnerPreference(ctx, "owner-one", "project-one", workspace, "agent-one")
	if err != nil || len(entries) != 1 || entries[0].ID != preferenceID {
		t.Fatalf("read returned forged citation record: records=%d err=%v", len(entries), err)
	}
	if _, err := sink.ReadOwnerPreference(ctx, "other-owner", "project-one", workspace, "agent-one"); err == nil {
		t.Fatal("wrong owner read accepted")
	}
	if _, err := sink.ReadOwnerPreference(ctx, "owner-one", "other-project", workspace, "agent-one"); err == nil {
		t.Fatal("wrong project read accepted")
	}
	if _, err := sink.ReadOwnerPreference(ctx, "owner-one", "project-one", t.TempDir(), "agent-one"); err == nil {
		t.Fatal("wrong workspace read accepted")
	}
	if _, err := sink.ReadOwnerPreference(ctx, "owner-one", "project-one", workspace, "other-agent"); err == nil {
		t.Fatal("wrong agent read accepted")
	}
	neighborID, err := facts.store.Local.Store(ctx, memory.Memory{Content: "neighbor scope must remain", Summary: "Neighbor preference", Category: "preference_hypothesis", Tier: "persist", Source: freewillPreferenceHypothesisSource, UserID: "owner-one", ProjectID: "project-one", TaskID: freewillPreferenceScopeID("project-one", facts.workspaceID, "neighbor-agent"), AgentID: "neighbor-agent"})
	if err != nil {
		t.Fatal(err)
	}
	localStore := facts.store.Local.(*memory.MarkdownStore)
	// A different, non-allowlisted record shares the derived ID and lives in
	// an earlier date file. ListRecent's first-seen ID deduplication used to
	// hide the actual derived row from the withdrawal selector.
	if _, err := localStore.Store(ctx, memory.Memory{ID: preferenceID, Content: "unrelated retained body", Summary: "unrelated duplicate ID", Category: "fact", Tier: "persist", Source: "synthetic-unrelated", UserID: "owner-one", ProjectID: "project-one", TaskID: preferenceScopeID, AgentID: "agent-one", Metadata: map[string]string{"correlation_id": preferenceScopeID}, CreatedAt: time.Now().Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if listed, err := localStore.ListRecent(ctx, 0); err != nil {
		t.Fatal(err)
	} else {
		for _, row := range listed {
			if row.ID == preferenceID && row.Source == freewillPreferenceHypothesisSource {
				t.Fatal("fixture failed to reproduce ListRecent duplicate suppression")
			}
		}
	}
	if raw, err := localStore.ListScopedRecords(ctx, memory.Scope{UserID: "owner-one", IncludeUserScope: true, ProjectID: "project-one", TaskID: preferenceScopeID, AgentID: "agent-one", CorrelationID: preferenceScopeID}); err != nil {
		t.Fatal(err)
	} else {
		derivedFound, unrelatedFound := false, false
		for _, row := range raw {
			if row.ID == preferenceID && row.Source == freewillPreferenceHypothesisSource {
				derivedFound = true
			}
			if row.ID == preferenceID && row.Source == "synthetic-unrelated" {
				unrelatedFound = true
			}
		}
		if !derivedFound || !unrelatedFound {
			t.Fatalf("raw scoped enumeration missed duplicate rows: derived=%v unrelated=%v", derivedFound, unrelatedFound)
		}
	}
	badIndexParent := filepath.Join(dataDir, "not-a-directory")
	if err := os.WriteFile(badIndexParent, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	index := memory.NewEmbeddingIndex(memory.EmbeddingConfig{IndexPath: filepath.Join(badIndexParent, "embeddings.json"), Dimensions: 1, Model: "synthetic"})
	localStore.SetEmbeddingIndex(index)
	status, err := sink.WithdrawPreferenceEvidence(ctx, "owner-one", "project-one", workspace, "agent-one", time.Now())
	if err == nil || status != "pending" {
		t.Fatalf("injected sidecar failure status=%q err=%v", status, err)
	}
	localStore.SetEmbeddingIndex(memory.NewEmbeddingIndex(memory.EmbeddingConfig{IndexPath: filepath.Join(dataDir, "freewill-terminal-facts", facts.workspaceID, "memory", "embeddings.json"), Dimensions: 1, Model: "synthetic"}))
	status, err = sink.WithdrawPreferenceEvidence(ctx, "owner-one", "project-one", workspace, "agent-one", time.Now().Add(time.Minute))
	if err == nil || status != "pending" {
		t.Fatalf("ambiguous duplicate ID with embedding index must remain pending: status=%q err=%v", status, err)
	}
	localStore.SetEmbeddingIndex(nil)
	status, err = sink.WithdrawPreferenceEvidence(ctx, "owner-one", "project-one", workspace, "agent-one", time.Now().Add(2*time.Minute))
	if err != nil || status != "complete" {
		t.Fatalf("withdrawal status=%q err=%v", status, err)
	}
	if _, err := sink.WithdrawPreferenceEvidence(ctx, "owner-one", "project-one", workspace, "agent-one", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("idempotent withdrawal: %v", err)
	}
	if _, err := sink.ReadPreferenceEvidence(ctx, "owner-one", "project-one", workspace, "agent-one", preferenceID); err == nil {
		t.Fatal("withdrawn evidence remained readable")
	}
	if rows, err := facts.store.Local.ListRecent(ctx, 0); err != nil {
		t.Fatal(err)
	} else {
		for _, row := range rows {
			if row.UserID == "owner-one" && row.ProjectID == "project-one" && row.AgentID == "agent-one" && row.TaskID == preferenceScopeID && row.Source == freewillPreferenceHypothesisSource && (row.Category == "preference_hypothesis" || row.Category == "preference_revision") || row.UserID == "owner-one" && row.ProjectID == "project-one" && row.AgentID == "agent-one" && row.TaskID == preferenceScopeID && row.Source == freewillPreferenceChallengeSource && row.Category == "preference_challenge" {
				t.Fatalf("derived preference body still listed: %#v", row)
			}
		}
	}
	for _, citation := range stored.PairCitations {
		for _, id := range []string{citation.OutcomeID, citation.SelfReportID} {
			if retained, err := facts.store.Local.Get(ctx, id); err != nil || retained == nil {
				t.Fatalf("cited task history %s was removed: %#v %v", id, retained, err)
			}
		}
	}
	if retained, err := facts.store.Local.Get(ctx, neighborID); err != nil || retained == nil {
		t.Fatalf("neighbor scope was removed: %#v %v", retained, err)
	}
	if retained, err := facts.store.Local.Get(ctx, preferenceID); err != nil || retained == nil || retained.Source != "synthetic-unrelated" || retained.Content != "unrelated retained body" {
		t.Fatalf("unrelated same-ID row was not preserved: %#v %v", retained, err)
	}
	if found, err := facts.store.Local.Search(ctx, "exploratory tasks", 0); err != nil {
		t.Fatal(err)
	} else {
		for _, row := range found {
			if row.ID == preferenceID || row.ID == neighborID {
				t.Fatalf("search returned erased/neighbor records unexpectedly: %#v", row)
			}
		}
	}
	memoryDir := filepath.Join(dataDir, "freewill-terminal-facts", facts.workspaceID, "memory")
	files, err := os.ReadDir(memoryDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".md" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(memoryDir, file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if strings.Contains(text, "unrelated retained body") {
			continue
		}
		if strings.Contains(text, "The two appraisals may indicate interest") || strings.Contains(text, "This misses that I preferred") || strings.Contains(text, "host_revision_high_uncertainty") {
			t.Fatalf("derived preference body remained on disk: %s", file.Name())
		}
	}
	if err := sink.DerivePreference(ctx, trigger); err == nil {
		t.Fatal("withdrawn preference was re-derived")
	}
}

func seedRetentionPreference(t *testing.T, retentionSeconds int) (*freewillPreferenceHypothesisSink, *freewillTerminalFactMemorySink, *event.SQLiteEventStore, string, time.Time) {
	t.Helper()
	ctx := context.Background()
	dataDir, workspace := t.TempDir(), t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	facts, err := newFreewillTerminalFactMemorySink(dataDir, "project-one", workspace, events, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{narrative: "synthetic", provider: "local-fake", model: "appraisal", version: "v1"}, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	selected := make([]string, 0, 2)
	for i, kind := range []string{"curiosity", "satisfaction"} {
		child := "retention-task-" + string(rune('1'+i))
		taskID := freewillTaskScopeID("project-one", facts.workspace, child)
		selected = append(selected, taskID)
		outcome := freewillTaskOutcome{Version: "freewill-task-outcome/v1", Kind: "host_task_outcome", ProjectID: "project-one", WorkspaceID: facts.workspaceID, Workspace: facts.workspace, AgentID: "agent-one", TaskID: taskID, ParentRunID: "retention-parent-" + child, ChildRunID: child, Status: "completed", Validation: "passed", Confidence: "host_validated_terminal_receipt", RecordedAt: base}
		ob, _ := json.Marshal(outcome)
		oid, err := facts.store.Local.Store(ctx, memory.Memory{Content: string(ob), Summary: "host task outcome", Category: "fact", Tier: "persist", Source: freewillTaskOutcomeSource, UserID: "owner-one", ProjectID: "project-one", TaskID: taskID, AgentID: "agent-one"})
		if err != nil {
			t.Fatal(err)
		}
		report := freewillTaskAppraisal{Version: 1, Kind: "model_task_self_report", OutcomeID: oid, ProjectID: "project-one", WorkspaceID: facts.workspaceID, TaskID: taskID, ParentRunID: outcome.ParentRunID, ChildRunID: child, Provider: "local-fake", Model: "appraisal", ModelVersion: "v1", Confidence: "model_self_report", Uncertainty: "synthetic", PerceivedOutcome: "completed", AppraisalKind: kind, Narrative: "synthetic", RecordedAt: base}
		rb, _ := json.Marshal(report)
		if _, err := facts.store.Local.Store(ctx, memory.Memory{Content: string(rb), Summary: "self report", Category: "appraisal", Tier: "persist", Source: freewillTaskAppraisalSource, UserID: "owner-one", ProjectID: "project-one", TaskID: taskID, AgentID: "agent-one"}); err != nil {
			t.Fatal(err)
		}
	}
	provider := &freewillPreferenceFake{}
	sink, err := newFreewillPreferenceHypothesisSink(appraisals, provider, "owner-one", selected, retentionSeconds)
	if err != nil {
		t.Fatal(err)
	}
	sink.now = func() time.Time { return base }
	trigger := multiagent.FreewillTerminalFact{ProjectID: "project-one", Workspace: workspace, AgentID: "agent-one", ParentRunID: "retention-parent", ChildRunID: "retention-trigger"}
	if err := sink.DerivePreference(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	return sink, facts, events, workspace, base
}

func addRetentionSourcePair(t *testing.T, facts *freewillTerminalFactMemorySink, created time.Time, suffix, kind string) string {
	t.Helper()
	ctx := context.Background()
	child := "new-source-" + suffix
	taskID := freewillTaskScopeID("project-one", facts.workspace, child)
	outcome := freewillTaskOutcome{Version: "freewill-task-outcome/v1", Kind: "host_task_outcome", ProjectID: "project-one", WorkspaceID: facts.workspaceID, Workspace: facts.workspace, AgentID: "agent-one", TaskID: taskID, ParentRunID: "new-parent-" + suffix, ChildRunID: child, Status: "completed", Validation: "passed", Confidence: "host_validated_terminal_receipt", RecordedAt: created}
	ob, _ := json.Marshal(outcome)
	oid, err := facts.store.Local.Store(ctx, memory.Memory{Content: string(ob), Summary: "host task outcome", Category: "fact", Tier: "persist", Source: freewillTaskOutcomeSource, UserID: "owner-one", ProjectID: "project-one", TaskID: taskID, AgentID: "agent-one"})
	if err != nil {
		t.Fatal(err)
	}
	report := freewillTaskAppraisal{Version: 1, Kind: "model_task_self_report", OutcomeID: oid, ProjectID: "project-one", WorkspaceID: facts.workspaceID, TaskID: taskID, ParentRunID: outcome.ParentRunID, ChildRunID: child, Provider: "local-fake", Model: "appraisal", ModelVersion: "v1", Confidence: "model_self_report", Uncertainty: "synthetic", PerceivedOutcome: "completed", AppraisalKind: kind, Narrative: "synthetic", RecordedAt: created}
	rb, _ := json.Marshal(report)
	if _, err := facts.store.Local.Store(ctx, memory.Memory{Content: string(rb), Summary: "self report", Category: "appraisal", Tier: "persist", Source: freewillTaskAppraisalSource, UserID: "owner-one", ProjectID: "project-one", TaskID: taskID, AgentID: "agent-one"}); err != nil {
		t.Fatal(err)
	}
	return taskID
}

func TestFreewillPreferenceRetentionFutureDueAndTaskHistory(t *testing.T) {
	ctx := context.Background()
	sink, facts, events, workspace, base := seedRetentionPreference(t, 3600)
	defer events.Close()
	entries, err := sink.ReadOwnerPreference(ctx, "owner-one", "project-one", workspace, "agent-one")
	if err != nil || len(entries) != 1 {
		t.Fatalf("future preference=%d err=%v", len(entries), err)
	}
	var hypothesis freewillPreferenceHypothesis
	if err := json.Unmarshal([]byte(entries[0].Content), &hypothesis); err != nil {
		t.Fatal(err)
	}
	challenge, _, err := sink.ChallengePreference(ctx, "owner-one", "project-one", workspace, "agent-one", entries[0].ID, freewillBindingDigest(entries[0].ID, entries[0].Content, entries[0].Source), "expire-challenge", hypothesis.PairCitations[0].OutcomeID, "Synthetic challenge before expiry.", base)
	if err != nil || len(challenge) == 0 {
		t.Fatalf("challenge before expiry: %s err=%v", challenge, err)
	}
	sink.now = func() time.Time { return base.Add(59 * time.Minute) }
	if _, err := sink.ReadPreferenceEvidence(ctx, "owner-one", "project-one", workspace, "agent-one", entries[0].ID); err != nil {
		t.Fatalf("future evidence was unavailable: %v", err)
	}
	sink.now = func() time.Time { return base.Add(61 * time.Minute) }
	if _, err := sink.ReadPreferenceEvidence(ctx, "owner-one", "project-one", workspace, "agent-one", entries[0].ID); err == nil {
		t.Fatal("due evidence remained readable")
	}
	if _, err := sink.ReadPreferenceChallenges(ctx, "owner-one", "project-one", workspace, "agent-one", entries[0].ID); err == nil {
		t.Fatal("due challenge route remained readable")
	}
	if _, _, err := sink.ChallengePreference(ctx, "owner-one", "project-one", workspace, "agent-one", entries[0].ID, "stale", "after-expiry", hypothesis.PairCitations[0].OutcomeID, "must fail", base.Add(61*time.Minute)); err == nil {
		t.Fatal("due challenge write was accepted")
	}
	scope := memory.Scope{UserID: "owner-one", IncludeUserScope: true, ProjectID: "project-one", TaskID: freewillPreferenceScopeID("project-one", facts.workspaceID, "agent-one"), AgentID: "agent-one", CorrelationID: freewillPreferenceScopeID("project-one", facts.workspaceID, "agent-one")}
	rows, err := facts.store.Local.(interface {
		ListScopedRecords(context.Context, memory.Scope) ([]memory.Memory, error)
	}).ListScopedRecords(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Source == freewillPreferenceHypothesisSource && (row.Category == "preference_hypothesis" || row.Category == "preference_revision") || row.Source == freewillPreferenceChallengeSource && row.Category == "preference_challenge" {
			t.Fatalf("expired derived row remains: %#v", row)
		}
	}
	history, err := facts.store.Local.ListRecent(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, citation := range hypothesis.PairCitations {
		for _, row := range history {
			if row.ID == citation.OutcomeID || row.ID == citation.SelfReportID {
				goto found
			}
		}
		t.Fatalf("cited task history %s was deleted", citation.OutcomeID)
	found:
	}
	if withdrawn, err := facts.store.Local.(interface {
		ScopeErased(context.Context, memory.Scope) (bool, error)
	}).ScopeErased(ctx, scope); err != nil || withdrawn {
		t.Fatalf("ordinary expiry became withdrawal: %v err=%v", withdrawn, err)
	}
	priorCalls := sink.provider.(*freewillPreferenceFake).calls
	if err := sink.DerivePreference(ctx, multiagent.FreewillTerminalFact{ProjectID: "project-one", Workspace: workspace, AgentID: "agent-one", ParentRunID: "retention-parent", ChildRunID: "retention-trigger"}); err != nil {
		t.Fatalf("same-source retry after expiry: %v", err)
	}
	if got := sink.provider.(*freewillPreferenceFake).calls; got != priorCalls {
		t.Fatalf("same expired source pairs reached provider again: calls=%d want=%d", got, priorCalls)
	}
	newIDs := []string{addRetentionSourcePair(t, facts, base.Add(time.Hour), "three", "curiosity"), addRetentionSourcePair(t, facts, base.Add(time.Hour), "four", "satisfaction")}
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{narrative: "unused", provider: "local-fake", model: "appraisal", version: "v1"}, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	newSink, err := newFreewillPreferenceHypothesisSink(appraisals, &freewillPreferenceFake{}, "owner-one", newIDs, 3600)
	if err != nil {
		t.Fatal(err)
	}
	newSink.now = sink.now
	if err := newSink.DerivePreference(ctx, multiagent.FreewillTerminalFact{ProjectID: "project-one", Workspace: workspace, AgentID: "agent-one", ParentRunID: "new-pair-parent", ChildRunID: "new-pair-trigger"}); err != nil {
		t.Fatalf("new source pairs did not allow a fresh hypothesis: %v", err)
	}
	newEntries, err := newSink.ReadOwnerPreference(ctx, "owner-one", "project-one", workspace, "agent-one")
	if err != nil || len(newEntries) != 1 || newEntries[0].ID == entries[0].ID {
		t.Fatalf("new pair preference IDs=%v err=%v", func() []string {
			ids := []string{}
			for _, e := range newEntries {
				ids = append(ids, e.ID)
			}
			return ids
		}(), err)
	}
}

func TestFreewillPreferenceRetentionDisabledAndLegacyTimestampFallback(t *testing.T) {
	ctx := context.Background()
	legacyOff, _, eventsOff, workspaceOff, baseOff := seedRetentionPreference(t, 0)
	legacyOff.now = func() time.Time { return baseOff.Add(20 * 365 * 24 * time.Hour) }
	if _, err := legacyOff.ReadOwnerPreference(ctx, "owner-one", "project-one", workspaceOff, "agent-one"); err != nil {
		t.Fatalf("unset retention changed existing preference reads: %v", err)
	}
	if err := eventsOff.Close(); err != nil {
		t.Fatal(err)
	}

	sink, facts, events, workspace, base := seedRetentionPreference(t, 3600)
	defer events.Close()
	scopeID := freewillPreferenceScopeID("project-one", facts.workspaceID, "agent-one")
	legacy := freewillPreferenceRevision{Version: 1, Kind: "preference_hypothesis_challenge_revision"}
	legacyBytes, _ := json.Marshal(legacy)
	if _, err := facts.store.Local.Store(ctx, memory.Memory{ID: "legacy-preference-revision", Content: string(legacyBytes), Summary: "legacy revision", Category: "preference_revision", Source: freewillPreferenceHypothesisSource, UserID: "owner-one", ProjectID: "project-one", TaskID: scopeID, AgentID: "agent-one", CreatedAt: base.Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	sink.now = func() time.Time { return base }
	if entries, err := sink.ReadOwnerPreference(ctx, "owner-one", "project-one", workspace, "agent-one"); err != nil || len(entries) != 1 {
		t.Fatalf("valid preference with legacy row=%d err=%v", len(entries), err)
	}
	rows, err := facts.store.Local.(interface {
		ListScopedRecords(context.Context, memory.Scope) ([]memory.Memory, error)
	}).ListScopedRecords(ctx, memory.Scope{UserID: "owner-one", IncludeUserScope: true, ProjectID: "project-one", TaskID: scopeID, AgentID: "agent-one", CorrelationID: scopeID})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == "legacy-preference-revision" {
			t.Fatal("legacy row exceeded retention because timestamp fallback was ignored")
		}
	}
}

func TestFreewillPreferenceRetentionDeadlineSurvivesMissingSidecarAndConfigOff(t *testing.T) {
	ctx := context.Background()
	_, facts, events, workspace, base := seedRetentionPreference(t, 60)
	if err := events.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Join(facts.factsRoot, "preference-retention"))
	if err != nil || len(files) != 1 {
		t.Fatalf("retention sidecar count=%d err=%v", len(files), err)
	}
	// Simulate a process stop after the derived body commit but before its
	// first retention sidecar commit. The host timestamp/deadline is embedded
	// in the body and remains authoritative after config is turned off.
	if err := os.Remove(filepath.Join(facts.factsRoot, "preference-retention", files[0].Name())); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Dir(filepath.Dir(facts.factsRoot))
	events2, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events2.Close()
	facts2, err := newFreewillTerminalFactMemorySink(dataDir, "project-one", workspace, events2, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts2, &freewillRetrospectiveFake{narrative: "unused", provider: "local-fake", model: "appraisal", version: "v1"}, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := newFreewillPreferenceHypothesisSink(appraisals, &freewillPreferenceFake{}, "owner-one", []string{"task-one", "task-two"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return base.Add(2 * time.Minute) }
	if err := restarted.ExpireDuePreferenceRecords(ctx); err != nil {
		t.Fatalf("deadline did not recover with setting off: %v", err)
	}
	if _, err := restarted.ReadOwnerPreference(ctx, "owner-one", "project-one", workspace, "agent-one"); err == nil {
		t.Fatal("body without a sidecar survived expired embedded deadline")
	}
	data, err := os.ReadFile(filepath.Join(facts2.factsRoot, "preference-retention", files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var ledger freewillPreferenceRetentionLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatal(err)
	}
	if len(ledger.Records) == 0 || ledger.Records[0].State != "expired" {
		t.Fatalf("recovered retention ledger=%#v", ledger)
	}
}

type retentionPrimary struct{}

func (retentionPrimary) Capture(context.Context, memory.Memory) (string, error) { return "", nil }
func (retentionPrimary) Search(context.Context, memory.SearchRequest) ([]memory.Memory, error) {
	return nil, nil
}

func TestFreewillPreferenceRetentionPendingPrimarySurvivesRestart(t *testing.T) {
	ctx := context.Background()
	sink, facts, events, workspace, base := seedRetentionPreference(t, 60)
	facts.store.Primary = retentionPrimary{}
	sink.now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, err := sink.ReadOwnerPreference(ctx, "owner-one", "project-one", workspace, "agent-one"); err == nil {
		t.Fatal("configured primary cleanup was claimed complete")
	}
	files, err := os.ReadDir(filepath.Join(facts.factsRoot, "preference-retention"))
	if err != nil || len(files) != 1 {
		t.Fatalf("retention state files=%d err=%v", len(files), err)
	}
	data, err := os.ReadFile(filepath.Join(facts.factsRoot, "preference-retention", files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var ledger freewillPreferenceRetentionLedger
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatal(err)
	}
	if len(ledger.Records) == 0 || ledger.Records[0].State != "pending" || ledger.OwnerID != "owner-one" || ledger.AgentID != "agent-one" {
		t.Fatalf("pending retention state=%#v", ledger)
	}
	if err := events.Close(); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Dir(filepath.Dir(facts.factsRoot))
	events2, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events2.Close()
	facts2, err := newFreewillTerminalFactMemorySink(dataDir, "project-one", workspace, events2, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	appraisals2, err := newFreewillTaskAppraisalSink(facts2, &freewillRetrospectiveFake{narrative: "unused", provider: "local-fake", model: "appraisal", version: "v1"}, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	sink2, err := newFreewillPreferenceHypothesisSink(appraisals2, &freewillPreferenceFake{}, "owner-one", []string{"task-one", "task-two"}, 60)
	if err != nil {
		t.Fatal(err)
	}
	sink2.now = func() time.Time { return base } // rollback must not unblock a durable pending expiry.
	facts2.store.Primary = retentionPrimary{}
	if err := sink2.ExpireDuePreferenceRecords(ctx); err == nil {
		t.Fatal("pending cleanup became complete with a configured primary")
	}
	facts2.store.Primary = nil
	if err := sink2.ExpireDuePreferenceRecords(ctx); err != nil {
		t.Fatalf("restart retry did not finish local cleanup: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(facts2.factsRoot, "preference-retention", files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		t.Fatal(err)
	}
	for _, record := range ledger.Records {
		if record.State != "expired" {
			t.Fatalf("retry state=%#v", record)
		}
	}
	if _, err := sink2.ReadOwnerPreference(ctx, "owner-one", "project-one", workspace, "agent-one"); err == nil {
		t.Fatal("expired preference became readable after restart")
	}
	facts2.store.Primary = retentionPrimary{}
	if _, err := sink2.ReadOwnerPreference(ctx, "owner-one", "project-one", workspace, "agent-one"); err == nil {
		t.Fatal("later primary attachment exposed an expired local preference")
	}
}
