package multiagent

import (
	"context"
	"strings"

	"github.com/emaharmony/prizm/internal/event"
)

func reflectionTriggerEnabled(policy *SchemaReflectionPolicy, trigger ReflectionTrigger) bool {
	if policy == nil || !policy.Enabled {
		return false
	}
	if len(policy.Triggers) == 0 {
		return trigger == ReflectionTerminal || trigger == ReflectionFailure || trigger == ReflectionVerificationFail || trigger == ReflectionInterruption || trigger == ReflectionRecovery || trigger == ReflectionApprovalResume
	}
	for _, configured := range policy.Triggers {
		if configured == string(trigger) {
			return true
		}
	}
	return false
}

func (r *DurableRuntime) runReflection(ctx context.Context, record *DurableRun, trigger ReflectionTrigger, input ReflectionInput) bool {
	if r.reflection == nil || !reflectionTriggerEnabled(r.supervisor.graph.ReflectionPolicy(), trigger) {
		return false
	}
	policy := r.supervisor.graph.ReflectionPolicy()
	r.emitReflection(event.EventReflectionRequested, record.State.RunID, input, nil, "")
	roleConfig, ok := r.supervisor.graph.RoleConfig(RoleReflector)
	if !ok {
		r.emitReflection(event.EventReflectionFailed, record.State.RunID, input, nil, "reflector role is not configured")
		return false
	}
	request := ReflectionRequest{Run: r.supervisor.runView(record.State), RoleConfig: roleConfig, Input: input}
	result, err := r.reflection.Reflect(ctx, request)
	if err != nil {
		r.emitReflection(event.EventReflectionFailed, record.State.RunID, input, nil, err.Error())
		return false
	}
	refl := ReflectionRecord{Trigger: trigger, SourceRole: input.SourceRole, ExecutionKey: record.ActiveExecutionKey, Result: result, ReplanCount: record.ReplanCount, CreatedAt: r.now().UTC()}
	if result.LessonCandidate != nil {
		if r.memory == nil || strings.TrimSpace(record.State.WorkspaceID) == "" {
			refl.MemoryStatus = "rejected"
			r.emitReflection(event.EventReflectionMemoryRejected, record.State.RunID, input, &result, "project memory sink or project identity unavailable")
		} else if err := r.memory.StoreReflectionMemory(ctx, record.State.WorkspaceID, result.LessonCandidate.Summary, result.LessonCandidate.Content, result.LessonCandidate.Category, result.LessonCandidate.KeyTopics); err != nil {
			refl.MemoryStatus = "rejected"
			r.emitReflection(event.EventReflectionMemoryRejected, record.State.RunID, input, &result, err.Error())
		} else {
			refl.MemoryStatus = "accepted"
			r.emitReflection(event.EventReflectionMemoryAccepted, record.State.RunID, input, &result, "")
		}
	}
	record.Reflections = append(record.Reflections, refl)
	record.State.LatestReflection = cloneReflectionRecord(&refl)
	r.emitReflection(event.EventReflectionCompleted, record.State.RunID, input, &result, "")
	if result.ReplanRequested && policy != nil {
		maxReplans := policy.MaxReplans
		if maxReplans == 0 {
			maxReplans = 2
		}
		if record.ReplanCount < maxReplans {
			record.ReplanCount++
			target := policy.ReplanRole
			if target == "" {
				target = string(RolePlanner)
			}
			if _, ok := r.supervisor.graph.RoleConfig(Role(target)); ok {
				record.State.CurrentRole = Role(target)
				record.State.Status = RunStatusRunning
				record.State.TerminalOutcome = nil
				record.State.UpdatedAt = r.now().UTC()
				record.PendingTransition = nil
				record.ActiveExecutionKey = ""
				record.Waiting = nil
				record.Phase = CheckpointPendingRole
				r.emitReflection(event.EventReflectionReplanRequested, record.State.RunID, input, &result, result.ReplanReason)
				return true
			}
		}
	}
	return false
}

func (r *DurableRuntime) emitReflection(eventType, runID string, input ReflectionInput, result *ReflectionResult, reason string) {
	payload := map[string]any{"run_id": runID, "trigger": string(input.Trigger), "role": string(input.SourceRole)}
	if result != nil {
		payload["verdict"] = result.Verdict
		payload["confidence"] = result.Confidence
	}
	if reason != "" {
		payload["reason"] = reason
	}
	e := event.NewEvent(eventType, "prizm-reflection-runtime", payload)
	e.Metadata.RunID = runID
	r.buffer.Emit(e)
}

func reflectionFailureInput(role Role, outcome TransitionOutcome, err error, run RunState) ReflectionInput {
	input := ReflectionInput{Trigger: ReflectionFailure, SourceRole: role, Outcome: outcome, Goal: run.CurrentTask.Description}
	if err != nil {
		input.Error = err.Error()
	}
	return input
}
