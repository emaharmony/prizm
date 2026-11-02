package multiagent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/policy"
)

type freewillApprovalEvaluatorStub struct {
	decision policy.PolicyDecision
}

func (s freewillApprovalEvaluatorStub) Evaluate(policy.PolicyRequest) policy.PolicyDecision {
	return s.decision
}

func TestFreewillApprovalPolicyAllowsAndRequiresApprovalRemainExecutionDisabled(t *testing.T) {
	for _, decision := range []policy.Decision{policy.DecisionAllowed, policy.DecisionRequiresApproval} {
		t.Run(string(decision), func(t *testing.T) {
			env, _, service, record, roleRunner := newFreewillApprovalTestService(t, decision)

			proposed, err := service.RequestApproval(context.Background(), record.State.RunID)
			if err != nil {
				t.Fatalf("request approval: %v", err)
			}
			if proposed.Freewill.Lifecycle != freewill.StateApprovalPending || proposed.Freewill.PolicyDecision != string(decision) ||
				!proposed.Freewill.ExecutionDisabled || proposed.Freewill.ApprovalRef == "" {
				t.Fatalf("proposal state = %#v", proposed.Freewill)
			}

			granted, err := service.Decide(context.Background(), record.State.RunID, proposed.Freewill.ApprovalRef, "grant", "owner", "")
			if err != nil {
				t.Fatalf("grant proposal: %v", err)
			}
			if granted.Freewill.Lifecycle != freewill.StatePaused || granted.Freewill.PauseReason != "execution_disabled" || !granted.Freewill.ExecutionDisabled {
				t.Fatalf("grant crossed decision-only boundary: %#v", granted.Freewill)
			}
			if calls := runnerCalls(roleRunner); len(calls) != 0 {
				t.Fatalf("approval gate executed workflow roles: %v", calls)
			}
			assertFreewillApprovalEventsDoNotExecute(t, env.events, record.State.RunID)
		})
	}
}

func TestFreewillApprovalPolicyDenialIsFinalAndQueryable(t *testing.T) {
	env, _, service, record, roleRunner := newFreewillApprovalTestService(t, policy.DecisionDenied)

	denied, err := service.RequestApproval(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("evaluate denied proposal: %v", err)
	}
	if denied.Freewill.Lifecycle != freewill.StateDenied || denied.Freewill.PolicyDecision != string(policy.DecisionDenied) ||
		denied.Freewill.PauseReason != "policy_denied" || !denied.Freewill.ExecutionDisabled || denied.Freewill.ApprovalRef != "" {
		t.Fatalf("policy denial was not final and execution-disabled: %#v", denied.Freewill)
	}

	status, err := service.Status(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("query denied proposal: %v", err)
	}
	if status.Lifecycle != freewill.StateDenied || status.PolicyDecision != string(policy.DecisionDenied) ||
		status.PauseReason != "policy_denied" || !status.ExecutionDisabled || status.ApprovalID != "" {
		t.Fatalf("queryable status lost policy denial: %#v", status)
	}
	if calls := runnerCalls(roleRunner); len(calls) != 0 {
		t.Fatalf("denied proposal executed workflow roles: %v", calls)
	}
	assertFreewillApprovalEventsDoNotExecute(t, env.events, record.State.RunID)
	events, err := env.events.Query(context.Background(), event.EventFilter{RunID: record.State.RunID, Limit: 100})
	if err != nil {
		t.Fatalf("query denial event: %v", err)
	}
	for _, current := range events {
		if current.Type == event.EventFreewillPolicyDenied {
			return
		}
	}
	t.Fatal("policy denial event was not persisted")
}

func TestFreewillApprovalEditedRequestInvalidatesPriorFingerprint(t *testing.T) {
	_, _, service, record, _ := newFreewillApprovalTestService(t, policy.DecisionRequiresApproval)

	pending, err := service.RequestApproval(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	changed, err := service.store.Load(context.Background(), record.State.RunID)
	if err != nil {
		t.Fatalf("load proposed record: %v", err)
	}
	changed.Freewill.Candidates[0].Request.Goal = "A changed request after approval was proposed"
	changed, err = service.store.Checkpoint(context.Background(), changed.Revision, changed, nil)
	if err != nil {
		t.Fatalf("persist edited request fixture: %v", err)
	}

	if _, err := service.Decide(context.Background(), record.State.RunID, pending.Freewill.ApprovalRef, "grant", "owner", ""); !errors.Is(err, ErrFreewillApprovalStale) {
		t.Fatalf("grant for edited request = %v, want stale approval", err)
	}
	item, err := service.approvals.Load(record.State.RunID, pending.Freewill.ApprovalRef)
	if err != nil {
		t.Fatalf("load approval: %v", err)
	}
	if item.Status != approval.StatusPending {
		t.Fatalf("stale approval was changed to %q", item.Status)
	}
}

func TestFreewillApprovalServiceRejectsMissingPolicy(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runtime := newDurableRuntimeForTest(t, validDefinition(), &scriptedRunner{}, env.store, env.claimer, env.events)
	if _, err := NewFreewillApprovalService(runtime, env.store, approval.NewStore(filepath.Join(t.TempDir(), "runs")), nil, "freewill-policy"); err == nil {
		t.Fatal("approval service composed without a policy evaluator")
	}
}

func newFreewillApprovalTestService(t *testing.T, decision policy.Decision) (durableTestEnvironment, *DurableRuntime, *FreewillApprovalService, DurableRun, *scriptedRunner) {
	t.Helper()
	env := newDurableTestEnvironment(t)
	roleRunner := &scriptedRunner{}
	runtime := newDurableRuntimeForTest(t, validDefinition(), roleRunner, env.store, env.claimer, env.events)
	approvalStore := approval.NewStore(filepath.Join(t.TempDir(), "runs"))
	evaluator := freewillApprovalEvaluatorStub{decision: policy.PolicyDecision{Decision: decision, RuleID: "test-rule"}}
	service, err := NewFreewillApprovalService(runtime, env.store, approvalStore, evaluator, "freewill-policy")
	if err != nil {
		t.Fatalf("compose Freewill approval service: %v", err)
	}
	request := freewill.IntentionRequest{
		RequestID: "request-1", AgentID: "agent-1", Origin: freewill.OriginAgent, CandidateSource: freewill.SourceNew,
		Title: "Read-only review", Goal: "Review a synthetic project", ReasonSummary: "The review is bounded and read-only.",
		RequestedCapabilities: []string{"memory.read"}, Scope: freewill.Scope{ProjectID: "project-1", Workspace: "workspace-1"},
		IdempotencyKey: "idem-1", CorrelationID: "corr-1",
	}
	now := time.Now().UTC()
	record := DurableRun{
		SchemaVersion: DurableRunSchemaVersion, Kind: DurableRunKindFreewill, Phase: CheckpointFreewillWake,
		State: RunState{RunID: "run-freewill-approval", WorkflowID: FreewillWorkflowID, WorkflowVersion: 1,
			RequestIdentity: &RequestIdentity{RequesterID: request.AgentID, IdempotencyKey: request.IdempotencyKey}, CreatedAt: now, UpdatedAt: now},
		Freewill: &FreewillRunState{Lifecycle: freewill.StateProposed, Scope: request.Scope, WakeID: "wake-1", Trigger: "scheduled",
			DedupeKey: request.IdempotencyKey, CorrelationID: request.CorrelationID, Candidates: []freewill.CandidateIntention{{CandidateID: "candidate-1", Request: request}},
			ChoiceID: "candidate-1", ReasonSummary: "Selected the bounded read-only review.", ExecutionDisabled: true},
	}
	if _, err := env.store.Create(context.Background(), record, nil); err != nil {
		t.Fatalf("create proposed Freewill record: %v", err)
	}
	return env, runtime, service, record, roleRunner
}

func assertFreewillApprovalEventsDoNotExecute(t *testing.T, store event.EventStore, runID string) {
	t.Helper()
	events, err := store.Query(context.Background(), event.EventFilter{RunID: runID, Limit: 100})
	if err != nil {
		t.Fatalf("query Freewill events: %v", err)
	}
	for _, current := range events {
		if current.Type == event.EventMultiAgentFanoutChildDispatched || current.Type == event.EventMultiAgentToolDiagnostic || strings.Contains(current.Type, ".tool.") ||
			strings.Contains(current.Type, ".role.") || strings.Contains(current.Type, ".action.") || strings.Contains(current.Type, "proposal.apply_") ||
			current.Type == "prizm.workflow.proposal.apply_started" || current.Type == "prizm.workflow.proposal.apply_completed" ||
			current.Type == "prizm.workflow.role.completed" || current.Type == "prizm.workflow.role.started" || current.Type == "prizm.workflow.action.started" ||
			current.Type == "prizm.workflow.action.completed" {
			t.Fatalf("policy/approval gate emitted execution event %q", current.Type)
		}
	}
}

