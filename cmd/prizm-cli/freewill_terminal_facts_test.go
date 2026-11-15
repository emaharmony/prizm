package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func freewillSyntheticTaskScores() map[string]int {
	return map[string]int{"fun": 3, "meaning": 4, "challenge": 2, "reward": 4, "satisfaction": 3, "curiosity": 5}
}

func TestFreewillTerminalFactMemoryIsLocalDurableAndBoundToIntentionWorkspace(t *testing.T) {
	dataDir := t.TempDir()
	workspace := t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	blockingEvents := &blockingFreewillFactEventStore{EventStore: events, started: started, release: release}
	sink, err := newFreewillTerminalFactMemorySink(dataDir, "project-one", workspace, blockingEvents)
	if err != nil {
		t.Fatal(err)
	}
	secondSink, err := newFreewillTerminalFactMemorySink(dataDir, "project-one", workspace, blockingEvents)
	if err != nil {
		t.Fatal(err)
	}
	contended := make(chan struct{})
	secondSink.claimer = &observedFreewillFactClaimer{base: secondSink.claimer, contended: contended}
	fact := multiagent.FreewillTerminalFact{
		ProjectID: "project-one", Workspace: workspace, AgentID: "agent-one", IntentionID: "candidate-17",
		ParentRunID: "parent-run-1", ChildRunID: "child-run-1", Status: "completed",
		ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("a", 64),
		ValidationSummary: "passed", RecordedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- sink.CaptureFreewillTerminalFact(context.Background(), fact) }()
	<-started
	secondDone := make(chan error, 1)
	go func() { secondDone <- secondSink.CaptureFreewillTerminalFact(context.Background(), fact) }()
	select {
	case <-contended:
	case <-time.After(2 * time.Second):
		t.Fatal("second sink did not observe the held process-safe claim")
	}
	unblock()
	if err := <-firstDone; err != nil {
		t.Fatalf("first sink capture: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("contending sink capture: %v", err)
	}
	// Reopen the Markdown store to prove local persistence survives sink recreation.
	sink, err = newFreewillTerminalFactMemorySink(dataDir, "project-one", workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	query := func(scope memory.Scope) []memory.Memory {
		t.Helper()
		items, _, searchErr := sink.store.Search(context.Background(), memory.SearchRequest{Scope: scope, Query: "host terminal outcome", Limit: 10})
		if searchErr != nil {
			t.Fatalf("scoped search: %v", searchErr)
		}
		return items
	}
	exact, err := sink.scopeFor(fact.ProjectID, workspace, fact.IntentionID, fact.AgentID, fact.ParentRunID)
	if err != nil {
		t.Fatal(err)
	}
	items := query(exact)
	if len(items) != 1 || !strings.Contains(items[0].Content, fact.ReportSHA256) || strings.Contains(items[0].Content, "raw report body") {
		t.Fatalf("exact scoped durable fact = %#v", items)
	}
	for _, eventType := range []string{memory.EventScopedCaptureStarted, memory.EventScopedCapturePersisted, memory.EventScopedCaptureSyncPending} {
		lifecycleEvents, queryErr := events.Query(context.Background(), event.EventFilter{Type: eventType, Limit: 10})
		if queryErr != nil || len(lifecycleEvents) != 1 {
			t.Fatalf("same-parent concurrent capture %s events=%d err=%v want=1", eventType, len(lifecycleEvents), queryErr)
		}
	}
	otherIntention := exact
	otherIntention.TaskID = freewillTerminalTaskID(fact.ProjectID, sink.workspace, "candidate-18")
	if got := query(otherIntention); len(got) != 0 {
		t.Fatalf("cross-intention retrieval returned %d facts", len(got))
	}
	otherWorkspacePath := t.TempDir()
	otherWorkspaceCanonical, err := canonicalFreewillWorkspace(otherWorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	otherWorkspace := exact
	otherWorkspace.TaskID = freewillTerminalTaskID(fact.ProjectID, otherWorkspaceCanonical, fact.IntentionID)
	if got := query(otherWorkspace); len(got) != 0 {
		t.Fatalf("cross-workspace retrieval returned %d facts", len(got))
	}
	otherProject := exact
	otherProject.ProjectID = "project-two"
	otherProject.TaskID = freewillTerminalTaskID(otherProject.ProjectID, sink.workspace, fact.IntentionID)
	if got := query(otherProject); len(got) != 0 {
		t.Fatalf("cross-project retrieval returned %d facts", len(got))
	}
	if err := sink.CaptureFreewillTerminalFact(context.Background(), multiagent.FreewillTerminalFact{
		ProjectID: "project-one", Workspace: t.TempDir(), AgentID: fact.AgentID, IntentionID: fact.IntentionID,
		ParentRunID: "other-parent", ChildRunID: "other-child", Status: "completed", ReportRef: "multiagent_report.json",
		ReportSHA256: strings.Repeat("b", 64), ValidationSummary: "passed", RecordedAt: fact.RecordedAt,
	}); err == nil {
		t.Fatal("capture accepted a workspace outside the opted-in binding")
	}
}

type blockingFreewillFactEventStore struct {
	event.EventStore
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

type observedFreewillFactClaimer struct {
	base      freewillTerminalFactClaimer
	contended chan struct{}
	once      sync.Once
}

func (c *observedFreewillFactClaimer) Acquire(ctx context.Context, key string) (multiagent.ExecutionClaim, error) {
	claim, err := c.base.Acquire(ctx, key)
	if errors.Is(err, multiagent.ErrRunClaimed) {
		c.once.Do(func() { close(c.contended) })
	}
	return claim, err
}

func (s *blockingFreewillFactEventStore) Store(ctx context.Context, item event.Event) error {
	if item.Type == memory.EventScopedCaptureStarted {
		s.once.Do(func() {
			close(s.started)
			<-s.release
		})
	}
	return s.EventStore.Store(ctx, item)
}

func TestFreewillTerminalFactOptInBindsExactProjectAndResolvedPath(t *testing.T) {
	workspace := t.TempDir()
	if freewillTerminalFactsBindingMatches("project-one", workspace, "", workspace) {
		t.Fatal("empty project binding opted in")
	}
	if freewillTerminalFactsBindingMatches("project-one", workspace, "project-two", workspace) {
		t.Fatal("different project binding opted in")
	}
	if !freewillTerminalFactsBindingMatches("project-one", workspace, "project-one", workspace) {
		t.Fatal("exact project and path binding was not accepted")
	}
}

func TestFreewillTerminalDiaryCaptureSerializesProviderAcrossIndependentSinks(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	workspace := t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	factsA, err := newFreewillTerminalFactMemorySink(dataDir, "project-one", workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	factsB, err := newFreewillTerminalFactMemorySink(dataDir, "project-one", workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	fact := multiagent.FreewillTerminalFact{
		ProjectID: "project-one", Workspace: workspace, AgentID: "agent-one", IntentionID: "intention-one",
		ParentRunID: "parent-run-one", ChildRunID: "child-run-one", Status: "completed",
		ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("a", 64),
		ValidationSummary: "passed", RecordedAt: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC),
	}
	if err := factsA.CaptureFreewillTerminalFact(ctx, fact); err != nil {
		t.Fatal(err)
	}
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	providerA := &freewillRetrospectiveFake{narrative: "I recall a different outcome.", provider: "local-fake", model: "synthetic-retrospective", version: "fixture-v1",
		started: providerStarted, release: releaseProvider}
	providerB := &freewillRetrospectiveFake{narrative: "A second synthetic narrative.", provider: "local-fake", model: "synthetic-retrospective", version: "fixture-v1"}
	diaryA, err := newFreewillTerminalDiarySink(factsA, providerA, "synthetic-owner")
	if err != nil {
		t.Fatal(err)
	}
	diaryB, err := newFreewillTerminalDiarySink(factsB, providerB, "synthetic-owner")
	if err != nil {
		t.Fatal(err)
	}
	contended := make(chan struct{})
	factsB.claimer = &observedFreewillFactClaimer{base: factsB.claimer, contended: contended}
	firstDone := make(chan error, 1)
	go func() { firstDone <- diaryA.CaptureFreewillTerminalRetrospective(ctx, fact) }()
	<-providerStarted
	secondDone := make(chan error, 1)
	go func() { secondDone <- diaryB.CaptureFreewillTerminalRetrospective(ctx, fact) }()
	select {
	case <-contended:
	case <-time.After(2 * time.Second):
		close(releaseProvider)
		t.Fatal("second diary sink did not contend on the shared claim")
	}
	close(releaseProvider)
	if err := <-firstDone; err != nil {
		t.Fatalf("first retrospective capture: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("contending retrospective capture: %v", err)
	}
	if providerA.calls+providerB.calls != 1 {
		t.Fatalf("provider calls A=%d B=%d; want exactly one", providerA.calls, providerB.calls)
	}
	scope, err := factsA.scopeFor(fact.ProjectID, workspace, fact.IntentionID, fact.AgentID, fact.ParentRunID)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := factsA.store.Local.ListRecent(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	factCount, diaryCount := 0, 0
	for _, entry := range entries {
		if entry.ProjectID != scope.ProjectID || entry.TaskID != scope.TaskID || entry.AgentID != scope.AgentID {
			continue
		}
		if entry.Source == "prizm:freewill-terminal" && entry.Category == "fact" {
			factCount++
		}
		if entry.Source == freewillDiarySource && entry.Category == "diary" {
			diaryCount++
		}
	}
	if factCount != 1 || diaryCount != 1 {
		t.Fatalf("same-scope fact count=%d diary count=%d; want one each", factCount, diaryCount)
	}
	ownerEntries, err := diaryB.ReadOwnerEntries(ctx, "synthetic-owner", fact.ProjectID, workspace, fact.IntentionID, fact.AgentID, fact.ParentRunID)
	if err != nil || len(ownerEntries) != 1 {
		t.Fatalf("owner diary entries=%d err=%v; want one", len(ownerEntries), err)
	}
}

func TestFreewillTaskAppraisalPersistsSeparateHostOutcomeAndSelfReport(t *testing.T) {
	ctx := context.Background()
	dataDir, workspace := t.TempDir(), t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	{
		e := event.NewEvent(event.EventFreewillExecutionFailed, "synthetic-host", map[string]any{"child_run_id": "task-one", "child_status": "failed", "report_ref": "multiagent_report.json", "report_sha256": strings.Repeat("a", 64), "validation_summary": "failed"})
		e.Metadata.RunID = "parent-one"
		if err := events.Store(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	facts, err := newFreewillTerminalFactMemorySink(dataDir, "project-one", workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	fact := multiagent.FreewillTerminalFact{ProjectID: "project-one", Workspace: workspace, AgentID: "agent-one", IntentionID: "intention-one", ParentRunID: "parent-one", ChildRunID: "task-one", Status: "execution_failed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("a", 64), ValidationSummary: "failed", FailureCode: "task_failed", RecordedAt: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	if err := facts.CaptureFreewillTerminalFact(ctx, fact); err != nil {
		t.Fatal(err)
	}
	provider := &freewillRetrospectiveFake{narrative: `{"appraisal_kind":"satisfaction","perceived_outcome":"completed","why":"The bounded task clarified the open question.","context":"One synthetic task and its host receipt.","narrative":"The task felt successful, though I may have misread the result.","uncertainty":"One model self-report is not independent evidence.","reason_labels":["learning","felt_in_control"],"scores":{"fun":3,"meaning":4,"challenge":2,"reward":4,"satisfaction":3,"curiosity":5}}`, provider: "local-fake", model: "synthetic-task-appraisal", version: "fixture-v1"}
	sink, err := newFreewillTaskAppraisalSink(facts, provider, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Capture(ctx, fact); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("fake provider calls=%d want=1", provider.calls)
	}
	if strings.Contains(provider.fact.Content, "raw report") {
		t.Fatal("provider received raw report body")
	}
	// Reopen the owner-scoped store through a fresh sink, then read both separate records.
	facts, err = newFreewillTerminalFactMemorySink(dataDir, "project-one", workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	sink, err = newFreewillTaskAppraisalSink(facts, provider, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	taskID := freewillTaskScopeID(fact.ProjectID, facts.workspace, fact.ChildRunID)
	globalID, err := facts.store.Local.Store(ctx, memory.Memory{ID: "global-task-outcome", Content: `{"kind":"global-row"}`, Summary: "Freewill task outcome appraisal adversarial global row", Category: "fact", Tier: "persist", Source: freewillTaskOutcomeSource, ProjectID: fact.ProjectID, TaskID: taskID, AgentID: fact.AgentID, Metadata: map[string]string{"correlation_id": fact.ParentRunID}})
	if err != nil || globalID == "" {
		t.Fatalf("seed adversarial global memory row: id=%q err=%v", globalID, err)
	}
	broadScope := memory.Scope{UserID: "owner-one", IncludeUserScope: true, ProjectID: fact.ProjectID, TaskID: taskID, AgentID: fact.AgentID, CorrelationID: fact.ParentRunID}
	broadResults, _, err := facts.store.Search(ctx, memory.SearchRequest{Scope: broadScope, Query: "Freewill task outcome appraisal", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	foundGlobal := false
	for _, row := range broadResults {
		if row.ID == globalID {
			foundGlobal = true
		}
	}
	if !foundGlobal {
		t.Fatal("test setup did not demonstrate global-row scope inclusion")
	}
	entries, err := sink.ReadOwnerTask(ctx, "owner-one", fact.ProjectID, workspace, taskID, fact.AgentID, fact.ParentRunID, fact.ChildRunID)
	if err != nil || len(entries) != 2 {
		t.Fatalf("reopened task records=%d err=%v", len(entries), err)
	}
	var outcome freewillTaskOutcome
	var appraisal freewillTaskAppraisal
	for _, entry := range entries {
		switch entry.Source {
		case freewillTaskOutcomeSource:
			if err := json.Unmarshal([]byte(entry.Content), &outcome); err != nil {
				t.Fatal(err)
			}
		case freewillTaskAppraisalSource:
			if err := json.Unmarshal([]byte(entry.Content), &appraisal); err != nil {
				t.Fatal(err)
			}
		}
	}
	if outcome.Kind != "host_task_outcome" || outcome.Status != "execution_failed" || outcome.Confidence != "host_validated_terminal_receipt" || len(outcome.ParentEventRefs) != 1 || outcome.ChildRunID != fact.ChildRunID {
		t.Fatalf("host outcome lost receipt scope/provenance: %#v", outcome)
	}
	if appraisal.Kind != "model_task_self_report" || appraisal.Confidence != "model_self_report" || appraisal.OutcomeID == "" || appraisal.Uncertainty == "" || !strings.Contains(appraisal.Narrative, "successful") || len(appraisal.ReasonLabels) != 2 {
		t.Fatalf("self-report provenance/disagreement lost: %#v", appraisal)
	}
	if !reflect.DeepEqual(appraisal.Scores, freewillSyntheticTaskScores()) || appraisal.Overall == nil || appraisal.Overall.Score != 3.5 || appraisal.Overall.DimensionCount != 6 {
		t.Fatalf("appraisal scores or host-derived overall lost after reopen: %#v", appraisal)
	}
	wantSourceRefs := []string{appraisal.OutcomeID, outcome.ParentEventRefs[0]}
	if appraisal.ReasonLabels[0].Label != "learning" || appraisal.ReasonLabels[0].Class != freewillTaskReasonStandard || appraisal.ReasonLabels[0].Status != "catalogued" || appraisal.ReasonLabels[1].Label != "felt_in_control" || appraisal.ReasonLabels[1].Class != freewillTaskReasonProposed || appraisal.ReasonLabels[1].Status != freewillTaskReasonProvisional {
		t.Fatalf("standard/proposed reason distinction lost: %#v", appraisal.ReasonLabels)
	}
	for _, label := range appraisal.ReasonLabels {
		if label.Confidence != freewillTaskReasonModelHypothesis || !reflect.DeepEqual(label.SourceRefs, wantSourceRefs) {
			t.Fatalf("reason label has invalid host provenance: %#v; want refs %v", label, wantSourceRefs)
		}
	}
	if err := validateFreewillTaskReasonLabels(appraisal, outcome, appraisal.OutcomeID); err != nil {
		t.Fatalf("valid reason labels rejected after reopen: %v", err)
	}
	verifiedLabels, err := sink.ReadOwnerTaskReasonLabels(ctx, "owner-one", fact.ProjectID, workspace, taskID, fact.AgentID, fact.ParentRunID, fact.ChildRunID)
	if err != nil || !reflect.DeepEqual(verifiedLabels, appraisal.ReasonLabels) {
		t.Fatalf("exact receipt-bound reason read labels=%#v err=%v", verifiedLabels, err)
	}
	wrongRefs := appraisal
	wrongRefs.ReasonLabels = append([]freewillTaskReasonLabel(nil), appraisal.ReasonLabels...)
	wrongRefs.ReasonLabels[0].SourceRefs = []string{"model-supplied-reference"}
	if err := validateFreewillTaskReasonLabels(wrongRefs, outcome, appraisal.OutcomeID); err == nil {
		t.Fatal("reason taxonomy accepted a non-host source reference")
	}
	authorityLabel := appraisal
	authorityLabel.ReasonLabels = append([]freewillTaskReasonLabel(nil), appraisal.ReasonLabels...)
	authorityLabel.ReasonLabels[1].Label = "model_confirmed_preference"
	if err := validateFreewillTaskReasonLabels(authorityLabel, outcome, appraisal.OutcomeID); err == nil {
		t.Fatal("reason taxonomy readback accepted an authority-shaped proposed label")
	}
	if _, err := sink.ReadOwnerTask(ctx, "other-owner", fact.ProjectID, workspace, taskID, fact.AgentID, fact.ParentRunID, fact.ChildRunID); err == nil {
		t.Fatal("wrong owner read was accepted")
	}
	otherOwnerSink, err := newFreewillTaskAppraisalSink(facts, provider, "other-owner")
	if err != nil {
		t.Fatal(err)
	}
	otherOwnerRecords, err := otherOwnerSink.ReadOwnerTask(ctx, "other-owner", fact.ProjectID, workspace, taskID, fact.AgentID, fact.ParentRunID, fact.ChildRunID)
	if err != nil || len(otherOwnerRecords) != 0 {
		t.Fatalf("separate configured owner surfaced A records: count=%d err=%v", len(otherOwnerRecords), err)
	}
	if _, err := sink.ReadOwnerTask(ctx, "owner-one", "project-two", workspace, taskID, fact.AgentID, fact.ParentRunID, fact.ChildRunID); err == nil {
		t.Fatal("wrong project read was accepted")
	}
	if _, err := sink.ReadOwnerTask(ctx, "owner-one", fact.ProjectID, t.TempDir(), taskID, fact.AgentID, fact.ParentRunID, fact.ChildRunID); err == nil {
		t.Fatal("wrong workspace read was accepted")
	}
	otherTask, err := sink.ReadOwnerTask(ctx, "owner-one", fact.ProjectID, workspace, freewillTaskScopeID(fact.ProjectID, facts.workspace, "other-task"), fact.AgentID, fact.ParentRunID, "other-task")
	if err != nil || len(otherTask) != 0 {
		t.Fatalf("wrong task returned records=%d err=%v", len(otherTask), err)
	}
}

func TestFreewillTaskAppraisalPersistsBoundedQualitativeFieldsForContrastingTasks(t *testing.T) {
	ctx := context.Background()
	dataDir, workspace := t.TempDir(), t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	facts, err := newFreewillTerminalFactMemorySink(dataDir, "project-qualitative", workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	type taskFixture struct {
		fact     multiagent.FreewillTerminalFact
		response string
	}
	fixtures := []taskFixture{
		{fact: multiagent.FreewillTerminalFact{ProjectID: "project-qualitative", Workspace: workspace, AgentID: "agent-one", IntentionID: "intention-one", ParentRunID: "parent-one", ChildRunID: "child-one", Status: "completed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("b", 64), ValidationSummary: "passed", RecordedAt: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}, response: `{"appraisal_kind":"satisfaction","why":"The bounded validation closed the loop clearly.","context":"The task had one small artifact and a direct check.","narrative":"I felt satisfied with the clear completion.","uncertainty":"This is one qualitative report.","scores":{"fun":4,"meaning":3,"challenge":3,"reward":4,"satisfaction":5,"curiosity":2}}`},
		{fact: multiagent.FreewillTerminalFact{ProjectID: "project-qualitative", Workspace: workspace, AgentID: "agent-one", IntentionID: "intention-one", ParentRunID: "parent-two", ChildRunID: "child-two", Status: "execution_failed", FailureCode: "task_failed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("c", 64), ValidationSummary: "failed", RecordedAt: time.Date(2026, 11, 11, 12, 1, 0, 0, time.UTC)}, response: `{"appraisal_kind":"curiosity","why":"The failure left a useful unanswered question.","context":"The task ended before its validation passed.","narrative":"I felt curious about the unresolved behavior.","uncertainty":"This is not independently verified.","scores":{"fun":1,"meaning":4,"challenge":5,"reward":2,"satisfaction":1,"curiosity":5}}`},
	}
	for _, fixture := range fixtures {
		eventType := event.EventFreewillExecutionCompleted
		if fixture.fact.Status == "execution_failed" {
			eventType = event.EventFreewillExecutionFailed
		}
		item := event.NewEvent(eventType, "synthetic-host", map[string]any{"child_run_id": fixture.fact.ChildRunID, "report_ref": fixture.fact.ReportRef, "report_sha256": fixture.fact.ReportSHA256, "validation_summary": fixture.fact.ValidationSummary})
		item.Metadata.RunID = fixture.fact.ParentRunID
		if err := events.Store(ctx, item); err != nil {
			t.Fatal(err)
		}
		if err := facts.CaptureFreewillTerminalFact(ctx, fixture.fact); err != nil {
			t.Fatal(err)
		}
		provider := &freewillRetrospectiveFake{narrative: fixture.response, provider: "local-fake", model: "synthetic-appraisal", version: "fixture-v1"}
		sink, err := newFreewillTaskAppraisalSink(facts, provider, "owner-one")
		if err != nil {
			t.Fatal(err)
		}
		if err := sink.Capture(ctx, fixture.fact); err != nil {
			t.Fatal(err)
		}
		var input map[string]any
		if err := json.Unmarshal([]byte(provider.fact.Content), &input); err != nil {
			t.Fatal("provider received non-structured bounded outcome", err)
		}
		if len(input) != 5 || input["kind"] == nil || input["status"] == nil || input["validation"] == nil || input["evidence_id"] == nil || input["task_id"] == nil || strings.Contains(provider.fact.Content, "approved.txt content") {
			t.Fatal("provider received task/report body")
		}
	}
	facts, err = newFreewillTerminalFactMemorySink(dataDir, "project-qualitative", workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{}, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		taskID := freewillTaskScopeID(fixture.fact.ProjectID, facts.workspace, fixture.fact.ChildRunID)
		records, err := sink.ReadOwnerTask(ctx, "owner-one", fixture.fact.ProjectID, workspace, taskID, fixture.fact.AgentID, fixture.fact.ParentRunID, fixture.fact.ChildRunID)
		if err != nil || len(records) != 2 {
			t.Fatalf("task %s read count=%d err=%v", fixture.fact.ChildRunID, len(records), err)
		}
		var outcome freewillTaskOutcome
		var appraisal freewillTaskAppraisal
		for _, record := range records {
			switch record.Source {
			case freewillTaskOutcomeSource:
				if strings.Contains(record.Content, `"appraisal_kind"`) || strings.Contains(record.Content, `"why"`) || strings.Contains(record.Content, `"context"`) || strings.Contains(record.Content, `"scores"`) || strings.Contains(record.Content, `"overall"`) {
					t.Fatal("model qualitative fields contaminated the host fact")
				}
				if err := json.Unmarshal([]byte(record.Content), &outcome); err != nil {
					t.Fatal(err)
				}
			case freewillTaskAppraisalSource:
				if err := json.Unmarshal([]byte(record.Content), &appraisal); err != nil {
					t.Fatal(err)
				}
			}
		}
		if outcome.ChildRunID != fixture.fact.ChildRunID || outcome.Status != fixture.fact.Status || outcome.Validation != fixture.fact.ValidationSummary || outcome.Confidence != "host_validated_terminal_receipt" {
			t.Fatalf("host receipt fact changed for %s: %#v", fixture.fact.ChildRunID, outcome)
		}
		wantKind, wantWhy := "satisfaction", "The bounded validation closed the loop clearly."
		if fixture.fact.ChildRunID == "child-two" {
			wantKind, wantWhy = "curiosity", "The failure left a useful unanswered question."
		}
		if appraisal.Kind != "model_task_self_report" || appraisal.Confidence != "model_self_report" || appraisal.AppraisalKind != wantKind || appraisal.Why != wantWhy || appraisal.Context == "" || appraisal.Uncertainty == "" || appraisal.OutcomeID == "" {
			t.Fatalf("qualitative self-report for %s lost fields or provenance: %#v", fixture.fact.ChildRunID, appraisal)
		}
		wantScores, wantOverall := map[string]int{"fun": 4, "meaning": 3, "challenge": 3, "reward": 4, "satisfaction": 5, "curiosity": 2}, 3.5
		if fixture.fact.ChildRunID == "child-two" {
			wantScores, wantOverall = map[string]int{"fun": 1, "meaning": 4, "challenge": 5, "reward": 2, "satisfaction": 1, "curiosity": 5}, 3
		}
		if !reflect.DeepEqual(appraisal.Scores, wantScores) || appraisal.Overall == nil || appraisal.Overall.Score != wantOverall || appraisal.Overall.DimensionCount != 6 {
			t.Fatalf("multidimensional self-report/derived overall for %s mismatch: scores=%#v overall=%#v", fixture.fact.ChildRunID, appraisal.Scores, appraisal.Overall)
		}
	}
}

func TestParseFreewillTaskAppraisalResponseKeepsUnknownAndRejectsUnboundedLabels(t *testing.T) {
	legacy, err := parseFreewillTaskAppraisalResponse("An older freeform self-report.")
	if err != nil || legacy.AppraisalKind != "unknown" || legacy.Narrative != "An older freeform self-report." {
		t.Fatalf("legacy freeform compatibility: %#v err=%v", legacy, err)
	}
	unknown, err := parseFreewillTaskAppraisalResponse(`{"appraisal_kind":"unknown","why":"Not enough information.","context":"The receipt is minimal.","narrative":"I cannot tell.","uncertainty":"Evidence is insufficient.","scores":{"fun":3,"meaning":4}}`)
	if err != nil || unknown.AppraisalKind != "unknown" || unknown.PerceivedOutcome != "unknown" {
		t.Fatalf("unknown appraisal should remain admissible: %#v err=%v", unknown, err)
	}
	claim, err := parseFreewillTaskAppraisalResponse(`{"appraisal_kind":"satisfaction","perceived_outcome":"completed","why":"A tentative claim.","context":"A bounded task.","narrative":"Synthetic only.","uncertainty":"Not verified.","scores":{"meaning":4,"curiosity":2}}`)
	if err != nil || claim.PerceivedOutcome != "completed" {
		t.Fatalf("explicit perceived outcome claim = %#v err=%v", claim, err)
	}
	if _, err := parseFreewillTaskAppraisalResponse(`{"appraisal_kind":"unknown","perceived_outcome":"maybe","why":"x","context":"y","narrative":"z","uncertainty":"u","scores":{"fun":3,"meaning":4}}`); err == nil {
		t.Fatal("invalid perceived outcome label was accepted")
	}
	if _, err := parseFreewillTaskAppraisalResponse(`{"appraisal_kind":"engagement_score","why":"x","context":"y","narrative":"z","uncertainty":"u","scores":{"fun":3,"meaning":4}}`); err == nil {
		t.Fatal("unapproved appraisal label was accepted")
	}
	if _, err := parseFreewillTaskAppraisalResponse(`{"appraisal_kind":"fun","why":"","context":"y","narrative":"z","uncertainty":"u","scores":{"fun":3,"meaning":4}}`); err == nil {
		t.Fatal("empty why field was accepted")
	}
	labels, err := parseFreewillTaskAppraisalResponse(`{"appraisal_kind":"unknown","why":"A tentative reason.","context":"One bounded task.","narrative":"Synthetic only.","uncertainty":"Not verified.","reason_labels":["learning","felt_in_control"],"scores":{"meaning":4,"curiosity":2}}`)
	if err != nil || !reflect.DeepEqual(labels.ReasonLabels, []string{"learning", "felt_in_control"}) {
		t.Fatalf("bounded reason labels were rejected: %#v err=%v", labels, err)
	}
	for _, malformed := range []string{
		`{"appraisal_kind":"unknown","why":"x","context":"y","narrative":"z","uncertainty":"u","reason_labels":["learning",{"label":"trusted","status":"promoted"}],"scores":{"fun":3,"meaning":4}}`,
		`{"appraisal_kind":"unknown","why":"x","context":"y","narrative":"z","uncertainty":"u","reason_labels":["verified_completion"],"scores":{"fun":3,"meaning":4}}`,
		`{"appraisal_kind":"unknown","why":"x","context":"y","narrative":"z","uncertainty":"u","reason_labels":["model_confirmed_preference"],"scores":{"fun":3,"meaning":4}}`,
		`{"appraisal_kind":"unknown","why":"x","context":"y","narrative":"z","uncertainty":"u","status":"promoted","scores":{"fun":3,"meaning":4}}`,
		`{"appraisal_kind":"unknown","why":"x","context":"y","narrative":"z","uncertainty":"u","reason_labels":["bad-label"],"scores":{"fun":3,"meaning":4}}`,
	} {
		if _, err := parseFreewillTaskAppraisalResponse(malformed); err == nil {
			t.Fatalf("malformed or authority-shaped taxonomy output was accepted: %s", malformed)
		}
	}
	oversized := freewillTaskAppraisalResponse{AppraisalKind: "meaning", Why: strings.Repeat("w", 321), Context: "c", Narrative: "n", Uncertainty: "u", Scores: freewillSyntheticTaskScores()}
	oversizedJSON, _ := json.Marshal(oversized)
	if _, err := parseFreewillTaskAppraisalResponse(string(oversizedJSON)); err == nil {
		t.Fatal("oversized qualitative explanation was accepted")
	}
}

func TestFreewillTaskAppraisalScoresAreBoundedAndOverallIsHostDerived(t *testing.T) {
	response, err := parseFreewillTaskAppraisalResponse(`{"appraisal_kind":"meaning","why":"The task had a clear contribution.","context":"A synthetic task.","narrative":"Tentative self-report.","uncertainty":"One task is not stable evidence.","scores":{"meaning":5,"curiosity":4}}`)
	if err != nil {
		t.Fatal(err)
	}
	overall, err := deriveFreewillTaskOverall(response.Scores)
	if err != nil || overall.Score != 4.5 || overall.DimensionCount != 2 || overall.Scale != "1_to_5" || overall.Confidence != "derived_from_unverified_model_self_report" {
		t.Fatalf("host-derived overall=%#v err=%v", overall, err)
	}
	for _, input := range []string{
		`{"appraisal_kind":"meaning","why":"x","context":"y","narrative":"z","uncertainty":"u","scores":{"meaning":5}}`,
		`{"appraisal_kind":"meaning","why":"x","context":"y","narrative":"z","uncertainty":"u","scores":{"meaning":5,"purpose":4}}`,
		`{"appraisal_kind":"meaning","why":"x","context":"y","narrative":"z","uncertainty":"u","scores":{"meaning":0,"curiosity":4}}`,
		`{"appraisal_kind":"meaning","why":"x","context":"y","narrative":"z","uncertainty":"u","scores":{"meaning":6,"curiosity":4}}`,
		`{"appraisal_kind":"meaning","why":"x","context":"y","narrative":"z","uncertainty":"u","scores":{"meaning":5,"curiosity":4},"overall":{"score":4.5}}`,
	} {
		if _, err := parseFreewillTaskAppraisalResponse(input); err == nil {
			t.Fatalf("invalid or model-supplied overall appraisal was accepted: %s", input)
		}
	}
}

func TestFreewillTaskAppraisalCapturePreservesLegacyFreeformWithoutScores(t *testing.T) {
	ctx := context.Background()
	dataDir, workspace := t.TempDir(), t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	const projectID, ownerID, parentRunID, childRunID = "project-legacy-appraisal", "owner-legacy-appraisal", "parent-legacy-appraisal", "child-legacy-appraisal"
	reportSHA := strings.Repeat("d", 64)
	terminal := event.NewEvent(event.EventFreewillExecutionCompleted, "synthetic-host", map[string]any{
		"child_run_id": childRunID, "report_ref": "multiagent_report.json", "report_sha256": reportSHA, "validation_summary": "passed",
	})
	terminal.Metadata.RunID = parentRunID
	if err := events.Store(ctx, terminal); err != nil {
		t.Fatal(err)
	}
	facts, err := newFreewillTerminalFactMemorySink(dataDir, projectID, workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	fact := multiagent.FreewillTerminalFact{
		ProjectID: projectID, Workspace: workspace, AgentID: "agent-legacy-appraisal", IntentionID: "intention-legacy-appraisal",
		ParentRunID: parentRunID, ChildRunID: childRunID, Status: "completed", ReportRef: "multiagent_report.json",
		ReportSHA256: reportSHA, ValidationSummary: "passed", RecordedAt: time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC),
	}
	if err := facts.CaptureFreewillTerminalFact(ctx, fact); err != nil {
		t.Fatal(err)
	}
	provider := &freewillRetrospectiveFake{narrative: "An older freeform self-report.", provider: "local-fake", model: "legacy-appraiser", version: "fixture-v1"}
	sink, err := newFreewillTaskAppraisalSink(facts, provider, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Capture(ctx, fact); err != nil {
		t.Fatalf("capture legacy freeform appraisal: %v", err)
	}
	taskID := freewillTaskScopeID(projectID, facts.workspace, childRunID)
	records, err := sink.ReadOwnerTask(ctx, ownerID, projectID, workspace, taskID, fact.AgentID, parentRunID, childRunID)
	if err != nil {
		t.Fatal(err)
	}
	var appraisal freewillTaskAppraisal
	for _, record := range records {
		if record.Source == freewillTaskAppraisalSource {
			if err := json.Unmarshal([]byte(record.Content), &appraisal); err != nil {
				t.Fatal(err)
			}
		}
	}
	if appraisal.Kind != "model_task_self_report" || appraisal.AppraisalKind != "unknown" || appraisal.Narrative != "An older freeform self-report." ||
		appraisal.Confidence != "model_self_report" || appraisal.OutcomeID == "" || appraisal.Scores != nil || appraisal.Overall != nil {
		t.Fatalf("legacy freeform appraisal compatibility changed: %#v", appraisal)
	}
}

type freewillTaskReflectionFake struct {
	calls  int
	input  string
	output string
}

type failFirstReflectionCaptureEvents struct {
	event.EventStore
	fail bool
}

func (s *failFirstReflectionCaptureEvents) Store(ctx context.Context, item event.Event) error {
	if s.fail && item.Type == memory.EventScopedCaptureStarted {
		s.fail = false
		return errors.New("synthetic reflection capture failure")
	}
	return s.EventStore.Store(ctx, item)
}

func (p *freewillTaskReflectionFake) ReflectTask(_ context.Context, input string) (string, string, string, string, error) {
	p.calls++
	p.input = input
	if p.output == "" {
		p.output = `{"alignment":"disagreement","hypothesis":"The model report may reflect an optimistic impression despite the failed host outcome.","uncertainty":"One task is insufficient to infer a stable preference."}`
	}
	return p.output, "local-fake", "synthetic-reflection", "fixture-v1", nil
}

type freewillAdaptiveTaskReflectionFake struct {
	base          freewillTaskReflectionFake
	followUpCalls int
	followUpInput string
	failFollowUp  bool
}

func (p *freewillAdaptiveTaskReflectionFake) ReflectTask(ctx context.Context, input string) (string, string, string, string, error) {
	return p.base.ReflectTask(ctx, input)
}

func (p *freewillAdaptiveTaskReflectionFake) FollowUpTask(_ context.Context, input string) (string, string, string, string, error) {
	p.followUpCalls++
	p.followUpInput = input
	if p.failFollowUp {
		return "", "", "", "", errors.New("synthetic follow-up failure")
	}
	return `{"hypothesis":"The initial completion impression may have preceded the failed validation.","uncertainty":"A single synthetic self-report cannot resolve the mismatch."}`, "local-fake", "synthetic-follow-up", "fixture-v1", nil
}

func TestE32AdaptiveReflectionUsesHostContradictionAndStopsAcrossReopen(t *testing.T) {
	for _, test := range []struct {
		name              string
		hostStatus        string
		hostValidation    string
		selfReportOutcome string
		wantCalls         int
		wantStop          string
		failFollowUp      bool
	}{
		{name: "failed host versus completion claim", hostStatus: "execution_failed", hostValidation: "failed", selfReportOutcome: "completed", wantCalls: 1, wantStop: "follow_up_call_budget_exhausted"},
		{name: "completed host versus failure claim", hostStatus: "completed", hostValidation: "passed", selfReportOutcome: "not_completed", wantCalls: 1, wantStop: "follow_up_call_budget_exhausted"},
		{name: "no contradiction", hostStatus: "execution_failed", hostValidation: "failed", selfReportOutcome: "not_completed", wantCalls: 0, wantStop: "no_host_verified_contradiction"},
		{name: "unknown claim stops", hostStatus: "execution_failed", hostValidation: "failed", selfReportOutcome: "", wantCalls: 0, wantStop: "insufficient_host_verified_or_self_report_signal"},
		{name: "provider error is not retried", hostStatus: "execution_failed", hostValidation: "failed", selfReportOutcome: "completed", wantCalls: 1, wantStop: "follow_up_provider_error_no_retry", failFollowUp: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			dataDir, workspace := t.TempDir(), t.TempDir()
			events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer events.Close()
			failureCode := ""
			if test.hostStatus == "execution_failed" {
				failureCode = "task_failed"
			}
			fact := multiagent.FreewillTerminalFact{ProjectID: "project-e32", Workspace: workspace, AgentID: "agent-e32", IntentionID: "intention-e32", ParentRunID: "parent-e32", ChildRunID: "child-e32", Status: test.hostStatus, ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("d", 64), ValidationSummary: test.hostValidation, FailureCode: failureCode, RecordedAt: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
			eventType := event.EventFreewillExecutionFailed
			if fact.Status == "completed" {
				eventType = event.EventFreewillExecutionCompleted
			}
			receipt := event.NewEvent(eventType, "synthetic-host", map[string]any{"child_run_id": fact.ChildRunID, "report_ref": fact.ReportRef, "report_sha256": fact.ReportSHA256, "validation_summary": fact.ValidationSummary})
			receipt.Metadata.RunID = fact.ParentRunID
			if err := events.Store(ctx, receipt); err != nil {
				t.Fatal(err)
			}
			facts, err := newFreewillTerminalFactMemorySink(dataDir, fact.ProjectID, workspace, events, "owner-e32")
			if err != nil {
				t.Fatal(err)
			}
			if err := facts.CaptureFreewillTerminalFact(ctx, fact); err != nil {
				t.Fatal(err)
			}
			appraisalJSON, _ := json.Marshal(freewillTaskAppraisalResponse{AppraisalKind: "satisfaction", PerceivedOutcome: test.selfReportOutcome, Why: "Synthetic bounded report.", Context: "One local task.", Narrative: "Synthetic self-report only.", Uncertainty: "This is a model claim.", Scores: freewillSyntheticTaskScores()})
			appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{narrative: string(appraisalJSON), provider: "local-fake", model: "synthetic-appraisal", version: "fixture-v1"}, "owner-e32")
			if err != nil {
				t.Fatal(err)
			}
			if err := appraisals.Capture(ctx, fact); err != nil {
				t.Fatal(err)
			}
			taskID := freewillTaskScopeID(fact.ProjectID, facts.workspace, fact.ChildRunID)
			sources, err := appraisals.ReadOwnerTask(ctx, "owner-e32", fact.ProjectID, workspace, taskID, fact.AgentID, fact.ParentRunID, fact.ChildRunID)
			if err != nil {
				t.Fatal(err)
			}
			var hostOutcome freewillTaskOutcome
			for _, entry := range sources {
				if entry.Source == freewillTaskOutcomeSource {
					if err := json.Unmarshal([]byte(entry.Content), &hostOutcome); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := validateFreewillTaskReflectionReceipt(ctx, facts.store.Events, fact, hostOutcome); err != nil {
				t.Fatalf("valid host receipt rejected: %v", err)
			}
			mismatchedReceipt := hostOutcome
			if fact.Status == "completed" {
				mismatchedReceipt.Status = "execution_failed"
			} else {
				mismatchedReceipt.Status = "completed"
			}
			if err := validateFreewillTaskReflectionReceipt(ctx, facts.store.Events, fact, mismatchedReceipt); err == nil {
				t.Fatal("reflection accepted an outcome status that disagreed with the terminal callback")
			}
			unknownEventRef := hostOutcome
			unknownEventRef.ParentEventRefs = []string{"synthetic-unknown-event"}
			if err := validateFreewillTaskReflectionReceipt(ctx, facts.store.Events, fact, unknownEventRef); err == nil {
				t.Fatal("reflection accepted an unverified parent receipt reference")
			}
			fake := &freewillAdaptiveTaskReflectionFake{failFollowUp: test.failFollowUp}
			sink, err := newFreewillTaskReflectionSink(appraisals, fake, "owner-e32")
			if err != nil {
				t.Fatal(err)
			}
			if err := sink.ReflectTask(ctx, fact); (err != nil) != test.failFollowUp {
				t.Fatalf("reflection error=%v want failure=%v", err, test.failFollowUp)
			}
			if fake.base.calls != 1 || fake.followUpCalls != test.wantCalls || len(fake.followUpInput) > 1800 {
				t.Fatalf("provider calls base=%d followup=%d input bytes=%d", fake.base.calls, fake.followUpCalls, len(fake.followUpInput))
			}
			expectedHost := freewillHostOutcomeClaim(hostOutcome)
			expectedSelfReport := test.selfReportOutcome
			if expectedSelfReport != "completed" && expectedSelfReport != "not_completed" {
				expectedSelfReport = "unknown"
			}
			if test.wantCalls == 1 && (!strings.Contains(fake.followUpInput, `"host_outcome":"`+expectedHost+`"`) || !strings.Contains(fake.followUpInput, `"self_report_outcome":"`+expectedSelfReport+`"`)) {
				t.Fatalf("follow-up did not use the host-computed contradiction: %s", fake.followUpInput)
			}
			var adaptive freewillAdaptiveTaskReflection
			for _, entry := range mustFreewillRecent(t, facts) {
				if entry.Source == freewillTaskReflectionSource && json.Unmarshal([]byte(entry.Content), &adaptive) == nil && adaptive.Kind == "task_reflection_adaptive_follow_up" {
					if entry.UserID != "owner-e32" || entry.ProjectID != fact.ProjectID || entry.TaskID != taskID || entry.AgentID != fact.AgentID || len(adaptive.SourceRefs) != 3 {
						t.Fatalf("adaptive record lost exact task provenance: %#v entry=%#v", adaptive, entry)
					}
					break
				}
			}
			if adaptive.Kind == "" || adaptive.StopReason != test.wantStop || adaptive.FollowUpCalls != test.wantCalls || adaptive.MaxFollowUpCalls != 1 || adaptive.MaxTokens != freewillTaskReflectionFollowUpTokenBudget || adaptive.PriorReflectionID == "" || len(adaptive.PriorReflectionRevision) != 64 {
				t.Fatalf("adaptive metadata missing stop/budget/revision: %#v", adaptive)
			}
			if test.wantStop == "follow_up_call_budget_exhausted" && (adaptive.InitialHypothesis == "" || adaptive.FollowUpQuestion == "" || adaptive.RevisedHypothesis == "" || adaptive.InitialHypothesis == adaptive.RevisedHypothesis || adaptive.Provider == "" || adaptive.ModelVersion == "") {
				t.Fatalf("revision did not preserve both tentative responses: %#v", adaptive)
			}
			facts, err = newFreewillTerminalFactMemorySink(dataDir, fact.ProjectID, workspace, events, "owner-e32")
			if err != nil {
				t.Fatal(err)
			}
			appraisals, err = newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{}, "owner-e32")
			if err != nil {
				t.Fatal(err)
			}
			restartedProvider := &freewillAdaptiveTaskReflectionFake{}
			sink, err = newFreewillTaskReflectionSink(appraisals, restartedProvider, "owner-e32")
			if err != nil {
				t.Fatal(err)
			}
			if err := sink.ReflectTask(ctx, fact); err != nil && !test.failFollowUp {
				t.Fatal(err)
			}
			if restartedProvider.base.calls != 0 || restartedProvider.followUpCalls != 0 {
				t.Fatalf("reopen repeated reflection calls: base=%d followup=%d", restartedProvider.base.calls, restartedProvider.followUpCalls)
			}
			malformedBytes, _ := json.Marshal(freewillAdaptiveTaskReflection{Version: 1, Kind: "task_reflection_adaptive_follow_up", PriorReflectionID: adaptive.PriorReflectionID})
			if _, err := facts.store.Local.Store(ctx, memory.Memory{Content: string(malformedBytes), Summary: "Malformed adaptive reflection fixture", Category: "reflection", Tier: "persist", Source: freewillTaskReflectionSource, UserID: "owner-e32", ProjectID: fact.ProjectID, TaskID: taskID, AgentID: fact.AgentID, Metadata: map[string]string{"correlation_id": fact.ParentRunID}}); err != nil {
				t.Fatal(err)
			}
			if err := sink.ReflectTask(ctx, fact); err == nil {
				t.Fatal("malformed matching adaptive record did not fail closed")
			}
			if restartedProvider.base.calls != 0 || restartedProvider.followUpCalls != 0 {
				t.Fatalf("malformed replay marker caused provider calls: base=%d followup=%d", restartedProvider.base.calls, restartedProvider.followUpCalls)
			}
		})
	}
}

func mustFreewillRecent(t *testing.T, facts *freewillTerminalFactMemorySink) []memory.Memory {
	t.Helper()
	entries, err := facts.store.Local.ListRecent(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestFreewillTaskReflectionIsBoundedLinkedAndOneShotAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dataDir, workspace := t.TempDir(), t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	fact := multiagent.FreewillTerminalFact{ProjectID: "project-one", Workspace: workspace, AgentID: "agent-one", IntentionID: "intention-one", ParentRunID: "parent-one", ChildRunID: "task-one", Status: "execution_failed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("a", 64), ValidationSummary: "failed", FailureCode: "task_failed", RecordedAt: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	receiptEvent := event.NewEvent(event.EventFreewillExecutionFailed, "synthetic-host", map[string]any{"child_run_id": "task-one", "report_ref": fact.ReportRef, "report_sha256": fact.ReportSHA256, "validation_summary": fact.ValidationSummary})
	receiptEvent.Metadata.RunID = fact.ParentRunID
	if err := events.Store(ctx, receiptEvent); err != nil {
		t.Fatal(err)
	}
	facts, err := newFreewillTerminalFactMemorySink(dataDir, fact.ProjectID, workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	if err := facts.CaptureFreewillTerminalFact(ctx, fact); err != nil {
		t.Fatal(err)
	}
	appraisalProvider := &freewillRetrospectiveFake{narrative: "The task felt satisfying and seemed successful, though I may have misread the result.", provider: "local-fake", model: "synthetic-appraisal", version: "fixture-v1"}
	appraisals, err := newFreewillTaskAppraisalSink(facts, appraisalProvider, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := appraisals.Capture(ctx, fact); err != nil {
		t.Fatal(err)
	}
	taskID := freewillTaskScopeID(fact.ProjectID, facts.workspace, fact.ChildRunID)
	before, err := appraisals.ReadOwnerTask(ctx, "owner-one", fact.ProjectID, workspace, taskID, fact.AgentID, fact.ParentRunID, fact.ChildRunID)
	if err != nil || len(before) != 2 {
		t.Fatalf("input records=%d err=%v", len(before), err)
	}
	originals := map[string]string{}
	for _, r := range before {
		originals[r.Source] = r.Content
	}
	var outcomeID, reportID string
	for _, r := range before {
		if r.Source == freewillTaskOutcomeSource {
			outcomeID = r.ID
		}
		if r.Source == freewillTaskAppraisalSource {
			reportID = r.ID
		}
	}
	wrongParentOutcome := freewillTaskOutcome{Version: "freewill-task-outcome/v1", Kind: "host_task_outcome", ProjectID: fact.ProjectID, WorkspaceID: facts.workspaceID, Workspace: facts.workspace, AgentID: fact.AgentID, TaskID: taskID, ParentRunID: "other-parent", ChildRunID: fact.ChildRunID, Status: fact.Status, Validation: fact.ValidationSummary, ReportRef: fact.ReportRef, ReportSHA256: fact.ReportSHA256, Confidence: "host_validated_terminal_receipt", RecordedAt: fact.RecordedAt}
	wrongOutcomeBytes, err := json.Marshal(wrongParentOutcome)
	if err != nil {
		t.Fatal(err)
	}
	wrongOutcomeID, err := facts.store.Local.Store(ctx, memory.Memory{ID: "wrong-parent-task-outcome", Content: string(wrongOutcomeBytes), Summary: "Freewill task outcome appraisal", Category: "fact", Tier: "persist", Source: freewillTaskOutcomeSource, UserID: "owner-one", ProjectID: fact.ProjectID, TaskID: taskID, AgentID: fact.AgentID, Metadata: map[string]string{"correlation_id": "other-parent"}})
	if err != nil || wrongOutcomeID == "" {
		t.Fatalf("seed wrong-parent outcome: id=%q err=%v", wrongOutcomeID, err)
	}
	wrongParentAppraisal := freewillTaskAppraisal{Version: 1, Kind: "model_task_self_report", OutcomeID: wrongOutcomeID, ProjectID: fact.ProjectID, WorkspaceID: facts.workspaceID, TaskID: taskID, ParentRunID: "other-parent", ChildRunID: fact.ChildRunID, Provider: "local-fake", Model: "forged", ModelVersion: "v0", Confidence: "model_self_report", Narrative: "wrong parent narrative", RecordedAt: fact.RecordedAt}
	wrongReportBytes, err := json.Marshal(wrongParentAppraisal)
	if err != nil {
		t.Fatal(err)
	}
	wrongReportID, err := facts.store.Local.Store(ctx, memory.Memory{ID: "wrong-parent-task-appraisal", Content: string(wrongReportBytes), Summary: "Freewill task outcome appraisal", Category: "appraisal", Tier: "persist", Source: freewillTaskAppraisalSource, UserID: "owner-one", ProjectID: fact.ProjectID, TaskID: taskID, AgentID: fact.AgentID, Metadata: map[string]string{"correlation_id": "other-parent"}})
	if err != nil || wrongReportID == "" {
		t.Fatalf("seed wrong-parent appraisal: id=%q err=%v", wrongReportID, err)
	}
	wrongParentContent, err := json.Marshal(freewillTaskReflection{Version: 1, Kind: "task_reflection_hypothesis", OutcomeID: outcomeID, SelfReportID: reportID, ProjectID: fact.ProjectID, WorkspaceID: facts.workspaceID, TaskID: taskID, ParentRunID: "other-parent", ChildRunID: fact.ChildRunID, Provider: "local-fake", Model: "forged", ModelVersion: "v0", Confidence: "model_reflection_hypothesis", ProbeVersion: "task-reflection/v1", StoppingRule: "single_prompt_single_call", Alignment: "agreement", Hypothesis: "wrong-parent probe", Uncertainty: "adversarial", RecordedAt: fact.RecordedAt.Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	decoyID, err := facts.store.Local.Store(ctx, memory.Memory{ID: "wrong-parent-reflection", Content: string(wrongParentContent), Summary: "Freewill task reflection hypothesis adversarial parent", Category: "reflection", Tier: "persist", Source: freewillTaskReflectionSource, UserID: "owner-one", ProjectID: fact.ProjectID, TaskID: taskID, AgentID: fact.AgentID, Metadata: map[string]string{"correlation_id": "other-parent"}})
	if err != nil || decoyID == "" {
		t.Fatalf("seed wrong-parent reflection: id=%q err=%v", decoyID, err)
	}
	provider := &freewillTaskReflectionFake{}
	sink, err := newFreewillTaskReflectionSink(appraisals, provider, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.ReflectTask(ctx, fact); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 || len(provider.input) > 2400 || strings.Contains(provider.input, "raw report body") || !strings.Contains(provider.input, "host_status") || !strings.Contains(provider.input, "self_report") {
		t.Fatalf("reflection input/calls exceeded bounded source contract: calls=%d bytes=%d input=%s", provider.calls, len(provider.input), provider.input)
	}
	for _, forbidden := range []string{"task objective", "README.md", "raw report"} {
		if strings.Contains(provider.input, forbidden) {
			t.Fatalf("reflection received forbidden task body %q", forbidden)
		}
	}
	// Reopen every sink and replay the terminal callback. The durable reflection suppresses another provider call.
	facts, err = newFreewillTerminalFactMemorySink(dataDir, fact.ProjectID, workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err = newFreewillTaskAppraisalSink(facts, appraisalProvider, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	restartProvider := &freewillTaskReflectionFake{}
	sink, err = newFreewillTaskReflectionSink(appraisals, restartProvider, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.ReflectTask(ctx, fact); err != nil {
		t.Fatal(err)
	}
	if restartProvider.calls != 0 {
		t.Fatalf("restart repeated reflection provider call: %d", restartProvider.calls)
	}
	reflections, err := sink.ReadOwnerReflection(ctx, "owner-one", fact.ProjectID, workspace, taskID, fact.AgentID, fact.ParentRunID, fact.ChildRunID)
	if err != nil || len(reflections) != 1 {
		t.Fatalf("reopened reflections=%d err=%v", len(reflections), err)
	}
	if reflections[0].ID == decoyID {
		t.Fatal("same-child wrong-parent reflection was returned")
	}
	prior, err := sink.ReadLatestOwnerProjectReflection(ctx, "owner-one", fact.ProjectID, workspace, fact.AgentID)
	if err != nil || prior == nil {
		t.Fatalf("latest exact-scope prior reflection missing after reopen: %#v err=%v", prior, err)
	}
	if prior.Binding.ID != reflections[0].ID || prior.Binding.Source != freewillTaskReflectionSource || len(prior.Binding.Revision) != 64 || prior.TaskID != taskID || prior.ProjectID != fact.ProjectID || prior.Workspace != facts.workspace || prior.AgentID != fact.AgentID || prior.OwnerID != "owner-one" {
		t.Fatalf("prior reflection citation is not host-bound to the reopened source: %#v", prior)
	}
	wrongOwner, err := sink.ReadLatestOwnerProjectReflection(ctx, "other-owner", fact.ProjectID, workspace, fact.AgentID)
	if err == nil || wrongOwner != nil {
		t.Fatalf("prior reflection lookup accepted another owner: %#v err=%v", wrongOwner, err)
	}
	wrongAgent, err := sink.ReadLatestOwnerProjectReflection(ctx, "owner-one", fact.ProjectID, workspace, "other-agent")
	if err != nil || wrongAgent != nil {
		t.Fatalf("prior reflection lookup crossed agent scope: %#v err=%v", wrongAgent, err)
	}
	wrongParentRead, err := sink.ReadOwnerReflection(ctx, "owner-one", fact.ProjectID, workspace, taskID, fact.AgentID, "other-parent", fact.ChildRunID)
	if len(wrongParentRead) != 0 {
		t.Fatalf("wrong-parent read returned %d reflections err=%v", len(wrongParentRead), err)
	}
	var reflection freewillTaskReflection
	if err := json.Unmarshal([]byte(reflections[0].Content), &reflection); err != nil {
		t.Fatal(err)
	}
	if reflection.Kind != "task_reflection_hypothesis" || reflection.OutcomeID == "" || reflection.SelfReportID == "" || reflection.OutcomeID == reflection.SelfReportID || reflection.Alignment != "disagreement" || reflection.Confidence != "model_reflection_hypothesis" || reflection.Uncertainty == "" || reflection.StoppingRule != "single_prompt_single_call" || reflection.Provider != "local-fake" {
		t.Fatalf("reflection provenance or disagreement lost: %#v", reflection)
	}
	after, err := appraisals.ReadOwnerTask(ctx, "owner-one", fact.ProjectID, workspace, taskID, fact.AgentID, fact.ParentRunID, fact.ChildRunID)
	if err != nil || len(after) != 4 {
		t.Fatalf("task scope after reflection includes expected adversarial rows: count=%d err=%v", len(after), err)
	}
	unchanged := 0
	for _, r := range after {
		if r.ID == wrongOutcomeID || r.ID == wrongReportID {
			continue
		}
		if originals[r.Source] != r.Content {
			t.Fatalf("reflection modified source %s", r.Source)
		}
		unchanged++
	}
	if unchanged != 2 {
		t.Fatalf("unchanged source originals=%d want=2", unchanged)
	}
	if _, err := sink.ReadOwnerReflection(ctx, "other-owner", fact.ProjectID, workspace, taskID, fact.AgentID, fact.ParentRunID, fact.ChildRunID); err == nil {
		t.Fatal("reflection read accepted wrong owner")
	}
	otherTask, err := sink.ReadOwnerReflection(ctx, "owner-one", fact.ProjectID, workspace, freewillTaskScopeID(fact.ProjectID, facts.workspace, "other-task"), fact.AgentID, fact.ParentRunID, "other-task")
	if len(otherTask) != 0 {
		t.Fatalf("other task returned reflection count=%d err=%v", len(otherTask), err)
	}
}

func TestFreewillTaskReflectionCaptureFailureMayRetryProviderAfterRestart(t *testing.T) {
	ctx := context.Background()
	dataDir, workspace := t.TempDir(), t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	fact := multiagent.FreewillTerminalFact{ProjectID: "project-one", Workspace: workspace, AgentID: "agent-one", IntentionID: "intention-one", ParentRunID: "parent-one", ChildRunID: "task-one", Status: "completed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat("b", 64), ValidationSummary: "passed", RecordedAt: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)}
	receipt := event.NewEvent(event.EventFreewillExecutionCompleted, "synthetic-host", map[string]any{"child_run_id": fact.ChildRunID, "report_ref": fact.ReportRef, "report_sha256": fact.ReportSHA256, "validation_summary": fact.ValidationSummary})
	receipt.Metadata.RunID = fact.ParentRunID
	if err := events.Store(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	facts, err := newFreewillTerminalFactMemorySink(dataDir, fact.ProjectID, workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	if err := facts.CaptureFreewillTerminalFact(ctx, fact); err != nil {
		t.Fatal(err)
	}
	appraiser, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{narrative: "The task felt engaging.", provider: "local-fake", model: "synthetic-appraisal", version: "v1"}, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := appraiser.Capture(ctx, fact); err != nil {
		t.Fatal(err)
	}
	firstProvider := &freewillTaskReflectionFake{}
	firstSink, err := newFreewillTaskReflectionSink(appraiser, firstProvider, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	failingEvents := &failFirstReflectionCaptureEvents{EventStore: events, fail: true}
	facts.store.Events = failingEvents
	if err := firstSink.ReflectTask(ctx, fact); err == nil {
		t.Fatal("reflection Capture unexpectedly survived the injected durable-store failure")
	}
	if firstProvider.calls != 1 || failingEvents.fail {
		t.Fatalf("failure did not occur after exactly one provider response: calls=%d fail_hook_pending=%v", firstProvider.calls, failingEvents.fail)
	}
	// Reopen after the provider responded but before reflection persistence; the absence of a durable marker permits a retry.
	facts, err = newFreewillTerminalFactMemorySink(dataDir, fact.ProjectID, workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	appraiser, err = newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{narrative: "The task felt engaging.", provider: "local-fake", model: "synthetic-appraisal", version: "v1"}, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	secondProvider := &freewillTaskReflectionFake{}
	secondSink, err := newFreewillTaskReflectionSink(appraiser, secondProvider, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	taskID := freewillTaskScopeID(fact.ProjectID, facts.workspace, fact.ChildRunID)
	missing, err := secondSink.ReadOwnerReflection(ctx, "owner-one", fact.ProjectID, workspace, taskID, fact.AgentID, fact.ParentRunID, fact.ChildRunID)
	if err != nil || len(missing) != 0 {
		t.Fatalf("failed capture left reflection records=%d err=%v", len(missing), err)
	}
	if err := secondSink.ReflectTask(ctx, fact); err != nil {
		t.Fatal(err)
	}
	if secondProvider.calls != 1 {
		t.Fatalf("restart retry calls=%d want one call after failed non-durable attempt", secondProvider.calls)
	}
}
