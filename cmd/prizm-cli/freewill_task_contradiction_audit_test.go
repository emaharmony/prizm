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
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func TestE49CrossCycleContradictionAuditIsScopedTypedAppendOnlyAndIdempotent(t *testing.T) {
	ctx := context.Background()
	work := t.TempDir()
	dataDir, workspace := filepath.Join(work, "data"), filepath.Join(work, "workspace")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	canonicalWorkspace, err := canonicalFreewillWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	workspace = canonicalWorkspace
	const ownerID, projectID, agentID = "e49-synthetic-owner", "e49-synthetic-project", "e49-synthetic-agent"
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	facts, err := newFreewillTerminalFactMemorySink(dataDir, projectID, workspace, events, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	response, _ := json.Marshal(freewillTaskAppraisalResponse{AppraisalKind: "unknown", PerceivedOutcome: "unknown", Why: "Synthetic local report.", Context: "Not verified.", Narrative: "Bounded fixture.", Uncertainty: "Synthetic evidence only.", Scores: freewillSyntheticTaskScores()})
	provider := &freewillRetrospectiveFake{narrative: string(response), provider: "local-fake", model: "e49-synthetic-appraiser", version: "fixture-v1"}
	appraisals, err := newFreewillTaskAppraisalSink(facts, provider, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	baseTime := time.Date(2026, 11, 12, 15, 0, 0, 0, time.UTC)
	addReceipt := func(child, parent, status, validation, hash string, at time.Time) freewillTaskAuditRequest {
		t.Helper()
		failureCode := ""
		if status != "completed" {
			failureCode = "synthetic_failure"
		}
		fact := multiagent.FreewillTerminalFact{ProjectID: projectID, Workspace: workspace, AgentID: agentID, IntentionID: "e49-synthetic-intention-" + child, ParentRunID: parent, ChildRunID: child, Status: status, FailureCode: failureCode, ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat(hash, 64), ValidationSummary: validation, RecordedAt: at}
		eventType := event.EventFreewillExecutionCompleted
		if status != "completed" {
			eventType = event.EventFreewillExecutionFailed
		}
		if status == "execution_cancelled" {
			eventType = event.EventFreewillExecutionCancelled
		}
		receipt := event.NewEvent(eventType, "synthetic-host", map[string]any{"child_run_id": child, "report_ref": fact.ReportRef, "report_sha256": fact.ReportSHA256, "validation_summary": validation})
		receipt.Metadata.RunID = parent
		if err := events.Store(ctx, receipt); err != nil {
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
		for _, row := range rows {
			if row.Source == freewillTaskOutcomeSource {
				return freewillTaskAuditRequest{OwnerID: ownerID, ProjectID: projectID, Workspace: workspace, AgentID: agentID, ParentRunID: parent, ChildRunID: child, ReceiptDigest: freewillBindingDigest(row.ID, row.Content, row.Source)}
			}
		}
		t.Fatal("host task receipt was not persisted")
		return freewillTaskAuditRequest{}
	}
	initial := addReceipt("e49-cycle-one", "e49-parent-one", "completed", "passed", "a", baseTime)
	later := addReceipt("e49-cycle-two", "e49-parent-two", "completed", "passed", "b", baseTime.Add(time.Minute))
	unknown := addReceipt("e49-cycle-unknown", "e49-parent-unknown", "execution_cancelled", "failed", "c", baseTime.Add(2*time.Minute))
	incomparable := addReceipt("e49-cycle-incomparable", "e49-parent-incomparable", "completed", "not_run", "d", baseTime.Add(3*time.Minute))
	next := addReceipt("e49-cycle-three", "e49-parent-three", "execution_failed", "failed", "e", baseTime.Add(4*time.Minute))
	providerCallsBeforeAudit := provider.calls
	manifestDigest := freewillBindingDigest("synthetic-manifest-v1")
	newSink := func(taskIdentity string) *freewillTaskContradictionAuditSink {
		t.Helper()
		sink, err := newFreewillTaskContradictionAuditSink(appraisals, freewillTaskAuditBinding{OwnerID: ownerID, ProjectID: projectID, Workspace: workspace, AgentID: agentID, TaskIdentity: taskIdentity, ManifestID: "e49-synthetic-manifest-v1", ContextDigest: manifestDigest})
		if err != nil {
			t.Fatal(err)
		}
		return sink
	}
	bindReq := func(sink *freewillTaskContradictionAuditSink, source freewillTaskAuditRequest) freewillTaskAuditRequest {
		source.TaskIdentity, source.ManifestID, source.ContextDigest, source.Proposition = sink.binding.TaskIdentity, sink.binding.ManifestID, sink.binding.ContextDigest, sink.proposition
		return source
	}
	forecastTime := baseTime.Add(10 * time.Second)
	contradictionSink := newSink("task:repeatable-build")
	forecastReq := bindReq(contradictionSink, initial)
	forecast, err := contradictionSink.RecordForecast(ctx, forecastReq, "not_completed", forecastTime)
	if err != nil {
		t.Fatal(err)
	}
	var original freewillTaskContradictionForecast
	if err := json.Unmarshal([]byte(forecast.Content), &original); err != nil {
		t.Fatal(err)
	}
	if original.ForecastAbout != "future_repeat_of_same_configured_task_context" || original.ExpectedOutcome != "not_completed" || original.Uncertainty == "" || original.Source.MemoryID == "" || original.Source.Digest != initial.ReceiptDigest {
		t.Fatalf("forecast is not a typed tentative future repeat with provenance: %#v", original)
	}

	wrongRequests := []struct {
		name   string
		change func(*freewillTaskAuditRequest)
	}{
		{"owner", func(r *freewillTaskAuditRequest) { r.OwnerID = "other-owner" }},
		{"project", func(r *freewillTaskAuditRequest) { r.ProjectID = "other-project" }},
		{"workspace", func(r *freewillTaskAuditRequest) { r.Workspace = filepath.Join(workspace, "other") }},
		{"agent", func(r *freewillTaskAuditRequest) { r.AgentID = "other-agent" }},
		{"task_identity", func(r *freewillTaskAuditRequest) { r.TaskIdentity = "other-task" }},
		{"context_revision", func(r *freewillTaskAuditRequest) { r.ContextDigest = strings.Repeat("0", 64) }},
		{"stale_digest", func(r *freewillTaskAuditRequest) { r.ReceiptDigest = strings.Repeat("0", 64) }},
		{"wrong_task", func(r *freewillTaskAuditRequest) { r.ChildRunID = initial.ChildRunID }},
	}
	for _, tc := range wrongRequests {
		t.Run(tc.name, func(t *testing.T) {
			req := bindReq(contradictionSink, later)
			tc.change(&req)
			if _, err := contradictionSink.Audit(ctx, req, baseTime.Add(2*time.Minute)); err == nil {
				t.Fatal("mismatched audit scope was accepted")
			}
		})
	}

	// Host decision compares only a tentative forecast with a later, independent
	// receipt. The historical receipt also said completed; it is not itself a
	// semantic contradiction.
	auditReq := bindReq(contradictionSink, later)
	processEnv := []string{"E49_AUDIT_MODE=write", "E49_AUDIT_DATA=" + dataDir, "E49_AUDIT_WORKSPACE=" + workspace, "E49_AUDIT_OWNER=" + ownerID, "E49_AUDIT_PROJECT=" + projectID, "E49_AUDIT_AGENT=" + agentID, "E49_AUDIT_CONTEXT=" + manifestDigest, "E49_AUDIT_TASK=task:repeatable-build", "E49_AUDIT_MANIFEST=e49-synthetic-manifest-v1", "E49_AUDIT_PROPOSITION=" + contradictionSink.proposition, "E49_AUDIT_PARENT=" + later.ParentRunID, "E49_AUDIT_CHILD=" + later.ChildRunID, "E49_AUDIT_RECEIPT=" + later.ReceiptDigest, "E49_AUDIT_AT=" + baseTime.Add(2*time.Minute).Format(time.RFC3339Nano)}
	var wg sync.WaitGroup
	outputs := make(chan string, 2)
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestE49AuditProcessRestartHelper$")
			cmd.Env = append(os.Environ(), processEnv...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				errCh <- fmt.Errorf("separate-process audit writer: %w: %s", err, strings.TrimSpace(string(out)))
				return
			}
			outputs <- strings.TrimSpace(string(out))
		}()
	}
	wg.Wait()
	close(outputs)
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	var firstAudit memory.Memory
	for output := range outputs {
		marker := "AUDIT_ROW_ID:"
		at := strings.LastIndex(output, marker)
		if at < 0 {
			t.Fatalf("separate-process writer omitted audit ID: %s", output)
		}
		id := strings.TrimSpace(strings.SplitN(output[at+len(marker):], "\n", 2)[0])
		if firstAudit.ID == "" {
			firstAudit.ID = id
		} else if firstAudit.ID != id {
			t.Fatalf("cross-process exact audit produced duplicate records: %s != %s", firstAudit.ID, id)
		}
	}
	processRows, err := contradictionSink.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundProcessAudit := false
	for _, row := range processRows {
		var stored freewillTaskContradictionAudit
		if json.Unmarshal([]byte(row.Content), &stored) == nil && row.ID == firstAudit.ID {
			firstAudit = row
			foundProcessAudit = true
		}
	}
	if !foundProcessAudit {
		var ids []string
		for _, row := range processRows {
			ids = append(ids, row.ID+"/"+row.Category)
		}
		t.Fatalf("cross-process audit record %s was not readable; records=%v", firstAudit.ID, ids)
	}
	var contradiction freewillTaskContradictionAudit
	if err := json.Unmarshal([]byte(firstAudit.Content), &contradiction); err != nil {
		t.Fatal(err)
	}
	if contradiction.Result != "contradiction" || contradiction.OriginalForecastID != original.ForecastID || contradiction.OriginalForecastDigest != freewillBindingDigest(forecast.ID, forecast.Content, forecast.Source) || contradiction.NewSource.Digest != later.ReceiptDigest || contradiction.RevisedConclusion != "completed" || contradiction.PreviousAuditID != original.ForecastID {
		t.Fatalf("contradiction audit lost source/revision lineage: %#v", contradiction)
	}
	replayed, err := contradictionSink.Audit(ctx, auditReq, baseTime.Add(3*time.Minute))
	if err != nil || replayed.ID != firstAudit.ID {
		t.Fatalf("exact receipt replay was not idempotent: record=%s err=%v", replayed.ID, err)
	}

	equalSink := newSink("task:equal-outcome")
	equalForecastReq := bindReq(equalSink, initial)
	if _, err := equalSink.RecordForecast(ctx, equalForecastReq, "completed", forecastTime); err != nil {
		t.Fatal(err)
	}
	equalAudit, err := equalSink.Audit(ctx, bindReq(equalSink, later), baseTime.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var equalRecord freewillTaskContradictionAudit
	if err := json.Unmarshal([]byte(equalAudit.Content), &equalRecord); err != nil {
		t.Fatal(err)
	}
	if equalRecord.Result != "equal" || equalRecord.RevisedConclusion != "" {
		t.Fatalf("equal outcome was labeled contradictory: %#v", equalRecord)
	}

	unknownSink := newSink("task:unknown-outcome")
	if _, err := unknownSink.RecordForecast(ctx, bindReq(unknownSink, initial), "completed", forecastTime); err != nil {
		t.Fatal(err)
	}
	unknownAudit, err := unknownSink.Audit(ctx, bindReq(unknownSink, unknown), baseTime.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var unknownRecord freewillTaskContradictionAudit
	if err := json.Unmarshal([]byte(unknownAudit.Content), &unknownRecord); err != nil {
		t.Fatal(err)
	}
	if unknownRecord.Result != "unknown" || unknownRecord.NewSource.Claim != "unknown" {
		t.Fatalf("unknown outcome was treated as contradiction: %#v", unknownRecord)
	}
	incomparableSink := newSink("task:incomparable-outcome")
	if _, err := incomparableSink.RecordForecast(ctx, bindReq(incomparableSink, initial), "completed", forecastTime); err != nil {
		t.Fatal(err)
	}
	incomparableAudit, err := incomparableSink.Audit(ctx, bindReq(incomparableSink, incomparable), baseTime.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var incomparableRecord freewillTaskContradictionAudit
	if err := json.Unmarshal([]byte(incomparableAudit.Content), &incomparableRecord); err != nil {
		t.Fatal(err)
	}
	if incomparableRecord.Result != "incomparable" || incomparableRecord.NewSource.Claim != "incomparable" {
		t.Fatalf("incomparable outcome was not distinguished: %#v", incomparableRecord)
	}

	chainAudit, err := contradictionSink.Audit(ctx, bindReq(contradictionSink, next), baseTime.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var chainRecord freewillTaskContradictionAudit
	if err := json.Unmarshal([]byte(chainAudit.Content), &chainRecord); err != nil {
		t.Fatal(err)
	}
	if chainRecord.PreviousAuditID != contradiction.AuditID || chainRecord.PreviousAuditDigest != freewillBindingDigest(firstAudit.ID, firstAudit.Content, firstAudit.Source) || chainRecord.Result != "equal" {
		t.Fatalf("second audit did not extend the first immutable link: %#v", chainRecord)
	}

	noForecastSink := newSink("task:no-forecast")
	if _, err := noForecastSink.Audit(ctx, bindReq(noForecastSink, later), baseTime.Add(2*time.Minute)); err == nil {
		t.Fatal("audit without explicit future forecast was accepted")
	}
	rows, err := contradictionSink.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var preserved memory.Memory
	for _, row := range rows {
		if row.ID == forecast.ID {
			preserved = row
		}
	}
	if preserved.ID == "" || preserved.Content != forecast.Content || freewillBindingDigest(preserved.ID, preserved.Content, preserved.Source) != contradiction.OriginalForecastDigest {
		t.Fatal("original forecast was overwritten or its digest changed")
	}
	allEvents, err := events.Query(ctx, event.EventFilter{Type: "prizm.", Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range allEvents {
		lower := strings.ToLower(e.Type)
		if strings.Contains(lower, "approval") || strings.Contains(lower, "dispatch") || strings.Contains(lower, "tool") || strings.Contains(lower, "action") {
			t.Fatalf("decision-only audit emitted a prohibited effect event: %s", e.Type)
		}
	}
	if provider.calls != providerCallsBeforeAudit {
		t.Fatalf("audit unexpectedly called optional model mapper: before=%d after=%d", providerCallsBeforeAudit, provider.calls)
	}

	malformedSink := newSink("task:malformed-chain")
	if _, err := malformedSink.RecordForecast(ctx, bindReq(malformedSink, initial), "completed", forecastTime); err != nil {
		t.Fatal(err)
	}
	malformedJSON, _ := json.Marshal(freewillTaskContradictionAudit{Version: 1, Kind: "cross_cycle_contradiction_audit", AuditID: "bad-link", PropositionKey: malformedSink.proposition, OriginalForecastID: "wrong-original", PreviousAuditID: "broken-link", PreviousAuditDigest: "stale"})
	if _, _, err := facts.store.Capture(ctx, memory.CaptureRequest{Scope: malformedSink.scope, Content: string(malformedJSON), Summary: "Synthetic malformed audit-chain fixture", Category: "audit", Tier: "persist", Source: freewillTaskContradictionAuditSource, CaptureKey: "malformed-fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := malformedSink.Audit(ctx, bindReq(malformedSink, later), baseTime.Add(2*time.Minute)); err == nil {
		t.Fatal("malformed prior audit link was extended")
	}

	if err := events.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestE49AuditProcessRestartHelper$")
	cmd.Env = append(os.Environ(), "E49_AUDIT_MODE=read", "E49_AUDIT_DATA="+dataDir, "E49_AUDIT_WORKSPACE="+workspace, "E49_AUDIT_OWNER="+ownerID, "E49_AUDIT_PROJECT="+projectID, "E49_AUDIT_AGENT="+agentID, "E49_AUDIT_CONTEXT="+manifestDigest)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("separate-process audit readback failed: %v: %s", err, strings.TrimSpace(string(output)))
	}
}

func TestE49AuditProcessRestartHelper(t *testing.T) {
	mode := os.Getenv("E49_AUDIT_MODE")
	if mode != "read" && mode != "write" {
		return
	}
	ctx := context.Background()
	dataDir, workspace := os.Getenv("E49_AUDIT_DATA"), os.Getenv("E49_AUDIT_WORKSPACE")
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	facts, err := newFreewillTerminalFactMemorySink(dataDir, os.Getenv("E49_AUDIT_PROJECT"), workspace, events, os.Getenv("E49_AUDIT_OWNER"))
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{}, os.Getenv("E49_AUDIT_OWNER"))
	if err != nil {
		t.Fatal(err)
	}
	sink, err := newFreewillTaskContradictionAuditSink(appraisals, freewillTaskAuditBinding{OwnerID: os.Getenv("E49_AUDIT_OWNER"), ProjectID: os.Getenv("E49_AUDIT_PROJECT"), Workspace: workspace, AgentID: os.Getenv("E49_AUDIT_AGENT"), TaskIdentity: "task:repeatable-build", ManifestID: "e49-synthetic-manifest-v1", ContextDigest: os.Getenv("E49_AUDIT_CONTEXT")})
	if err != nil {
		t.Fatal(err)
	}
	if mode == "write" {
		at, err := time.Parse(time.RFC3339Nano, os.Getenv("E49_AUDIT_AT"))
		if err != nil {
			t.Fatal(err)
		}
		req := freewillTaskAuditRequest{OwnerID: os.Getenv("E49_AUDIT_OWNER"), ProjectID: os.Getenv("E49_AUDIT_PROJECT"), Workspace: workspace, AgentID: os.Getenv("E49_AUDIT_AGENT"), TaskIdentity: os.Getenv("E49_AUDIT_TASK"), ManifestID: os.Getenv("E49_AUDIT_MANIFEST"), ContextDigest: os.Getenv("E49_AUDIT_CONTEXT"), Proposition: os.Getenv("E49_AUDIT_PROPOSITION"), ParentRunID: os.Getenv("E49_AUDIT_PARENT"), ChildRunID: os.Getenv("E49_AUDIT_CHILD"), ReceiptDigest: os.Getenv("E49_AUDIT_RECEIPT")}
		row, err := sink.Audit(ctx, req, at)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("AUDIT_ROW_ID:%s\n", row.ID)
		return
	}
	rows, err := sink.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("restart readback got %d contradiction records, want forecast plus two-cycle audit chain", len(rows))
	}
	var forecastCount, auditCount int
	for _, row := range rows {
		if row.Category == "hypothesis" {
			forecastCount++
		}
		if row.Category == "audit" {
			auditCount++
		}
	}
	if forecastCount != 1 || auditCount != 2 {
		t.Fatalf("restart readback categories forecast=%d audit=%d", forecastCount, auditCount)
	}
}
