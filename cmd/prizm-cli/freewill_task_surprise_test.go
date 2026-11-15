package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill/telemetry"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func TestE50TypedTaskSurprisePersistsExactEvidenceAndAppendRevisions(t *testing.T) {
	ctx := context.Background()
	work := t.TempDir()
	dataDir, workspace := filepath.Join(work, "data"), filepath.Join(work, "workspace")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	workspace, err := canonicalFreewillWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	const ownerID, projectID, agentID = "e50-synthetic-owner", "e50-synthetic-project", "e50-synthetic-agent"
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	facts, err := newFreewillTerminalFactMemorySink(dataDir, projectID, workspace, events, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	provider := &freewillRetrospectiveFake{provider: "local-fake", model: "e50-appraiser", version: "fixture-v1"}
	appraisals, err := newFreewillTaskAppraisalSink(facts, provider, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	baseTime := time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC)
	captureCycle := func(child, parent, status, validation, perceived, hash string, at time.Time) freewillTaskSurpriseRequest {
		t.Helper()
		response, _ := json.Marshal(freewillTaskAppraisalResponse{AppraisalKind: "unknown", PerceivedOutcome: perceived, Why: "Synthetic tentative report.", Context: "Local fixture only.", Narrative: "No stable interest claim.", Uncertainty: "Synthetic model self-report.", Scores: freewillSyntheticTaskScores()})
		provider.narrative = string(response)
		failureCode := ""
		if status != "completed" {
			failureCode = "synthetic_failure"
		}
		fact := multiagent.FreewillTerminalFact{ProjectID: projectID, Workspace: workspace, AgentID: agentID, IntentionID: "e50-intention-" + child, ParentRunID: parent, ChildRunID: child, Status: status, FailureCode: failureCode, ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat(hash, 64), ValidationSummary: validation, RecordedAt: at}
		eventType := event.EventFreewillExecutionCompleted
		if status != "completed" {
			eventType = event.EventFreewillExecutionFailed
		}
		if status == "execution_cancelled" {
			eventType = event.EventFreewillExecutionCancelled
		}
		receiptEvent := event.NewEvent(eventType, "synthetic-host", map[string]any{"child_run_id": child, "report_ref": fact.ReportRef, "report_sha256": fact.ReportSHA256, "validation_summary": validation})
		receiptEvent.Metadata.RunID = parent
		if err := events.Store(ctx, receiptEvent); err != nil {
			t.Fatal(err)
		}
		if err := facts.CaptureFreewillTerminalFact(ctx, fact); err != nil {
			t.Fatal(err)
		}
		if err := appraisals.Capture(ctx, fact); err != nil {
			t.Fatal(err)
		}
		taskID := freewillTaskScopeID(projectID, workspace, child)
		rows, err := appraisals.ReadOwnerTask(ctx, ownerID, projectID, workspace, taskID, agentID, parent, child)
		if err != nil {
			t.Fatal(err)
		}
		request := freewillTaskSurpriseRequest{Task: freewillTaskAuditRequest{OwnerID: ownerID, ProjectID: projectID, Workspace: workspace, AgentID: agentID, ParentRunID: parent, ChildRunID: child}}
		for _, row := range rows {
			switch row.Source {
			case freewillTaskOutcomeSource:
				request.Task.ReceiptDigest = freewillBindingDigest(row.ID, row.Content, row.Source)
			case freewillTaskAppraisalSource:
				request.SelfReportDigest = freewillBindingDigest(row.ID, row.Content, row.Source)
			}
		}
		return request
	}
	initial := captureCycle("e50-cycle-one", "e50-parent-one", "completed", "passed", "completed", "a", baseTime)
	laterOpposite := captureCycle("e50-cycle-two", "e50-parent-two", "completed", "passed", "not_completed", "b", baseTime.Add(time.Minute))
	equal := captureCycle("e50-cycle-equal", "e50-parent-equal", "completed", "passed", "completed", "c", baseTime.Add(2*time.Minute))
	unknown := captureCycle("e50-cycle-unknown", "e50-parent-unknown", "completed", "passed", "unknown", "d", baseTime.Add(3*time.Minute))
	incomparable := captureCycle("e50-cycle-incomparable", "e50-parent-incomparable", "completed", "not_run", "not_completed", "e", baseTime.Add(4*time.Minute))

	binding := freewillTaskAuditBinding{OwnerID: ownerID, ProjectID: projectID, Workspace: workspace, AgentID: agentID, TaskIdentity: "task:repeatable-appraisal", ManifestID: "e50-synthetic-manifest", ContextDigest: freewillBindingDigest("e50-synthetic-context")}
	sink, err := newFreewillTaskSurpriseSink(appraisals, binding)
	if err != nil {
		t.Fatal(err)
	}
	bind := func(req freewillTaskSurpriseRequest) freewillTaskSurpriseRequest {
		req.Task.TaskIdentity, req.Task.ManifestID, req.Task.ContextDigest = binding.TaskIdentity, binding.ManifestID, binding.ContextDigest
		req.Task.Proposition = sink.base.proposition
		return req
	}
	forecastAt := baseTime.Add(10 * time.Second)
	predictionRow, err := sink.RecordPrediction(ctx, bind(initial), telemetry.OutcomeCompleted, forecastAt)
	if err != nil {
		t.Fatal(err)
	}
	var prediction telemetry.Prediction
	if err := json.Unmarshal([]byte(predictionRow.Content), &prediction); err != nil {
		t.Fatal(err)
	}
	if prediction.ExpectedOutcome != telemetry.OutcomeCompleted || prediction.Uncertainty == "" || prediction.SourceReceipt.ID == "" || prediction.SourceSelfReport.ID == "" || prediction.SourceReceipt.Digest != initial.Task.ReceiptDigest || prediction.SourceSelfReport.Digest != initial.SelfReportDigest {
		t.Fatalf("typed tentative prediction lost exact source provenance: %#v", prediction)
	}
	tamperedPrediction := prediction
	tamperedPrediction.SourceReceipt.Outcome = telemetry.OutcomeNotCompleted
	tamperedContent, err := json.Marshal(tamperedPrediction)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceE50StoredMemoryContent(filepath.Join(facts.factsRoot, "memory", "memory"), predictionRow.Content, string(tamperedContent)); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Audit(ctx, bind(laterOpposite), baseTime.Add(2*time.Minute)); err == nil {
		t.Fatal("audit accepted a persisted original source outcome that disagreed with its reloaded receipt")
	}
	if err := replaceE50StoredMemoryContent(filepath.Join(facts.factsRoot, "memory", "memory"), string(tamperedContent), predictionRow.Content); err != nil {
		t.Fatal(err)
	}

	badRequests := []struct {
		name   string
		mutate func(*freewillTaskSurpriseRequest)
	}{
		{"wrong_owner", func(r *freewillTaskSurpriseRequest) { r.Task.OwnerID = "other-owner" }},
		{"wrong_project", func(r *freewillTaskSurpriseRequest) { r.Task.ProjectID = "other-project" }},
		{"wrong_workspace", func(r *freewillTaskSurpriseRequest) { r.Task.Workspace = filepath.Join(workspace, "other") }},
		{"wrong_agent", func(r *freewillTaskSurpriseRequest) { r.Task.AgentID = "other-agent" }},
		{"wrong_task_identity", func(r *freewillTaskSurpriseRequest) { r.Task.TaskIdentity = "other-task" }},
		{"wrong_context", func(r *freewillTaskSurpriseRequest) { r.Task.ContextDigest = strings.Repeat("0", 64) }},
		{"stale_receipt_digest", func(r *freewillTaskSurpriseRequest) { r.Task.ReceiptDigest = strings.Repeat("0", 64) }},
		{"stale_report_digest", func(r *freewillTaskSurpriseRequest) { r.SelfReportDigest = strings.Repeat("0", 64) }},
	}
	for _, test := range badRequests {
		t.Run(test.name, func(t *testing.T) {
			req := bind(laterOpposite)
			test.mutate(&req)
			if _, err := sink.Audit(ctx, req, baseTime.Add(2*time.Minute)); err == nil {
				t.Fatal("wrong or stale source scope accepted")
			}
		})
	}

	writerEnv := []string{"E50_SURPRISE_MODE=write", "E50_SURPRISE_DATA=" + dataDir, "E50_SURPRISE_WORKSPACE=" + workspace, "E50_SURPRISE_OWNER=" + ownerID, "E50_SURPRISE_PROJECT=" + projectID, "E50_SURPRISE_AGENT=" + agentID, "E50_SURPRISE_TASK=task:repeatable-appraisal", "E50_SURPRISE_MANIFEST=e50-synthetic-manifest", "E50_SURPRISE_CONTEXT=" + binding.ContextDigest, "E50_SURPRISE_PROPOSITION=" + sink.base.proposition, "E50_SURPRISE_PARENT=" + laterOpposite.Task.ParentRunID, "E50_SURPRISE_CHILD=" + laterOpposite.Task.ChildRunID, "E50_SURPRISE_RECEIPT=" + laterOpposite.Task.ReceiptDigest, "E50_SURPRISE_REPORT=" + laterOpposite.SelfReportDigest, "E50_SURPRISE_AT=" + baseTime.Add(2*time.Minute).Format(time.RFC3339Nano)}
	var writers sync.WaitGroup
	writeOutputs := make(chan string, 2)
	writeErrors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestE50SurpriseProcessHelper$")
			cmd.Env = append(os.Environ(), writerEnv...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				writeErrors <- fmt.Errorf("surprise writer process: %w: %s", err, strings.TrimSpace(string(output)))
				return
			}
			writeOutputs <- strings.TrimSpace(string(output))
		}()
	}
	writers.Wait()
	close(writeOutputs)
	close(writeErrors)
	for err := range writeErrors {
		t.Fatal(err)
	}
	var firstAuditRowID string
	for output := range writeOutputs {
		at := strings.LastIndex(output, "REVISION_ROW_ID:")
		if at < 0 {
			t.Fatalf("writer omitted stored revision ID: %s", output)
		}
		id := strings.TrimSpace(strings.SplitN(output[at+len("REVISION_ROW_ID:"):], "\n", 2)[0])
		if firstAuditRowID == "" {
			firstAuditRowID = id
		} else if firstAuditRowID != id {
			t.Fatalf("exact concurrent source produced duplicate revisions: %s != %s", firstAuditRowID, id)
		}
	}
	rows, err := sink.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var oppositeRow memory.Memory
	for _, row := range rows {
		if row.ID == firstAuditRowID {
			oppositeRow = row
		}
	}
	if oppositeRow.ID == "" {
		t.Fatal("concurrent cross-process revision was not readable")
	}
	var opposite telemetry.SurpriseRevision
	if err := json.Unmarshal([]byte(oppositeRow.Content), &opposite); err != nil {
		t.Fatal(err)
	}
	if opposite.Relation != telemetry.RelationOpposite || opposite.SourceReceipt.Digest != laterOpposite.Task.ReceiptDigest || opposite.SourceSelfReport.Digest != laterOpposite.SelfReportDigest || opposite.DurableInterest || !opposite.FurtherEvidenceRequired || opposite.PreviousRevisionID != prediction.PredictionID {
		t.Fatalf("opposite report did not append a bounded evidence-linked surprise: %#v", opposite)
	}
	replayed, err := sink.Audit(ctx, bind(laterOpposite), baseTime.Add(2*time.Minute))
	if err != nil || replayed.ID != oppositeRow.ID {
		t.Fatalf("exact source replay failed: row=%s err=%v", replayed.ID, err)
	}

	for _, test := range []struct {
		name   string
		source freewillTaskSurpriseRequest
		want   telemetry.Relation
		at     time.Time
	}{
		{"equal", equal, telemetry.RelationEqual, baseTime.Add(4 * time.Minute)},
		{"unknown", unknown, telemetry.RelationUnknown, baseTime.Add(5 * time.Minute)},
		{"incomparable", incomparable, telemetry.RelationIncomparable, baseTime.Add(6 * time.Minute)},
	} {
		row, err := sink.Audit(ctx, bind(test.source), test.at)
		if err != nil {
			t.Fatalf("%s audit: %v", test.name, err)
		}
		var record telemetry.SurpriseRevision
		if err := json.Unmarshal([]byte(row.Content), &record); err != nil {
			t.Fatal(err)
		}
		if record.Relation != test.want || record.DurableInterest || !record.FurtherEvidenceRequired {
			t.Fatalf("%s relation/evidence gate wrong: %#v", test.name, record)
		}
	}

	var preserved memory.Memory
	for _, row := range rows {
		if row.ID == predictionRow.ID {
			preserved = row
		}
	}
	if preserved.ID == "" || preserved.Content != predictionRow.Content || freewillBindingDigest(preserved.ID, preserved.Content, preserved.Source) != freewillBindingDigest(predictionRow.ID, predictionRow.Content, predictionRow.Source) {
		t.Fatal("original prediction was overwritten")
	}
	allEvents, err := events.Query(ctx, event.EventFilter{Type: "prizm.", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range allEvents {
		lower := strings.ToLower(e.Type)
		if strings.Contains(lower, "approval") || strings.Contains(lower, "dispatch") || strings.Contains(lower, "tool") || strings.Contains(lower, "action") || strings.Contains(lower, "selection") {
			t.Fatalf("surprise audit emitted a prohibited effect event: %s", e.Type)
		}
	}

	if err := events.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestE50SurpriseProcessHelper$")
	cmd.Env = append(os.Environ(), "E50_SURPRISE_MODE=read", "E50_SURPRISE_DATA="+dataDir, "E50_SURPRISE_WORKSPACE="+workspace, "E50_SURPRISE_OWNER="+ownerID, "E50_SURPRISE_PROJECT="+projectID, "E50_SURPRISE_AGENT="+agentID, "E50_SURPRISE_TASK=task:repeatable-appraisal", "E50_SURPRISE_MANIFEST=e50-synthetic-manifest", "E50_SURPRISE_CONTEXT="+binding.ContextDigest, "E50_SURPRISE_PROPOSITION="+sink.base.proposition)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fresh process surprise readback failed: %v: %s", err, strings.TrimSpace(string(output)))
	}
}

func TestE50SurpriseProcessHelper(t *testing.T) {
	mode := os.Getenv("E50_SURPRISE_MODE")
	if mode != "write" && mode != "read" {
		return
	}
	ctx := context.Background()
	dataDir, workspace := os.Getenv("E50_SURPRISE_DATA"), os.Getenv("E50_SURPRISE_WORKSPACE")
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	facts, err := newFreewillTerminalFactMemorySink(dataDir, os.Getenv("E50_SURPRISE_PROJECT"), workspace, events, os.Getenv("E50_SURPRISE_OWNER"))
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{}, os.Getenv("E50_SURPRISE_OWNER"))
	if err != nil {
		t.Fatal(err)
	}
	binding := freewillTaskAuditBinding{OwnerID: os.Getenv("E50_SURPRISE_OWNER"), ProjectID: os.Getenv("E50_SURPRISE_PROJECT"), Workspace: workspace, AgentID: os.Getenv("E50_SURPRISE_AGENT"), TaskIdentity: os.Getenv("E50_SURPRISE_TASK"), ManifestID: os.Getenv("E50_SURPRISE_MANIFEST"), ContextDigest: os.Getenv("E50_SURPRISE_CONTEXT")}
	sink, err := newFreewillTaskSurpriseSink(appraisals, binding)
	if err != nil {
		t.Fatal(err)
	}
	if mode == "write" {
		at, err := time.Parse(time.RFC3339Nano, os.Getenv("E50_SURPRISE_AT"))
		if err != nil {
			t.Fatal(err)
		}
		req := freewillTaskSurpriseRequest{Task: freewillTaskAuditRequest{OwnerID: binding.OwnerID, ProjectID: binding.ProjectID, Workspace: workspace, AgentID: binding.AgentID, TaskIdentity: binding.TaskIdentity, ManifestID: binding.ManifestID, ContextDigest: binding.ContextDigest, Proposition: os.Getenv("E50_SURPRISE_PROPOSITION"), ParentRunID: os.Getenv("E50_SURPRISE_PARENT"), ChildRunID: os.Getenv("E50_SURPRISE_CHILD"), ReceiptDigest: os.Getenv("E50_SURPRISE_RECEIPT")}, SelfReportDigest: os.Getenv("E50_SURPRISE_REPORT")}
		row, err := sink.Audit(ctx, req, at)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("REVISION_ROW_ID:%s\n", row.ID)
		return
	}
	rows, err := sink.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var predictions, revisions int
	for _, row := range rows {
		if row.Category == "hypothesis" {
			predictions++
		}
		if row.Category == "audit" {
			revisions++
		}
	}
	if predictions != 1 || revisions != 4 {
		t.Fatalf("fresh process got predictions=%d revisions=%d", predictions, revisions)
	}
}

func TestE50SurpriseSourceTamperingFailsClosed(t *testing.T) {
	at := time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC)
	actual := telemetry.Source{ID: "source-id", Digest: strings.Repeat("a", 64), Outcome: telemetry.OutcomeCompleted, ParentRunID: "parent", ChildRunID: "child", RecordedAt: at}
	mutations := []struct {
		name string
		edit func(*telemetry.Source)
	}{
		{"id", func(source *telemetry.Source) { source.ID = "other-id" }},
		{"digest", func(source *telemetry.Source) { source.Digest = strings.Repeat("b", 64) }},
		{"outcome", func(source *telemetry.Source) { source.Outcome = telemetry.OutcomeNotCompleted }},
		{"parent", func(source *telemetry.Source) { source.ParentRunID = "other-parent" }},
		{"child", func(source *telemetry.Source) { source.ChildRunID = "other-child" }},
		{"timestamp", func(source *telemetry.Source) { source.RecordedAt = at.Add(time.Second) }},
	}
	if !sameSurpriseSource(actual, actual) {
		t.Fatal("identical source was rejected")
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			stored := actual
			test.edit(&stored)
			if sameSurpriseSource(actual, stored) {
				t.Fatalf("tampered %s source field was accepted", test.name)
			}
		})
	}
}

func replaceE50StoredMemoryContent(root, oldContent, newContent string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		path := filepath.Join(root, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		contents := string(data)
		if !strings.Contains(contents, oldContent) {
			continue
		}
		updated := strings.Replace(contents, oldContent, newContent, 1)
		return os.WriteFile(path, []byte(updated), 0600)
	}
	return fmt.Errorf("stored synthetic memory content not found")
}
