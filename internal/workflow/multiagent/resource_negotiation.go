package multiagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/emaharmony/prizm/internal/event"
)

var ErrResourceExtensionPending = errors.New("multiagent: resource extension requires an owner decision")

func resourceRequestPending(record DurableRun) bool {
	if record.ResourceNegotiation == nil || record.Waiting == nil || record.Waiting.Kind != "resource_extension" {
		return false
	}
	for i := len(record.ResourceNegotiation.Requests) - 1; i >= 0; i-- {
		if record.ResourceNegotiation.Requests[i].Status == "pending" {
			return true
		}
	}
	return false
}

func resourceExtensionCanBeRequested(record DurableRun, transition ResolvedTransition) bool {
	state := record.ResourceNegotiation
	if state == nil || !state.Config.Enabled || transition.Terminal != "" || transition.To == "" ||
		state.Allowance <= 0 || record.State.TransitionCount+1 < state.Allowance ||
		state.Allowance+state.Config.Increment > state.Config.HardCeiling || resourceRequestPending(record) {
		return false
	}
	return true
}

func (r *DurableRuntime) createResourceExtensionRequest(record *DurableRun, intent *ResourceExtensionIntent) {
	if record == nil || record.ResourceNegotiation == nil || record.PendingTransition == nil || intent == nil {
		return
	}
	state := record.ResourceNegotiation
	cfg := state.Config
	requestID := resourceExtensionRequestID(record.State.RunID, record.LastCompletedExecutionKey, state.Generation, state.Allowance)
	request := ResourceExtensionRequestRecord{
		RequestID: requestID, RunID: record.State.RunID, ParentRunID: cfg.ParentRunID,
		AgentID: cfg.AgentID, ProjectID: cfg.ProjectID, Workspace: cfg.Workspace, OwnerID: cfg.OwnerID,
		GrantGeneration: cfg.GrantGeneration, CheckpointRevision: record.Revision + 1,
		FromRole: record.PendingTransition.Resolved.From, NextRole: record.PendingTransition.Resolved.To,
		ExecutionKey: record.PendingTransition.ExecutionKey, Reason: intent.Reason,
		CurrentAllowance: state.Allowance, RequestedIncrement: cfg.Increment,
		HardCeiling: cfg.HardCeiling, Generation: state.Generation, Status: "pending", CreatedAt: r.now().UTC(),
	}
	state.Requests = append(state.Requests, request)
	now := r.now().UTC()
	record.State.Status = RunStatusPaused
	record.State.UpdatedAt = now
	record.Waiting = &WaitingState{Kind: "resource_extension", Reason: "owner decision required", SafeToRetry: false, Since: now}
	r.supervisor.emitRun(event.EventMultiAgentRunPaused, record.State, "resource extension decision required")
	r.buffer.Emit(event.NewEvent(event.EventMultiAgentResourceExtensionRequested, durableEventSource, map[string]any{
		"run_id": request.RunID, "workflow_id": record.State.WorkflowID, "request_id": request.RequestID,
		"parent_run_id": request.ParentRunID, "agent_id": request.AgentID, "project_id": request.ProjectID,
		"grant_generation":    request.GrantGeneration,
		"checkpoint_revision": request.CheckpointRevision, "from_role": string(request.FromRole),
		"next_role": string(request.NextRole), "current_allowance": request.CurrentAllowance,
		"requested_increment": request.RequestedIncrement, "hard_ceiling": request.HardCeiling,
		"generation": request.Generation,
	}).WithCorrelationID(record.State.RunID).WithMetadata(event.EventMetadata{RunID: record.State.RunID}))
}

func resourceExtensionRequestID(runID, executionKey string, generation uint64, allowance int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d", runID, executionKey, generation, allowance)))
	return "rxe_" + hex.EncodeToString(digest[:12])
}

// InspectResourceExtension returns the latest request bound to the run.
func (r *DurableRuntime) InspectResourceExtension(ctx context.Context, runID string) (ResourceExtensionRequestRecord, error) {
	record, err := r.store.Load(ctx, runID)
	if err != nil {
		return ResourceExtensionRequestRecord{}, err
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		return ResourceExtensionRequestRecord{}, err
	}
	if record.ResourceNegotiation == nil || len(record.ResourceNegotiation.Requests) == 0 {
		return ResourceExtensionRequestRecord{}, errors.New("multiagent: resource extension request is unavailable")
	}
	return record.ResourceNegotiation.Requests[len(record.ResourceNegotiation.Requests)-1], nil
}

// DecideResourceExtension records one exact owner decision. Acceptance updates
// only the run-local transition allowance; the caller must separately resume.
func (r *DurableRuntime) DecideResourceExtension(ctx context.Context, runID, requestID string, expectedRevision int64, decision, actor, reason string) (ResourceExtensionDecision, error) {
	if decision != "accept" && decision != "reject" || strings.TrimSpace(actor) == "" || len(actor) > 128 || len(reason) > 256 {
		return ResourceExtensionDecision{}, errors.New("multiagent: resource extension decision is invalid")
	}
	if err := validateResourceDecisionText(actor, reason); err != nil {
		return ResourceExtensionDecision{}, err
	}
	claim, err := r.claimer.Acquire(ctx, runID)
	if err != nil {
		return ResourceExtensionDecision{}, err
	}
	defer claim.Release()
	record, err := r.store.Load(ctx, runID)
	if err != nil {
		return ResourceExtensionDecision{}, err
	}
	if err := record.Validate(r.supervisor.graph); err != nil {
		return ResourceExtensionDecision{}, err
	}
	state := record.ResourceNegotiation
	if state == nil || actor != state.Config.OwnerID {
		return ResourceExtensionDecision{}, errors.New("multiagent: resource extension owner does not match")
	}
	index := -1
	for i := range state.Requests {
		if state.Requests[i].RequestID == requestID {
			index = i
			break
		}
	}
	if index < 0 {
		return ResourceExtensionDecision{}, errors.New("multiagent: resource extension request is unknown")
	}
	for _, prior := range state.Decisions {
		if prior.RequestID == requestID {
			if prior.Decision == decision && prior.Actor == actor && prior.Reason == reason {
				return prior, nil
			}
			return ResourceExtensionDecision{}, errors.New("multiagent: resource extension decision conflicts with prior decision")
		}
	}
	request := &state.Requests[index]
	if request.Status != "pending" || !resourceRequestPending(record) || record.Phase != CheckpointTransitionPending ||
		record.PendingTransition == nil || request.CheckpointRevision != expectedRevision || expectedRevision != record.Revision ||
		request.ExecutionKey != record.PendingTransition.ExecutionKey || request.CurrentAllowance != state.Allowance ||
		request.HardCeiling != int(r.supervisor.graph.Budgets().MaxTransitions) ||
		request.CurrentAllowance+request.RequestedIncrement > request.HardCeiling ||
		request.OwnerID != state.Config.OwnerID || request.ParentRunID != state.Config.ParentRunID ||
		request.AgentID != state.Config.AgentID || request.ProjectID != state.Config.ProjectID || request.Workspace != state.Config.Workspace ||
		request.GrantGeneration != state.Config.GrantGeneration {
		return ResourceExtensionDecision{}, errors.New("multiagent: resource extension request is stale or outside scope")
	}
	newAllowance := state.Allowance
	if decision == "accept" {
		newAllowance += request.RequestedIncrement
		if newAllowance > request.HardCeiling {
			return ResourceExtensionDecision{}, errors.New("multiagent: resource extension exceeds hard ceiling")
		}
	}
	now := r.now().UTC()
	result := ResourceExtensionDecision{RequestID: requestID, Decision: decision, Actor: actor, Reason: reason, Allowance: newAllowance, DecidedAt: now}
	state.Decisions = append(state.Decisions, result)
	request.Status = decision
	r.buffer.Emit(event.NewEvent(event.EventMultiAgentResourceExtensionDecided, durableEventSource, map[string]any{
		"run_id": runID, "workflow_id": record.State.WorkflowID, "request_id": requestID,
		"decision": decision, "actor": actor, "allowance": newAllowance,
		"hard_ceiling": request.HardCeiling, "generation": request.Generation,
	}).WithCorrelationID(runID).WithMetadata(event.EventMetadata{RunID: runID}))
	if decision == "accept" {
		r.supervisor.emitRun(event.EventMultiAgentRunResumed, record.State, "resource extension decision accepted")
		state.Allowance = newAllowance
		state.Generation++
		record.Waiting = nil
		record.State.Status = RunStatusRunning
		record.State.UpdatedAt = now
		saved, saveErr := r.checkpoint(ctx, record)
		if saveErr != nil {
			return ResourceExtensionDecision{}, saveErr
		}
		return saved.ResourceNegotiation.Decisions[len(saved.ResourceNegotiation.Decisions)-1], nil
	}
	budgetErr := newBudgetError("max_transitions", "", state.Allowance+1, Limit(state.Allowance))
	terminalState, terminalErr := r.supervisor.exhaustRun(record.State, budgetErr)
	record.State = terminalState
	record.Phase = CheckpointTerminal
	record.PendingTransition = nil
	record.Waiting = nil
	record.Failure = persistedFailure("budget_exhausted", terminalErr, now)
	if _, saveErr := r.checkpoint(ctx, record); saveErr != nil {
		return ResourceExtensionDecision{}, saveErr
	}
	return result, nil
}

func validateResourceDecisionText(actor, reason string) error {
	for _, value := range []string{actor, reason} {
		for _, r := range value {
			if unicode.IsControl(r) {
				return errors.New("multiagent: resource extension decision text is invalid")
			}
		}
	}
	return nil
}

func validateResourceNegotiationState(record DurableRun, graph *CompiledGraph) []string {
	var problems []string
	state := record.ResourceNegotiation
	if state == nil {
		if record.Waiting != nil && record.Waiting.Kind == "resource_extension" {
			problems = append(problems, "resource extension wait requires negotiation state")
		}
		return problems
	}
	cfg := state.Config
	if !cfg.Enabled || cfg.InitialAllowance <= 0 || cfg.Increment <= 0 || cfg.HardCeiling <= 0 ||
		cfg.HardCeiling != int(graph.Budgets().MaxTransitions) || cfg.InitialAllowance+cfg.Increment > cfg.HardCeiling ||
		strings.TrimSpace(cfg.OwnerID) == "" || strings.TrimSpace(cfg.ParentRunID) == "" ||
		strings.TrimSpace(cfg.AgentID) == "" || strings.TrimSpace(cfg.ProjectID) == "" || strings.TrimSpace(cfg.Workspace) == "" {
		problems = append(problems, "resource negotiation config does not match the immutable graph ceiling and exact host scope")
		return problems
	}
	if state.Allowance < cfg.InitialAllowance || state.Allowance > cfg.HardCeiling {
		problems = append(problems, "resource negotiation allowance is outside its initial and hard ceilings")
	}
	decisionByRequest := make(map[string]ResourceExtensionDecision, len(state.Decisions))
	accepted := 0
	for _, decision := range state.Decisions {
		if decision.RequestID == "" || (decision.Decision != "accept" && decision.Decision != "reject") ||
			decision.Actor != cfg.OwnerID || decision.Allowance < cfg.InitialAllowance || decision.Allowance > cfg.HardCeiling {
			problems = append(problems, "resource negotiation decision is invalid")
			break
		}
		if _, exists := decisionByRequest[decision.RequestID]; exists {
			problems = append(problems, "resource negotiation request has duplicate decisions")
			break
		}
		decisionByRequest[decision.RequestID] = decision
		if decision.Decision == "accept" {
			accepted++
		}
	}
	if state.Generation != uint64(accepted) {
		problems = append(problems, "resource negotiation generation does not match accepted decisions")
	}
	wantAllowance := cfg.InitialAllowance
	seen := make(map[string]struct{}, len(state.Requests))
	pending := 0
	for _, request := range state.Requests {
		if request.RequestID == "" || request.RunID != record.State.RunID || request.ParentRunID != cfg.ParentRunID ||
			request.AgentID != cfg.AgentID || request.ProjectID != cfg.ProjectID || request.Workspace != cfg.Workspace ||
			request.OwnerID != cfg.OwnerID || request.GrantGeneration != cfg.GrantGeneration ||
			request.CurrentAllowance <= 0 || request.RequestedIncrement != cfg.Increment || request.HardCeiling != cfg.HardCeiling ||
			request.CurrentAllowance+request.RequestedIncrement > request.HardCeiling || request.Reason == "" ||
			len(request.Reason) > 256 || request.CheckpointRevision <= 0 || request.ExecutionKey == "" ||
			request.FromRole == "" || request.NextRole == "" {
			problems = append(problems, "resource extension request binding is invalid")
			break
		}
		if _, exists := seen[request.RequestID]; exists {
			problems = append(problems, "resource extension request id is duplicated")
			break
		}
		seen[request.RequestID] = struct{}{}
		if request.Status == "pending" {
			pending++
			if _, decided := decisionByRequest[request.RequestID]; decided {
				problems = append(problems, "pending resource extension already has a decision")
			}
		} else {
			decision, ok := decisionByRequest[request.RequestID]
			if !ok || request.Status != decision.Decision || request.CurrentAllowance != wantAllowance {
				problems = append(problems, "resource extension request decision history is inconsistent")
			}
			if ok && decision.Decision == "accept" {
				wantAllowance += request.RequestedIncrement
			}
		}
	}
	if wantAllowance != state.Allowance {
		problems = append(problems, "resource negotiation allowance does not match accepted request history")
	}
	if pending > 1 {
		problems = append(problems, "resource negotiation has more than one pending request")
	}
	if pending == 1 {
		request := state.Requests[len(state.Requests)-1]
		if record.Phase != CheckpointTransitionPending || record.State.Status != RunStatusPaused || record.Waiting == nil ||
			record.Waiting.Kind != "resource_extension" || record.PendingTransition == nil ||
			(request.CheckpointRevision != record.Revision && request.CheckpointRevision != record.Revision+1) ||
			request.ExecutionKey != record.PendingTransition.ExecutionKey || request.FromRole != record.PendingTransition.Resolved.From ||
			request.NextRole != record.PendingTransition.Resolved.To {
			problems = append(problems, "pending resource extension does not match its exact transition checkpoint")
		}
	} else if record.Waiting != nil && record.Waiting.Kind == "resource_extension" {
		problems = append(problems, "resource extension wait lacks a pending request")
	}
	for requestID := range decisionByRequest {
		if _, ok := seen[requestID]; !ok {
			problems = append(problems, "resource extension decision lacks its request")
			break
		}
	}
	return problems
}
