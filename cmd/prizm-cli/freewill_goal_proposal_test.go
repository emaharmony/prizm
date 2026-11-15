package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/api"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

type failFreewillGoalReviewStore struct {
	memory.MemoryStore
	fail bool
}

func (s *failFreewillGoalReviewStore) Store(ctx context.Context, item memory.Memory) (string, error) {
	if s.fail && item.Source == freewillGoalReviewSource {
		s.fail = false
		return "", errors.New("synthetic one-time review persistence failure")
	}
	return s.MemoryStore.Store(ctx, item)
}

func duplicateGoalTaskOutcome(t *testing.T, h *freewillGoalTestHarness, childID string) {
	t.Helper()
	items, err := h.facts.store.Local.ListRecent(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Source != freewillTaskOutcomeSource || item.Category != "fact" {
			continue
		}
		var outcome freewillTaskOutcome
		if json.Unmarshal([]byte(item.Content), &outcome) != nil || outcome.ChildRunID != childID {
			continue
		}
		outcome.ParentRunID += "-ambiguous"
		encoded, _ := json.Marshal(outcome)
		_, _, err = h.facts.store.Capture(context.Background(), memory.CaptureRequest{Scope: memory.Scope{UserID: "owner-a", IncludeUserScope: true, ProjectID: outcome.ProjectID, TaskID: outcome.TaskID, AgentID: outcome.AgentID, CorrelationID: outcome.ParentRunID}, Content: string(encoded), Summary: "Synthetic duplicate host outcome", Category: "fact", Tier: "persist", Source: freewillTaskOutcomeSource, CaptureKey: "ambiguous-outcome:" + childID})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("task outcome for %s was not found", childID)
}

func addMalformedGoalTaskEvidence(t *testing.T, h *freewillGoalTestHarness, childID string) {
	t.Helper()
	ctx := context.Background()
	parentID, reportSHA := "parent-"+childID, strings.Repeat("d", 64)
	taskID := freewillTaskScopeID("project-a", h.workspace, childID)
	receipt := event.NewEvent(event.EventFreewillExecutionCompleted, "malformed-receipt-fixture", map[string]any{"child_run_id": childID, "report_ref": "multiagent_report.json", "report_sha256": reportSHA, "validation_summary": "passed"})
	receipt.Metadata.RunID = parentID
	if err := h.events.Store(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	host := freewillTaskOutcome{Version: "unexpected-version", Kind: "host_task_outcome", ProjectID: "project-a", WorkspaceID: h.facts.workspaceID, Workspace: h.workspace, AgentID: "agent-a", TaskID: taskID, ParentRunID: parentID, ChildRunID: childID, Status: "completed", Validation: "passed", ReportRef: "multiagent_report.json", ReportSHA256: reportSHA, ParentEventRefs: []string{receipt.ID}, Confidence: "host_validated_terminal_receipt", RecordedAt: time.Now().UTC()}
	encoded, _ := json.Marshal(host)
	scope := memory.Scope{UserID: "owner-a", IncludeUserScope: true, ProjectID: "project-a", TaskID: taskID, AgentID: "agent-a", CorrelationID: parentID}
	outcome, _, err := h.facts.store.Capture(ctx, memory.CaptureRequest{Scope: scope, Content: string(encoded), Summary: "Malformed host task receipt fixture", Category: "fact", Tier: "persist", Source: freewillTaskOutcomeSource, CaptureKey: "malformed-outcome:" + childID})
	if err != nil {
		t.Fatal(err)
	}
	appraisal := freewillTaskAppraisal{Version: 1, Kind: "model_task_self_report", OutcomeID: outcome.ID, ProjectID: "project-a", WorkspaceID: h.facts.workspaceID, TaskID: taskID, ParentRunID: parentID, ChildRunID: childID, Confidence: "model_self_report", AppraisalKind: "unknown", PerceivedOutcome: "unknown", Uncertainty: "synthetic", Narrative: "synthetic", RecordedAt: time.Now().UTC()}
	encoded, _ = json.Marshal(appraisal)
	if _, _, err := h.facts.store.Capture(ctx, memory.CaptureRequest{Scope: scope, Content: string(encoded), Summary: "Synthetic task self-report", Category: "appraisal", Tier: "persist", Source: freewillTaskAppraisalSource, CaptureKey: "malformed-appraisal:" + childID}); err != nil {
		t.Fatal(err)
	}
}

type freewillGoalFake struct {
	output string
	calls  int
	input  string
}

type freewillGoalRelevanceFake struct {
	output, input string
	calls         int
}

func (f *freewillGoalRelevanceFake) ReviewGoalRelevance(_ context.Context, input string) (string, error) {
	f.calls++
	f.input = input
	return f.output, nil
}

func (f *freewillGoalFake) ProposeGoal(_ context.Context, input string) (string, string, string, error) {
	f.calls++
	f.input = input
	return f.output, "local-fake", "synthetic-goal-model", nil
}

type freewillGoalTestHarness struct {
	dataDir, workspace string
	events             *event.SQLiteEventStore
	facts              *freewillTerminalFactMemorySink
	appraisals         *freewillTaskAppraisalSink
}

func newFreewillGoalTestHarness(t *testing.T, childIDs ...string) *freewillGoalTestHarness {
	t.Helper()
	ctx := context.Background()
	dataDir, workspace := t.TempDir(), t.TempDir()
	events, err := event.NewSQLiteEventStore(filepath.Join(dataDir, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	facts, err := newFreewillTerminalFactMemorySink(dataDir, "project-a", workspace, events)
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{narrative: "synthetic qualitative report", provider: "local-fake", model: "synthetic-appraisal", version: "v1"}, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	for i, childID := range childIDs {
		shaByte := string(rune('a' + i))
		fact := multiagent.FreewillTerminalFact{ProjectID: "project-a", Workspace: workspace, AgentID: "agent-a", IntentionID: "intention-" + childID, ParentRunID: "parent-" + childID, ChildRunID: childID, Status: "completed", ReportRef: "multiagent_report.json", ReportSHA256: strings.Repeat(shaByte, 64), ValidationSummary: "passed", RecordedAt: time.Date(2026, 11, 11, 12, i, 0, 0, time.UTC)}
		receipt := event.NewEvent(event.EventFreewillExecutionCompleted, "synthetic-host", map[string]any{"child_run_id": childID, "report_ref": fact.ReportRef, "report_sha256": fact.ReportSHA256, "validation_summary": fact.ValidationSummary})
		receipt.Metadata.RunID = fact.ParentRunID
		if childID != "task-no-event" {
			if err := events.Store(ctx, receipt); err != nil {
				t.Fatal(err)
			}
		}
		if err := facts.CaptureFreewillTerminalFact(ctx, fact); err != nil {
			t.Fatal(err)
		}
		if err := appraisals.Capture(ctx, fact); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = events.Close() })
	return &freewillGoalTestHarness{dataDir: dataDir, workspace: workspace, events: events, facts: facts, appraisals: appraisals}
}

func (h *freewillGoalTestHarness) sink(t *testing.T, p freewillGoalProvider) *freewillGoalProposalSink {
	t.Helper()
	sink, err := newFreewillGoalProposalSink(h.appraisals, p, "owner-a", "project-a", h.workspace, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	return sink
}

func (h *freewillGoalTestHarness) evidence(t *testing.T, children ...string) []freewillGoalEvidence {
	t.Helper()
	var evidence []freewillGoalEvidence
	for _, child := range children {
		rows, err := h.sink(t, &freewillGoalFake{}).readTaskEvidence(context.Background(), child)
		if err != nil {
			t.Fatal(err)
		}
		evidence = append(evidence, rows...)
	}
	return evidence
}

func goalOutput(t *testing.T, ids ...string) string {
	t.Helper()
	value, err := json.Marshal(freewillGoalProposalOutput{Hypothesis: "A possible long-term goal is to explore related work.", SourceIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	return string(value)
}

func TestFreewillGoalProposalUsesTwoExactTaskPairsAndReopensDecision(t *testing.T) {
	ctx := context.Background()
	h := newFreewillGoalTestHarness(t, "task-a", "task-b")
	evidence := h.evidence(t, "task-a", "task-b")
	ids := []string{evidence[0].OutcomeID, evidence[0].SelfReportID, evidence[1].OutcomeID, evidence[1].SelfReportID}
	fake := &freewillGoalFake{output: goalOutput(t, ids[0], ids[2])}
	sink := h.sink(t, fake)
	proposal, err := sink.Propose(ctx, "owner-a", "project-a", "agent-a", []string{"task-a", "task-b"})
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 || len(proposal.SourceIDs) != 2 || !strings.Contains(fake.input, `"host_status":"completed"`) || strings.Contains(fake.input, "synthetic qualitative report") {
		t.Fatalf("provider call or bounded input mismatch calls=%d proposal=%+v", fake.calls, proposal)
	}
	if _, err := sink.Read(ctx, "other-owner", "project-a", "agent-a", proposal.ProposalID); err == nil {
		t.Fatal("wrong owner read accepted")
	}
	if _, err := sink.Propose(ctx, "owner-a", "project-a", "other-agent", []string{"task-a", "task-b"}); err == nil {
		t.Fatal("wrong agent proposal accepted")
	}
	if _, err := sink.Propose(ctx, "owner-a", "project-a", "agent-a", []string{"task-a", "task-a"}); err == nil {
		t.Fatal("duplicate task pair accepted")
	}

	// Reopen local Markdown memory and build a fresh sink. The deterministic
	// request key must return the existing proposal without another provider call.
	facts, err := newFreewillTerminalFactMemorySink(h.dataDir, "project-a", h.workspace, h.events)
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{}, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	reopened := h.sink(t, fake)
	reopened.appraisals = appraisals
	again, err := reopened.Propose(ctx, "owner-a", "project-a", "agent-a", []string{"task-a", "task-b"})
	if err != nil || again.ProposalID != proposal.ProposalID || fake.calls != 1 {
		t.Fatalf("reopened proposal=%+v calls=%d err=%v", again, fake.calls, err)
	}
	read, err := reopened.Read(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID)
	if err != nil || read.Decision != nil || read.Proposal.ProposalID != proposal.ProposalID {
		t.Fatalf("pre-decision reopen=%+v err=%v", read, err)
	}
	decision, err := reopened.Decide(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "accept")
	if err != nil || decision.Decision != "accept" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}

	facts, err = newFreewillTerminalFactMemorySink(h.dataDir, "project-a", h.workspace, h.events)
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err = newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{}, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	finalSink := h.sink(t, fake)
	finalSink.appraisals = appraisals
	final, err := finalSink.Read(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID)
	if err != nil || final.Decision == nil || final.Decision.Decision != "accept" || final.Proposal.Hypothesis != proposal.Hypothesis {
		t.Fatalf("final reopen=%+v err=%v", final, err)
	}
	if _, err := finalSink.Decide(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "reject"); err == nil {
		t.Fatal("conflicting second decision accepted")
	}
}

func TestFreewillGoalLifecycleRequiresExplicitActivationAndReopensImmutablePauseHistory(t *testing.T) {
	ctx := context.Background()
	h := newFreewillGoalTestHarness(t, "task-a", "task-b")
	evidence := h.evidence(t, "task-a", "task-b")
	fake := &freewillGoalFake{output: goalOutput(t, evidence[0].OutcomeID, evidence[1].OutcomeID)}
	sink := h.sink(t, fake)
	proposal, err := sink.Propose(ctx, "owner-a", "project-a", "agent-a", []string{"task-a", "task-b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Activate(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID); err == nil {
		t.Fatal("proposal without explicit owner acceptance activated")
	}
	decision, err := sink.Decide(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "accept")
	if err != nil || decision.Decision != "accept" {
		t.Fatalf("explicit owner choice=%+v err=%v", decision, err)
	}
	accepted, err := sink.Read(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID)
	if err != nil || accepted.Decision == nil || accepted.Goal != nil || len(accepted.GoalHistory) != 0 {
		t.Fatalf("accept implicitly created goal state: record=%+v err=%v", accepted, err)
	}
	if _, err := sink.Pause(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "owner requested pause"); err == nil {
		t.Fatal("pause before explicit activation accepted")
	}
	active, err := sink.Activate(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID)
	if err != nil || active.Goal == nil || active.Goal.State != "active" || active.Goal.Revision != 1 || len(active.GoalHistory) != 1 || active.GoalHistory[0].State != "active" {
		t.Fatalf("explicit activation=%+v err=%v", active, err)
	}
	activeAgain, err := sink.Activate(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID)
	if err != nil || activeAgain.Goal == nil || activeAgain.Goal.GoalID != active.Goal.GoalID || len(activeAgain.GoalHistory) != 1 || activeAgain.GoalHistory[0].RecordID != active.GoalHistory[0].RecordID {
		t.Fatalf("activation replay changed history=%+v err=%v", activeAgain, err)
	}
	reason := "owner requested a bounded pause"
	if _, err := sink.Pause(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, " "); err == nil {
		t.Fatal("empty pause reason accepted")
	}
	if _, err := sink.Pause(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, strings.Repeat("x", 257)); err == nil {
		t.Fatal("oversized pause reason accepted")
	}
	paused, err := sink.Pause(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, reason)
	if err != nil || paused.Goal == nil || paused.Goal.GoalID != active.Goal.GoalID || paused.Goal.State != "paused" || paused.Goal.Revision != 2 || len(paused.GoalHistory) != 2 {
		t.Fatalf("pause=%+v err=%v", paused, err)
	}
	if paused.GoalHistory[0].Revision != 1 || paused.GoalHistory[0].State != "active" || paused.GoalHistory[0].RecordID != active.GoalHistory[0].RecordID ||
		paused.GoalHistory[1].Revision != 2 || paused.GoalHistory[1].State != "paused" || paused.GoalHistory[1].Reason != reason {
		t.Fatalf("goal state history lost immutable predecessor or pause reason: %+v", paused.GoalHistory)
	}
	pausedAgain, err := sink.Pause(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, reason)
	if err != nil || len(pausedAgain.GoalHistory) != 2 || pausedAgain.GoalHistory[1].RecordID != paused.GoalHistory[1].RecordID {
		t.Fatalf("identical pause replay changed history=%+v err=%v", pausedAgain, err)
	}
	if _, err := sink.Pause(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "different reason"); err == nil {
		t.Fatal("conflicting pause overwrote immutable state")
	}
	if strings.Contains(fake.input, reason) || fake.calls != 1 {
		t.Fatalf("pause reason entered model input or triggered another provider call: calls=%d", fake.calls)
	}

	facts, err := newFreewillTerminalFactMemorySink(h.dataDir, "project-a", h.workspace, h.events)
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{}, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := newFreewillGoalProposalSink(appraisals, fake, "owner-a", "project-a", h.workspace, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	read, err := reopened.Read(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID)
	if err != nil || read.Decision == nil || read.Decision.Decision != "accept" || read.Goal == nil || read.Goal.GoalID != paused.Goal.GoalID ||
		read.Goal.WorkspaceID == "" || read.Goal.State != "paused" || read.Goal.Revision != 2 || len(read.GoalHistory) != 2 ||
		read.GoalHistory[1].Reason != reason || read.GoalHistory[0].RecordID != active.GoalHistory[0].RecordID || read.GoalHistory[1].RecordID != paused.GoalHistory[1].RecordID {
		t.Fatalf("reopened goal lifecycle=%+v err=%v", read, err)
	}
	for _, scope := range [][3]string{{"wrong-owner", "project-a", "agent-a"}, {"owner-a", "wrong-project", "agent-a"}, {"owner-a", "project-a", "wrong-agent"}} {
		if _, err := reopened.Read(ctx, scope[0], scope[1], scope[2], proposal.ProposalID); err == nil {
			t.Fatalf("out-of-scope goal read accepted: %v", scope)
		}
	}
	otherFacts, err := newFreewillTerminalFactMemorySink(h.dataDir, "project-a", t.TempDir(), h.events)
	if err != nil {
		t.Fatal(err)
	}
	otherAppraisals, err := newFreewillTaskAppraisalSink(otherFacts, &freewillRetrospectiveFake{}, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	otherWorkspace, err := newFreewillGoalProposalSink(otherAppraisals, fake, "owner-a", "project-a", otherFacts.workspace, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherWorkspace.Read(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID); err == nil {
		t.Fatal("goal lifecycle read crossed configured workspace")
	}
}

func TestFreewillGoalResumeAppendsLinkedRevisionAndReopensExactScope(t *testing.T) {
	ctx := context.Background()
	h := newFreewillGoalTestHarness(t, "task-a", "task-b")
	evidence := h.evidence(t, "task-a", "task-b")
	fake := &freewillGoalFake{output: goalOutput(t, evidence[0].OutcomeID, evidence[1].OutcomeID)}
	sink := h.sink(t, fake)
	proposal, err := sink.Propose(ctx, "owner-a", "project-a", "agent-a", []string{"task-a", "task-b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Decide(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "accept"); err != nil {
		t.Fatal(err)
	}
	active, err := sink.Activate(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Resume(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "should not resume active revision one"); err == nil {
		t.Fatal("active revision one skipped the required pause state")
	}
	paused, err := sink.Pause(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "owner paused this record")
	if err != nil {
		t.Fatal(err)
	}
	resumeReason := "owner resumes the goal record"
	if _, err := sink.Resume(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, " "); err == nil {
		t.Fatal("empty resume reason accepted")
	}
	if _, err := sink.Resume(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, strings.Repeat("r", 257)); err == nil {
		t.Fatal("oversized resume reason accepted")
	}
	for _, scope := range [][3]string{{"wrong-owner", "project-a", "agent-a"}, {"owner-a", "wrong-project", "agent-a"}, {"owner-a", "project-a", "wrong-agent"}} {
		if _, err := sink.Resume(ctx, scope[0], scope[1], scope[2], proposal.ProposalID, resumeReason); err == nil {
			t.Fatalf("out-of-scope resume accepted: %v", scope)
		}
	}
	resumed, err := sink.Resume(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, resumeReason)
	if err != nil || resumed.Goal == nil || resumed.Goal.GoalID != active.Goal.GoalID || resumed.Goal.State != "active" || resumed.Goal.Revision != 3 || len(resumed.GoalHistory) != 3 {
		t.Fatalf("resume failed: record=%+v err=%v", resumed, err)
	}
	if resumed.GoalHistory[0].RecordID != active.GoalHistory[0].RecordID || resumed.GoalHistory[1].RecordID != paused.GoalHistory[1].RecordID ||
		resumed.GoalHistory[2].Revision != 3 || resumed.GoalHistory[2].State != "active" || resumed.GoalHistory[2].Transition != "owner_resume" ||
		resumed.GoalHistory[2].PreviousRecordID != paused.GoalHistory[1].RecordID || resumed.GoalHistory[2].Reason != resumeReason {
		t.Fatalf("resume did not append a linked immutable third revision: %+v", resumed.GoalHistory)
	}
	replayed, err := sink.Resume(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, resumeReason)
	if err != nil || len(replayed.GoalHistory) != 3 || replayed.GoalHistory[2].RecordID != resumed.GoalHistory[2].RecordID {
		t.Fatalf("identical resume replay appended or changed history: record=%+v err=%v", replayed, err)
	}
	if _, err := sink.Resume(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "different resume reason"); err == nil {
		t.Fatal("conflicting resume replay changed immutable history")
	}
	if strings.Contains(fake.input, resumeReason) || fake.calls != 1 {
		t.Fatalf("resume reason entered model input or triggered another provider call: calls=%d", fake.calls)
	}
	facts, err := newFreewillTerminalFactMemorySink(h.dataDir, "project-a", h.workspace, h.events)
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{}, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := newFreewillGoalProposalSink(appraisals, fake, "owner-a", "project-a", h.workspace, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	read, err := reopened.Read(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID)
	if err != nil || read.Decision == nil || read.Decision.Decision != "accept" || read.Goal == nil || read.Goal.GoalID != active.Goal.GoalID ||
		read.Goal.State != "active" || read.Goal.Revision != 3 || len(read.GoalHistory) != 3 ||
		read.GoalHistory[0].RecordID != active.GoalHistory[0].RecordID || read.GoalHistory[1].RecordID != paused.GoalHistory[1].RecordID ||
		read.GoalHistory[2].RecordID != resumed.GoalHistory[2].RecordID || read.GoalHistory[2].PreviousRecordID != paused.GoalHistory[1].RecordID || read.GoalHistory[2].Reason != resumeReason {
		t.Fatalf("reopened exact-scope three-revision history mismatch: record=%+v err=%v", read, err)
	}
	if _, err := reopened.Read(ctx, "other-owner", "project-a", "agent-a", proposal.ProposalID); err == nil {
		t.Fatal("reopened lifecycle read crossed configured owner")
	}
}

func TestFreewillGoalProgressCheckInBindsActiveGoalReceiptAndReopensHistory(t *testing.T) {
	ctx := context.Background()
	h := newFreewillGoalTestHarness(t, "task-a", "task-b", "task-no-event")
	evidence := h.evidence(t, "task-a", "task-b")
	fake := &freewillGoalFake{output: goalOutput(t, evidence[0].OutcomeID, evidence[1].OutcomeID)}
	sink := h.sink(t, fake)
	proposal, err := sink.Propose(ctx, "owner-a", "project-a", "agent-a", []string{"task-a", "task-b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sink.Decide(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "accept"); err != nil {
		t.Fatal(err)
	}
	active, err := sink.Activate(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.CheckIn(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, 1, active.Goal.GoalID, "empty-event", "task-no-event", ""); err == nil {
		t.Fatal("host outcome without a matching parent receipt event was accepted")
	}
	addMalformedGoalTaskEvidence(t, h, "task-malformed")
	if _, err := sink.CheckIn(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, 1, active.Goal.GoalID, "malformed", "task-malformed", ""); err == nil {
		t.Fatal("malformed terminal outcome version was accepted")
	}
	for _, reviewOutput := range []freewillGoalRelevanceOutput{{Classification: "uncertain", Reason: "invented citation", SourceIDs: []string{"model-invented-source"}}} {
		encoded, _ := json.Marshal(reviewOutput)
		malicious := &freewillGoalRelevanceFake{output: string(encoded)}
		sink.relevanceReviewer = malicious
		if _, err := sink.CheckIn(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, 1, active.Goal.GoalID, "invented-ref", "task-a", ""); err == nil || malicious.calls != 1 || !strings.Contains(malicious.input, proposal.Hypothesis) {
			t.Fatalf("invented model source ref or missing goal-bound assessment was accepted: err=%v calls=%d", err, malicious.calls)
		}
	}
	validReview, _ := json.Marshal(freewillGoalRelevanceOutput{Classification: "uncertain", Reason: "tentative synthetic relevance hypothesis", SourceIDs: []string{evidence[0].OutcomeID, evidence[0].SelfReportID}})
	reviewFake := &freewillGoalRelevanceFake{output: string(validReview)}
	sink.relevanceReviewer = reviewFake
	beforeEvents, err := h.events.Query(ctx, event.EventFilter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	localStore := h.facts.store.Local
	failingStore := &failFreewillGoalReviewStore{MemoryStore: localStore, fail: true}
	h.facts.store.Local = failingStore
	_, err = sink.CheckIn(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, 1, active.Goal.GoalID, "checkin-1", "task-a", "owner says this was useful")
	h.facts.store.Local = localStore
	if err == nil {
		t.Fatal("injected review persistence failure was not observed")
	}
	result, err := sink.CheckIn(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, 1, active.Goal.GoalID, "checkin-1", "task-a", "owner says this was useful")
	if err != nil || reviewFake.calls != 1 {
		t.Fatalf("check-in recovery failed or repeated local assessment: calls=%d err=%v", reviewFake.calls, err)
	}
	if len(result.Progress) != 1 || len(result.Reviews) != 1 || result.Progress[0].GoalID != active.Goal.GoalID || result.Progress[0].GoalRevision != 1 ||
		result.Progress[0].ChildRunID != "task-a" || result.Progress[0].ReceiptRef != evidence[0].OutcomeID || result.Progress[0].ReceiptEventID == "" || !validFreewillFactSHA256(result.Progress[0].ReportSHA256) || result.Progress[0].SelfReportRef != evidence[0].SelfReportID ||
		result.Progress[0].OwnerSelfReport != "owner says this was useful" || result.Reviews[0].Classification != "uncertain" || result.Reviews[0].ProgressRecordID != result.Progress[0].RecordID ||
		result.Reviews[0].PreviousReviewID != "" || len(result.Reviews[0].SourceIDs) != 2 || result.Reviews[0].SourceIDs[0] != result.Progress[0].ReceiptRef || result.Reviews[0].SourceIDs[1] != result.Progress[0].SelfReportRef || result.Reviews[0].Provider != "local-fake" || result.Reviews[0].Confidence != "tentative_model_hypothesis" || result.Goal.State != "active" || result.Goal.Revision != 1 || len(result.GoalHistory) != 1 {
		t.Fatalf("check-in/review association mismatch: %+v", result)
	}
	replay, err := sink.CheckIn(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, 1, active.Goal.GoalID, "checkin-1", "task-a", "owner says this was useful")
	if err != nil || len(replay.Progress) != 1 || replay.Progress[0].RecordID != result.Progress[0].RecordID || len(replay.Reviews) != 1 || replay.Reviews[0].RecordID != result.Reviews[0].RecordID {
		t.Fatalf("exact replay changed append-only history: %+v err=%v", replay, err)
	}
	if reviewFake.calls != 1 {
		t.Fatalf("exact replay repeated relevance assessment: calls=%d", reviewFake.calls)
	}
	for _, c := range []struct {
		owner, project, agent        string
		rev                          int
		goal, request, child, report string
	}{
		{"other-owner", "project-a", "agent-a", 1, active.Goal.GoalID, "bad-owner", "task-a", ""},
		{"owner-a", "other-project", "agent-a", 1, active.Goal.GoalID, "bad-project", "task-a", ""},
		{"owner-a", "project-a", "other-agent", 1, active.Goal.GoalID, "bad-agent", "task-a", ""},
		{"owner-a", "project-a", "agent-a", 2, active.Goal.GoalID, "stale", "task-a", ""},
		{"owner-a", "project-a", "agent-a", 1, active.Goal.GoalID, "missing", "no-such-child", ""},
		{"owner-a", "project-a", "agent-a", 1, active.Goal.GoalID, "checkin-1", "task-b", "conflicting"},
	} {
		if _, err := sink.CheckIn(ctx, c.owner, c.project, c.agent, proposal.ProposalID, c.rev, c.goal, c.request, c.child, c.report); err == nil {
			t.Fatalf("negative check-in accepted: %+v", c)
		}
	}
	if _, err := sink.Pause(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "pause before stale check"); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.CheckIn(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, 1, active.Goal.GoalID, "paused", "task-b", ""); err == nil {
		t.Fatal("paused goal accepted progress")
	}
	pausedReplay, err := sink.CheckIn(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, 1, active.Goal.GoalID, "checkin-1", "task-a", "owner says this was useful")
	if err != nil || len(pausedReplay.Progress) != 1 || pausedReplay.Progress[0].RecordID != result.Progress[0].RecordID {
		t.Fatalf("exact historical replay after pause was rejected or changed: %+v err=%v", pausedReplay, err)
	}
	resumed, err := sink.Resume(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "resume for another bounded check-in")
	if err != nil {
		t.Fatal(err)
	}
	sink.relevanceReviewer = freewillLocalFakeGoalRelevanceReviewer{}
	next, err := sink.CheckIn(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, resumed.Goal.Revision, resumed.Goal.GoalID, "checkin-2", "task-b", "new tentative owner report")
	if err != nil || len(next.Progress) != 2 || len(next.Reviews) != 2 || next.Progress[1].GoalRevision != 3 || next.Reviews[1].PreviousReviewID != result.Reviews[0].RecordID || next.Reviews[1].Classification != "uncertain" {
		t.Fatalf("resumed revision failed to append correctly linked check-in: %+v err=%v", next, err)
	}
	duplicateGoalTaskOutcome(t, h, "task-b")
	if _, err := sink.CheckIn(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, resumed.Goal.Revision, resumed.Goal.GoalID, "ambiguous-receipt", "task-b", ""); err == nil {
		t.Fatal("ambiguous host receipt was accepted")
	}
	afterEvents, err := h.events.Query(ctx, event.EventFilter{Limit: 500})
	governedEffects := func(events []event.Event) int {
		count := 0
		for _, item := range events {
			kind := strings.ToLower(string(item.Type))
			if strings.Contains(kind, "approval") || strings.Contains(kind, "dispatch") || strings.Contains(kind, "tool.") || strings.Contains(kind, "selection") {
				count++
			}
		}
		return count
	}
	if err != nil || governedEffects(beforeEvents) != governedEffects(afterEvents) {
		t.Fatalf("progress changed governed approval/dispatch/tool/selection events: before=%d after=%d err=%v", governedEffects(beforeEvents), governedEffects(afterEvents), err)
	}
	if fake.calls != 1 {
		t.Fatalf("relevance check called proposal provider: %d", fake.calls)
	}
	facts, err := newFreewillTerminalFactMemorySink(h.dataDir, "project-a", h.workspace, h.events)
	if err != nil {
		t.Fatal(err)
	}
	appraisals, err := newFreewillTaskAppraisalSink(facts, &freewillRetrospectiveFake{}, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := newFreewillGoalProposalSink(appraisals, fake, "owner-a", "project-a", h.workspace, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	read, err := reopened.Read(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID)
	if err != nil || len(read.Progress) != 2 || read.Progress[0].RecordID != result.Progress[0].RecordID || read.Progress[1].RecordID != next.Progress[1].RecordID || len(read.Reviews) != 2 || read.Reviews[0].RecordID != result.Reviews[0].RecordID || read.Reviews[1].RecordID != next.Reviews[1].RecordID || read.Goal.State != "active" || read.Goal.Revision != 3 {
		t.Fatalf("reopened progress/review or independent lifecycle history mismatch: %+v err=%v", read, err)
	}
}

func TestFreewillGoalRejectedChoiceCannotActivate(t *testing.T) {
	ctx := context.Background()
	h := newFreewillGoalTestHarness(t, "task-a", "task-b")
	evidence := h.evidence(t, "task-a", "task-b")
	fake := &freewillGoalFake{output: goalOutput(t, evidence[0].OutcomeID, evidence[1].OutcomeID)}
	sink := h.sink(t, fake)
	proposal, err := sink.Propose(ctx, "owner-a", "project-a", "agent-a", []string{"task-a", "task-b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Decide(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "reject"); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Activate(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID); err == nil {
		t.Fatal("rejected proposal activated")
	}
}

func TestFreewillGoalLifecycleSerializesIndependentSinkPauseReplay(t *testing.T) {
	ctx := context.Background()
	h := newFreewillGoalTestHarness(t, "task-a", "task-b")
	evidence := h.evidence(t, "task-a", "task-b")
	fake := &freewillGoalFake{output: goalOutput(t, evidence[0].OutcomeID, evidence[1].OutcomeID)}
	first := h.sink(t, fake)
	proposal, err := first.Propose(ctx, "owner-a", "project-a", "agent-a", []string{"task-a", "task-b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Decide(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "accept"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Activate(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID); err != nil {
		t.Fatal(err)
	}
	facts1, err := newFreewillTerminalFactMemorySink(h.dataDir, "project-a", h.workspace, h.events)
	if err != nil {
		t.Fatal(err)
	}
	facts2, err := newFreewillTerminalFactMemorySink(h.dataDir, "project-a", h.workspace, h.events)
	if err != nil {
		t.Fatal(err)
	}
	appraisals1, _ := newFreewillTaskAppraisalSink(facts1, &freewillRetrospectiveFake{}, "owner-a")
	appraisals2, _ := newFreewillTaskAppraisalSink(facts2, &freewillRetrospectiveFake{}, "owner-a")
	second, err := newFreewillGoalProposalSink(appraisals1, fake, "owner-a", "project-a", h.workspace, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	third, err := newFreewillGoalProposalSink(appraisals2, fake, "owner-a", "project-a", h.workspace, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, sink := range []*freewillGoalProposalSink{second, third} {
		wg.Add(1)
		go func(s *freewillGoalProposalSink) {
			defer wg.Done()
			<-start
			_, err := s.Pause(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, "paused by owner")
			results <- err
		}(sink)
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("same pause replay failed across sinks: %v", err)
		}
	}
	read, err := third.Read(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID)
	if err != nil || read.Goal == nil || read.Goal.State != "paused" || read.Goal.Revision != 2 || len(read.GoalHistory) != 2 {
		t.Fatalf("parallel pause produced duplicate or partial history: record=%+v err=%v", read, err)
	}
}

func TestFreewillGoalProposalRejectsOneTaskOnlyAndForgedCitations(t *testing.T) {
	ctx := context.Background()
	h := newFreewillGoalTestHarness(t, "task-a", "task-b")
	evidence := h.evidence(t, "task-a", "task-b")
	for _, ids := range [][]string{{evidence[0].OutcomeID}, {evidence[0].OutcomeID, "forged-source-id"}} {
		fake := &freewillGoalFake{output: goalOutput(t, ids...)}
		sink := h.sink(t, fake)
		if _, err := sink.Propose(ctx, "owner-a", "project-a", "agent-a", []string{"task-a", "task-b"}); err == nil {
			t.Fatalf("invalid citation list accepted: %#v", ids)
		}
	}
}

func TestFreewillGoalDecisionSerializesConflictingSinks(t *testing.T) {
	ctx := context.Background()
	h := newFreewillGoalTestHarness(t, "task-a", "task-b")
	evidence := h.evidence(t, "task-a", "task-b")
	fake := &freewillGoalFake{output: goalOutput(t, evidence[0].OutcomeID, evidence[1].OutcomeID)}
	proposal, err := h.sink(t, fake).Propose(ctx, "owner-a", "project-a", "agent-a", []string{"task-a", "task-b"})
	if err != nil {
		t.Fatal(err)
	}
	// Independent sinks share only the configured durable claim root and local
	// store. Opposite choices race; exactly one immutable record may win.
	firstFacts, err := newFreewillTerminalFactMemorySink(h.dataDir, "project-a", h.workspace, h.events)
	if err != nil {
		t.Fatal(err)
	}
	secondFacts, err := newFreewillTerminalFactMemorySink(h.dataDir, "project-a", h.workspace, h.events)
	if err != nil {
		t.Fatal(err)
	}
	firstAppraisals, _ := newFreewillTaskAppraisalSink(firstFacts, &freewillRetrospectiveFake{}, "owner-a")
	secondAppraisals, _ := newFreewillTaskAppraisalSink(secondFacts, &freewillRetrospectiveFake{}, "owner-a")
	first, err := newFreewillGoalProposalSink(firstAppraisals, fake, "owner-a", "project-a", h.workspace, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := newFreewillGoalProposalSink(secondAppraisals, fake, "owner-a", "project-a", h.workspace, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, item := range []struct {
		sink   *freewillGoalProposalSink
		choice string
	}{{first, "accept"}, {second, "reject"}} {
		wg.Add(1)
		go func(s *freewillGoalProposalSink, choice string) {
			defer wg.Done()
			<-start
			_, err := s.Decide(ctx, "owner-a", "project-a", "agent-a", proposal.ProposalID, choice)
			results <- err
		}(item.sink, item.choice)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	var errorsSeen []string
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, multiagent.ErrRunClaimed) || strings.Contains(err.Error(), "already has an immutable decision") || strings.Contains(err.Error(), "goal proposal") {
			conflicts++
			errorsSeen = append(errorsSeen, err.Error())
		} else {
			errorsSeen = append(errorsSeen, err.Error())
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("conflicting cross-sink decisions successes=%d conflicts=%d errors=%v", successes, conflicts, errorsSeen)
	}
}

func TestFreewillGoalProposalRequiresBearerTokenWhenEnabled(t *testing.T) {
	if freewillGoalProposalAuthAllowed(true, " ") {
		t.Fatal("enabled goal proposals accepted an empty API bearer token")
	}
	if !freewillGoalProposalAuthAllowed(true, "synthetic-token") || !freewillGoalProposalAuthAllowed(false, "") {
		t.Fatal("goal bearer-token gate rejected valid state")
	}
}

var _ api.FreewillGoalProposalController = (*freewillServeRuntime)(nil)
