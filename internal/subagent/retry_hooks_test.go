package subagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/retry"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
)

type recordingRetryHooks struct {
	attempt             RetryAttempt
	consumed            []string
	corrections         []string
	progress            []string
	confirmedNoEffects  []string
	feedback            []string
	nextAt              time.Time
	onConsume           func()
	lastObservationHash string
	noProgressCount     int
	countedAttempts     []string
}

func TestLoopRunnerFailedToolRefreshesAttemptBeforeProgressCount(t *testing.T) {
	backend := &scriptBackend{
		parse:   lineParser,
		turns:   []Turn{{Text: "TOOL read_file"}, {Text: "FINAL: corrected"}},
		toolErr: errors.New("read failed"),
	}
	hooks := &recordingRetryHooks{attempt: RetryAttempt{VisitID: "visit-failed-read", AttemptID: "attempt-1", Number: 1, Remaining: 10}}
	var diagnostics []ToolDiagnostic
	runner := NewLoopRunner(LoopRunnerConfig{Backend: backend})
	result, err := runner.Run(context.Background(), v2.TaskPacket{TaskID: "failed-read"}, AgentRuntime{
		AgentID: "planner", Role: "planner", RunID: "run-failed-read", TaskID: "failed-read", ExecutionKey: "exec-failed-read",
		RetryHooks: hooks,
		ToolEventSink: func(event ToolDiagnostic) error {
			diagnostics = append(diagnostics, event)
			if event.Status == "failed" && event.ObservationHash != "" {
				hooks.lastObservationHash = event.ObservationHash
				hooks.noProgressCount = 1
			}
			return nil
		},
	})
	if err != nil || result.Summary != "corrected" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.LastRecoveryClass != "tool" || result.LastRecoveryCode != "tool_execution_error" || result.LastRecoveryOrigin != RecoveryOriginToolExecutor {
		t.Fatalf("last recovery provenance = class:%q code:%q origin:%q", result.LastRecoveryClass, result.LastRecoveryCode, result.LastRecoveryOrigin)
	}
	if result.RecoveryAttempts != 1 || result.Corrections != 1 {
		t.Fatalf("recovery accounting changed: attempts=%d corrections=%d", result.RecoveryAttempts, result.Corrections)
	}
	if got := backend.toolCalls; len(got) != 1 || got[0] != "read_file" {
		t.Fatalf("tool executions=%v, want one failed read", got)
	}
	if len(hooks.consumed) != 1 || hooks.consumed[0] != "tool_execution_error" || hooks.attempt.AttemptID != "attempt-2" {
		t.Fatalf("recovery state=%#v", hooks)
	}
	if len(hooks.countedAttempts) != 1 || hooks.countedAttempts[0] != "attempt-2" {
		t.Fatalf("progress count attempt identities=%v", hooks.countedAttempts)
	}
	if len(diagnostics) != 2 || diagnostics[0].AttemptID != "attempt-1" || diagnostics[1].AttemptID != "attempt-1" {
		t.Fatalf("action diagnostics lost original attempt identity: %#v", diagnostics)
	}
	if len(backend.messages) < 2 || !strings.Contains(backend.messages[1][len(backend.messages[1])-1].Content, "Tool \"read_file\" failed") {
		t.Fatalf("corrective feedback missing from next provider request: %#v", backend.messages)
	}
}

func (h *recordingRetryHooks) CurrentAttempt() RetryAttempt { return h.attempt }

func (h *recordingRetryHooks) NoProgressCount(expectedAttemptID, observationHash string) (int, error) {
	if expectedAttemptID != h.attempt.AttemptID {
		return 0, errors.New("stale attempt")
	}
	h.countedAttempts = append(h.countedAttempts, expectedAttemptID)
	if observationHash != h.lastObservationHash {
		return 0, nil
	}
	return h.noProgressCount, nil
}

func (h *recordingRetryHooks) ConsumeRecovery(expectedAttemptID, failureCode string) (RetryAttempt, error) {
	if expectedAttemptID != h.attempt.AttemptID {
		return h.attempt, errors.New("stale attempt")
	}
	h.consumed = append(h.consumed, failureCode)
	h.attempt.Number++
	h.attempt.RecoveryNumber++
	h.attempt.Remaining--
	h.attempt.AttemptID = fmt.Sprintf("attempt-%d", h.attempt.Number)
	h.attempt.NextAt = h.nextAt
	if h.onConsume != nil {
		h.onConsume()
	}
	return h.attempt, nil
}

func (h *recordingRetryHooks) RecordProgress(expectedAttemptID, kind, safeRef, observationHash string) error {
	if expectedAttemptID != h.attempt.AttemptID {
		return errors.New("stale attempt")
	}
	h.progress = append(h.progress, kind+":"+safeRef+":"+observationHash)
	return nil
}

func (h *recordingRetryHooks) RecordCorrection(expectedAttemptID, errorClass, code string) error {
	if expectedAttemptID != h.attempt.AttemptID {
		return errors.New("stale attempt")
	}
	h.corrections = append(h.corrections, errorClass+":"+code)
	return nil
}

func (h *recordingRetryHooks) ConfirmNoEffects(expectedAttemptID, mutationID string) error {
	if expectedAttemptID != h.attempt.AttemptID || mutationID == "" {
		return errors.New("stale or missing mutation identity")
	}
	h.confirmedNoEffects = append(h.confirmedNoEffects, mutationID)
	return nil
}

func (h *recordingRetryHooks) RecordCorrectionFeedback(expectedAttemptID, errorClass, code, feedback string) error {
	if expectedAttemptID != h.attempt.AttemptID {
		return errors.New("stale attempt")
	}
	h.corrections = append(h.corrections, errorClass+":"+code)
	h.feedback = append(h.feedback, feedback)
	return nil
}

func TestLoopRunner_MalformedToolInputConsumesSharedRecoveryAndKeepsFinalProgress(t *testing.T) {
	backend := &scriptBackend{
		parse: func(text string) Action {
			if text == "bad tool request" {
				return Action{CorrectionCode: "malformed_tool_input"}
			}
			return Action{Final: true, Content: `{"schema_version":1,"summary":"done"}`}
		},
		turns: []Turn{{Text: "bad tool request", PromptTokens: 5, CompletionTokens: 2}, {Text: "valid final", PromptTokens: 7, CompletionTokens: 3}},
	}
	hooks := &recordingRetryHooks{attempt: RetryAttempt{VisitID: "visit-1", AttemptID: "attempt-1", Number: 1, Remaining: 10}}
	var diagnostics []ToolDiagnostic
	runner := NewLoopRunner(LoopRunnerConfig{Backend: backend})
	result, err := runner.Run(context.Background(), v2.TaskPacket{TaskID: "task-1"}, AgentRuntime{
		AgentID: "reviewer", Role: "reviewer", RunID: "run-1", TaskID: "task-1", ExecutionKey: "exec-1",
		RetryHooks: hooks, ToolEventSink: func(event ToolDiagnostic) error { diagnostics = append(diagnostics, event); return nil },
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Iterations != 2 || result.RecoveryAttempts != 1 || result.Corrections != 1 {
		t.Fatalf("result did not retain correction metrics: %+v", result)
	}
	if len(hooks.consumed) != 1 || hooks.consumed[0] != "malformed_tool_input" {
		t.Fatalf("recovery debits = %v", hooks.consumed)
	}
	if len(hooks.corrections) != 1 || hooks.corrections[0] != "schema:malformed_tool_input" {
		t.Fatalf("corrections = %v", hooks.corrections)
	}
	if len(hooks.progress) != 1 || len(diagnostics) != 0 {
		t.Fatalf("final progress=%v diagnostics=%v", hooks.progress, diagnostics)
	}
}

func TestLoopRunner_CorrectsOnlyProvenNoEffectPatchFailure(t *testing.T) {
	backend := &scriptBackend{
		parse: func(text string) Action {
			switch text {
			case "invalid patch":
				return Action{Tool: "apply_patch_proposal", Input: map[string]any{"patch": "bad", "base_sha": "base"}}
			case "correct patch":
				return Action{Tool: "apply_patch_proposal", Input: map[string]any{"patch": "diff", "base_sha": "base"}}
			default:
				return Action{Final: true, Content: `{"schema_version":1,"summary":"proposal ready"}`}
			}
		},
		turns:    []Turn{{Text: "invalid patch"}, {Text: "correct patch"}, {Text: "final"}},
		toolErrs: map[string]error{},
	}
	// The first adapter result is a typed proof that patch validation stopped
	// before approval persistence. It must not expose its source error.
	toolCall := 0
	hooks := &recordingRetryHooks{attempt: RetryAttempt{VisitID: "visit-patch", AttemptID: "attempt-1", Number: 1, Remaining: 10}}
	var diagnostics []ToolDiagnostic
	// Replace the bound tool behavior through a wrapper backend that preserves
	// the normal scripted provider/parser and returns one correctable failure.
	wrapped := &correctablePatchBackend{inner: backend, calls: &toolCall}
	runner := NewLoopRunner(LoopRunnerConfig{Backend: wrapped})
	result, err := runner.Run(context.Background(), v2.TaskPacket{TaskID: "patch-task", FinalizationPrerequisiteTool: "apply_patch_proposal"}, AgentRuntime{
		AgentID: "developer", Role: "developer", RunID: "run-patch", TaskID: "patch-task", ExecutionKey: "exec-patch",
		RetryHooks:    hooks,
		ToolEventSink: func(d ToolDiagnostic) error { diagnostics = append(diagnostics, d); return nil },
	})
	if err != nil || !strings.Contains(result.Summary, "proposal ready") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if toolCall != 2 || len(hooks.corrections) != 1 || hooks.corrections[0] != "tool:patch_validation_failed" || len(hooks.consumed) != 1 {
		t.Fatalf("tool calls=%d corrections=%v consumed=%v", toolCall, hooks.corrections, hooks.consumed)
	}
	if len(hooks.confirmedNoEffects) != 1 || hooks.confirmedNoEffects[0] != diagnostics[0].MutationID {
		t.Fatalf("confirmed=%v first diagnostic=%#v", hooks.confirmedNoEffects, diagnostics[0])
	}
	if len(hooks.feedback) != 1 || !strings.Contains(hooks.feedback[0], "complete unified diff") {
		t.Fatalf("durable correction feedback=%v", hooks.feedback)
	}
	if len(diagnostics) != 4 || diagnostics[0].Status != "started" || diagnostics[1].Status != "failed" || !diagnostics[1].NoEffectsProven || diagnostics[1].ErrorCode != "patch_validation_failed" {
		t.Fatalf("diagnostics=%#v", diagnostics)
	}
	joined := strings.Join(messagesText(backend.messages), "\n")
	for _, want := range []string{"complete unified diff", "base_sha", "allowed write paths", "failed validation"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("correction feedback missing %q", want)
		}
	}
	for _, forbidden := range []string{"git apply", "filesystem", "raw error", "bad"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("correction feedback contains non-allowlisted detail %q", forbidden)
		}
	}
}

type correctablePatchBackend struct {
	inner *scriptBackend
	calls *int
}

func (b *correctablePatchBackend) Bind(runtime AgentRuntime) (LLMFunc, Parser, ToolExec, error) {
	llm, parse, exec, err := b.inner.Bind(runtime)
	if err != nil {
		return nil, nil, nil, err
	}
	return llm, parse, func(ctx context.Context, tool string, input map[string]any) (string, error) {
		*b.calls = *b.calls + 1
		if *b.calls == 1 {
			return "", &CorrectableToolError{Code: "patch_validation_failed"}
		}
		return exec(ctx, tool, input)
	}, nil
}

func messagesText(groups [][]v2.Message) []string {
	var out []string
	for _, group := range groups {
		for _, message := range group {
			out = append(out, message.Content)
		}
	}
	return out
}

func TestLoopRunner_ProviderFailureRetainsPartialTurnMetrics(t *testing.T) {
	backend := &scriptBackend{
		parse:      lineParser,
		turns:      []Turn{{PromptTokens: 11, CompletionTokens: 3}},
		turnErrors: []error{retry.NewRetryableErrorAfter(errors.New("provider response contains secret"), 13*time.Second)},
	}
	runner := NewLoopRunner(LoopRunnerConfig{Backend: backend})
	result, err := runner.Run(context.Background(), v2.TaskPacket{TaskID: "task-metrics"}, AgentRuntime{AgentID: "reviewer"})
	if err == nil || !strings.Contains(err.Error(), "provider_turn_failed") {
		t.Fatalf("Run error = %v, want classified provider failure", err)
	}
	var after interface{ RetryAfter() time.Duration }
	if !errors.As(err, &after) || after.RetryAfter() != 13*time.Second || strings.Contains(err.Error(), "secret") {
		t.Fatalf("provider retry delay/privacy was not preserved: %v", err)
	}
	if result.Iterations != 1 || result.PromptTokens != 11 || result.CompletionTokens != 3 {
		t.Fatalf("partial provider metrics were lost: %+v", result)
	}
}

func TestLoopRunner_RetryWaitHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend := &scriptBackend{
		parse: func(string) Action { return Action{CorrectionCode: "malformed_tool_input"} },
		turns: []Turn{{Text: "bad request", PromptTokens: 3, CompletionTokens: 2}, {Text: "must not run"}},
	}
	hooks := &recordingRetryHooks{
		attempt:   RetryAttempt{VisitID: "visit-cancel", AttemptID: "attempt-1", Number: 1, Remaining: 10},
		nextAt:    time.Now().Add(time.Hour),
		onConsume: cancel,
	}
	runner := NewLoopRunner(LoopRunnerConfig{Backend: backend})
	result, err := runner.Run(ctx, v2.TaskPacket{TaskID: "task-cancel"}, AgentRuntime{AgentID: "reviewer", RetryHooks: hooks})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error=%v, want context cancellation", err)
	}
	if result.Iterations != 1 || result.PromptTokens != 3 || result.CompletionTokens != 2 || result.RecoveryAttempts != 1 {
		t.Fatalf("cancellation lost partial metrics: %+v", result)
	}
}

func TestLoopRunner_ToolDiagnosticCheckpointFailurePreventsTool(t *testing.T) {
	backend := &scriptBackend{
		parse: func(string) Action {
			return Action{Tool: "write_file", Input: map[string]any{"path": "private.txt", "content": "secret"}}
		},
		turns:      []Turn{{Text: "write file", PromptTokens: 4, CompletionTokens: 2}, {Text: "must not run"}},
		toolResult: "written",
	}
	runner := NewLoopRunner(LoopRunnerConfig{Backend: backend})
	var intent ToolDiagnostic
	result, err := runner.Run(context.Background(), v2.TaskPacket{TaskID: "task-event-fail"}, AgentRuntime{
		AgentID: "coder", RetryHooks: &recordingRetryHooks{attempt: RetryAttempt{AttemptID: "attempt-1", Number: 1, Remaining: 10}},
		ToolEventSink: func(diagnostic ToolDiagnostic) error {
			intent = diagnostic
			return errors.New("private sqlite path failure")
		},
	})
	var safe *SafeFailureError
	if !errors.As(err, &safe) || safe.Code != "tool_diagnostic_checkpoint_failed" || strings.Contains(err.Error(), "private sqlite") {
		t.Fatalf("error=%v, want safe diagnostic checkpoint failure", err)
	}
	if result.Iterations != 1 || result.ToolCalls != 1 || result.PromptTokens != 4 || result.CompletionTokens != 2 {
		t.Fatalf("checkpoint failure lost tool/usage metrics: %+v", result)
	}
	if len(backend.messages) != 1 {
		t.Fatalf("provider calls=%d, want stop after failed durable intent checkpoint", len(backend.messages))
	}
	if len(backend.toolCalls) != 0 {
		t.Fatalf("tool calls=%v, want no tool execution after failed durable intent checkpoint", backend.toolCalls)
	}
	if intent.Status != "started" || !intent.MutationPossible || intent.MutationID == "" {
		t.Fatalf("missing fail-closed mutation intent metadata: %+v", intent)
	}
}

func TestLoopRunner_ToolOutcomeCheckpointFailureStopsFurtherWork(t *testing.T) {
	backend := &scriptBackend{
		parse: func(string) Action {
			return Action{Tool: "write_file", Input: map[string]any{"path": "private.txt", "content": "secret"}}
		},
		turns:      []Turn{{Text: "write file", PromptTokens: 4, CompletionTokens: 2}, {Text: "must not run"}},
		toolResult: "written",
	}
	sinkCalls := 0
	var started, completed ToolDiagnostic
	runner := NewLoopRunner(LoopRunnerConfig{Backend: backend})
	result, err := runner.Run(context.Background(), v2.TaskPacket{TaskID: "task-event-outcome-fail"}, AgentRuntime{
		AgentID: "coder", RetryHooks: &recordingRetryHooks{attempt: RetryAttempt{AttemptID: "attempt-1", Number: 1, Remaining: 10}},
		ToolEventSink: func(diagnostic ToolDiagnostic) error {
			sinkCalls++
			if diagnostic.Status == "started" {
				started = diagnostic
			}
			if diagnostic.Status == "completed" {
				completed = diagnostic
			}
			if sinkCalls == 2 && diagnostic.Status == "completed" {
				return errors.New("private sqlite path failure")
			}
			return nil
		},
	})
	var safe *SafeFailureError
	if !errors.As(err, &safe) || safe.Code != "tool_diagnostic_checkpoint_failed" || strings.Contains(err.Error(), "private sqlite") {
		t.Fatalf("error=%v, want safe diagnostic checkpoint failure", err)
	}
	if len(backend.toolCalls) != 1 || len(backend.messages) != 1 || result.Iterations != 1 || result.ToolCalls != 1 {
		t.Fatalf("post-tool checkpoint failure did not stop cleanly: calls=%v provider=%d result=%+v", backend.toolCalls, len(backend.messages), result)
	}
	if started.Status != "started" || !started.MutationPossible || completed.Status != "completed" || !completed.MutationPossible || started.MutationID == "" || started.MutationID != completed.MutationID {
		t.Fatalf("started/outcome mutation identity is not stable: %+v / %+v", started, completed)
	}
}

func TestLoopRunner_NewObservationResetsConsecutiveNoProgressStreak(t *testing.T) {
	backend := &scriptBackend{
		parse: func(text string) Action {
			switch text {
			case "read-a1", "read-a2", "read-a4":
				return Action{Tool: "read_file", Input: map[string]any{"path": "a.txt"}}
			case "read-b":
				return Action{Tool: "read_file", Input: map[string]any{"path": "b.txt"}}
			default:
				return Action{Final: true, Content: "done"}
			}
		},
		turns:      []Turn{{Text: "read-a1"}, {Text: "read-a2"}, {Text: "read-b"}, {Text: "read-a4"}, {Text: "final"}},
		toolResult: "same result",
	}
	hooks := &recordingRetryHooks{attempt: RetryAttempt{VisitID: "visit-streak", AttemptID: "attempt-1", Number: 1, Remaining: 10}}
	runner := NewLoopRunner(LoopRunnerConfig{Backend: backend})
	result, err := runner.Run(context.Background(), v2.TaskPacket{TaskID: "task-streak"}, AgentRuntime{
		AgentID: "reviewer", RetryHooks: hooks,
		ToolEventSink: func(event ToolDiagnostic) error {
			if event.NoProgressEligible && event.ObservationHash != "" {
				if event.ObservationHash == hooks.lastObservationHash {
					hooks.noProgressCount++
				} else {
					hooks.lastObservationHash, hooks.noProgressCount = event.ObservationHash, 1
				}
			}
			return nil
		},
	})
	if err != nil || result.Summary != "done" {
		t.Fatalf("Run result=%+v error=%v", result, err)
	}
	if result.RecoveryAttempts != 0 || hooks.noProgressCount != 1 {
		t.Fatalf("new observation failed to reset consecutive streak: result=%+v count=%d", result, hooks.noProgressCount)
	}
}

func TestLoopRunner_RepeatedToolObservationStopsAtSixWithSafeCorrelatedDiagnostics(t *testing.T) {
	turns := make([]Turn, 6)
	for i := range turns {
		turns[i] = Turn{Text: "same read"}
	}
	backend := &scriptBackend{
		parse:      func(string) Action { return Action{Tool: "read_file", Input: map[string]any{"path": "private.txt"}} },
		turns:      turns,
		toolResult: "secret file contents",
	}
	hooks := &recordingRetryHooks{attempt: RetryAttempt{VisitID: "visit-2", AttemptID: "attempt-1", Number: 1, Remaining: 10}}
	var diagnostics []ToolDiagnostic
	runner := NewLoopRunner(LoopRunnerConfig{Backend: backend})
	result, err := runner.Run(context.Background(), v2.TaskPacket{TaskID: "task-2"}, AgentRuntime{
		AgentID: "coder", Role: "developer", RunID: "run-2", TaskID: "task-2", CorrelationID: "corr-2", ExecutionKey: "exec-2",
		RetryHooks: hooks, ToolEventSink: func(event ToolDiagnostic) error {
			diagnostics = append(diagnostics, event)
			if event.NoProgressEligible && event.ObservationHash != "" {
				if event.ObservationHash == hooks.lastObservationHash {
					hooks.noProgressCount++
				} else {
					hooks.lastObservationHash, hooks.noProgressCount = event.ObservationHash, 1
				}
			}
			return nil
		},
	})
	var noProgress *NoProgressError
	if !errors.As(err, &noProgress) || noProgress.RepeatedObservations != 6 {
		t.Fatalf("error = %v, want typed six-observation no-progress failure", err)
	}
	if result.Iterations != 6 || result.ToolCalls != 6 || result.RecoveryAttempts != 3 || result.Corrections != 3 {
		t.Fatalf("failure metrics were lost: %+v", result)
	}
	if len(hooks.progress) != 1 {
		t.Fatalf("unchanged evidence should be recorded once, got %d progress records", len(hooks.progress))
	}
	if len(diagnostics) != 12 {
		t.Fatalf("diagnostics = %d, want started and completed per tool call", len(diagnostics))
	}
	for i := 0; i < len(diagnostics); i += 2 {
		started, completed := diagnostics[i], diagnostics[i+1]
		if started.Type != "prizm.tool.started" || started.Status != "started" || started.MutationPossible ||
			completed.Type != "prizm.tool.completed" || completed.Status != "completed" || completed.MutationPossible {
			t.Errorf("diagnostic pair %d has incorrect phase/safety: %+v / %+v", i/2, started, completed)
		}
		if started.RunID != "run-2" || started.TaskID != "task-2" || started.CorrelationID != "corr-2" || started.AttemptID == "" || started.Sequence != i/2+1 || started.MutationID == "" || started.MutationID != completed.MutationID {
			t.Errorf("diagnostic pair %d lacks safe correlation: %+v / %+v", i/2, started, completed)
		}
		if completed.Tool != "read_file" || completed.ObservationHash == "" || completed.SafeRef != "tool:read_file" {
			t.Errorf("diagnostic pair %d lacks bounded outcome metadata: %+v", i/2, completed)
		}
		encoded := fmt.Sprintf("%+v %+v", started, completed)
		if containsAny(encoded, "secret file contents", "private.txt") {
			t.Errorf("diagnostic pair %d included prompt/result/file path data: %s", i/2, encoded)
		}
	}
}

func containsAny(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}
