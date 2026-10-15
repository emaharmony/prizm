package multiagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/retry"
	"github.com/emaharmony/prizm/internal/subagent"
)

func TestDurableRoleRetrySurvivesRestartAndSharedBudget(t *testing.T) {
	env := newDurableTestEnvironment(t)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner:   {{err: retry.NewRetryableError(errors.New("503 unavailable"))}, {result: scriptedResult(OutcomePlanReady)}},
		RoleDeveloper: {{result: scriptedResult(OutcomeImplementationReady)}},
		RoleTester:    {{result: scriptedResult(OutcomeTestsPassed)}},
		RoleReviewer:  {{result: scriptedResult(OutcomeReviewApproved)}},
	}}
	makeRuntime := func() *DurableRuntime {
		runtime, err := NewDurableRuntime(mustAdaptGraph(t, validDefinition()), runner, env.store, env.claimer, env.events,
			DurableRuntimeOptions{Clock: func() time.Time { return now }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatalf("new runtime: %v", err)
		}
		return runtime
	}
	request := testRunRequest()
	request.RunID = "run-durable-retry-restart"
	state, err := makeRuntime().Run(context.Background(), request)
	var waitingErr *RunWaitingError
	if !errors.As(err, &waitingErr) {
		t.Fatalf("Run() error=%v, want scheduled retry wait", err)
	}
	if state.Status != RunStatusPaused {
		t.Fatalf("status=%s, want paused", state.Status)
	}
	stored, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Waiting == nil || stored.Waiting.Kind != "role_retry" || stored.Retry == nil || stored.Retry.RecoveryNumber != 1 || stored.Retry.Number != 2 {
		t.Fatalf("retry checkpoint=%#v", stored)
	}
	candidates, err := makeRuntime().ListRecoveryCandidates(context.Background(), now, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Ready || candidates[0].Kind != "role_retry" || candidates[0].DueAt != stored.Retry.NextAt {
		t.Fatalf("not-due recovery candidate=%#v", candidates)
	}
	now = stored.Retry.NextAt.Add(time.Millisecond)
	candidates, err = makeRuntime().ListRecoveryCandidates(context.Background(), now, 20)
	if err != nil || len(candidates) != 1 || !candidates[0].Ready {
		t.Fatalf("due candidates=%#v err=%v", candidates, err)
	}
	completed, err := makeRuntime().Resume(context.Background(), request.RunID)
	if err != nil {
		t.Fatalf("Resume() after restart: %v", err)
	}
	if completed.Status != RunStatusCompleted || completed.RoleStates[RolePlanner].Visits != 1 || completed.RoleStates[RolePlanner].Retries != 1 || completed.BudgetUsage.Retries != 1 {
		t.Fatalf("completed state lost role retry accounting: %#v", completed)
	}
	if got := runnerCalls(runner); len(got) != 5 || got[0] != RolePlanner || got[1] != RolePlanner {
		t.Fatalf("role executions=%v", got)
	}
}

func TestAcceptIsIdempotentAndRejectsIdentityConflict(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runtime := newDurableRuntimeForTest(t, validDefinition(), happyDurableRunner(), env.store, env.claimer, env.events)
	request := testRunRequest()
	request.RunID = "run-accept-idempotent"
	request.Identity = &RequestIdentity{RequesterID: "api", SessionID: "session-a", Source: "api", ResultDestination: "dashboard", IdempotencyKey: "key-a"}
	first, err := runtime.Accept(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != RunStatusCreated || first.RequestIdentity == nil || first.RequestIdentity.AcceptedAt.IsZero() {
		t.Fatalf("accepted state=%#v", first)
	}
	second, err := runtime.Accept(context.Background(), request)
	if err != nil || second.RunID != first.RunID || second.CreatedAt != first.CreatedAt {
		t.Fatalf("duplicate accepted=%#v err=%v", second, err)
	}
	request.Identity.SessionID = "session-b"
	if _, err := runtime.Accept(context.Background(), request); !errors.Is(err, ErrRequestIdentityConflict) {
		t.Fatalf("conflicting identity error=%v", err)
	}
}

func TestRetryClassificationAndPolicyDefaults(t *testing.T) {
	if class, _, _, ok := classifyRetryableRoleFailure(context.Canceled); ok || class != "" {
		t.Fatal("cancellation is retryable")
	}
	if _, _, _, ok := classifyRetryableRoleFailure(&GovernanceError{Kind: "policy", Reason: "denied"}); ok {
		t.Fatal("governance denial is retryable")
	}
	if _, _, _, ok := classifyRetryableRoleFailure(retry.NewNonRetryableError(errors.New("401 unauthorized"))); ok {
		t.Fatal("auth failure is retryable")
	}
	if class, _, _, ok := classifyRetryableRoleFailure(&StructuredOutputError{Role: RolePlanner, Cause: errors.New("bad shape")}); !ok || class != "schema_correction" {
		t.Fatal("structured correction was not classified")
	}
	if class, code, _, ok := classifyRetryableRoleFailure(&subagent.SafeFailureError{Code: "invalid_proposal_diff"}); !ok || class != "schema_correction" || code != "invalid_proposal_diff" {
		t.Fatalf("typed malformed diff was not recoverable: class=%q code=%q ok=%t", class, code, ok)
	}
	if _, _, _, ok := classifyRetryableRoleFailure(&subagent.SafeFailureError{Code: "invalid_proposal_context"}); ok {
		t.Fatal("workspace or source-context violation must remain nonretryable")
	}
	if got := roleRetryLimit(CompiledNode{}); got != 10 {
		t.Fatalf("default shared retry budget=%d", got)
	}
	defaultGraph := mustAdaptGraph(t, DefaultReferenceDefinition())
	defaultPlanner, _ := defaultGraph.Node(nodeID(RolePlanner))
	if got := roleRetryLimit(defaultPlanner); got != 10 || defaultGraph.Budgets().MaxRetries != Unlimited {
		t.Fatalf("reference defaults role=%d aggregate=%d, want per-role 10 and unlimited aggregate", got, defaultGraph.Budgets().MaxRetries)
	}
	if got := roleRetryLimit(CompiledNode{Retry: &SchemaRetryPolicy{MaxRetries: 2}}); got != 2 {
		t.Fatalf("explicit retry cap=%d", got)
	}
	if _, _, _, ok := classifyRetryableRoleFailure(&net.DNSError{Err: "timeout", IsTimeout: true}); !ok {
		t.Fatal("network reset should retry")
	}
	if _, _, _, ok := classifyRetryableRoleFailure(&net.DNSError{Err: "temporary DNS failure", IsTemporary: true}); !ok {
		t.Fatal("temporary DNS error should retry")
	}
	if _, _, _, ok := classifyRetryableRoleFailure(errors.New("provider said 503 in its output")); ok {
		t.Fatal("arbitrary output text must not be classified transient")
	}
	if err := event.Validate(event.NewEvent(event.EventMultiAgentRetryScheduled, durableEventSource, map[string]any{
		"run_id": "r", "workflow_id": "w", "role": "planner", "execution_key": "e", "visit_id": "v", "attempt_id": "a", "attempt": 2, "recovery_number": 1, "failure_class": "provider_transient", "failure_code": "503", "next_at": time.Now().UTC().Format(time.RFC3339Nano),
	})); err != nil {
		t.Fatalf("retry event contract: %v", err)
	}
}

func TestNonRetryableRoleFailureRetainsOnlyAllowlistedTypedCodes(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantClass string
		wantCode  string
	}{
		{name: "safe execution code", err: &subagent.SafeFailureError{Code: "progress_checkpoint_failed"}, wantClass: "execution", wantCode: "progress_checkpoint_failed"},
		{name: "safe tool input code", err: &subagent.SafeFailureError{Code: "invalid_input"}, wantClass: "tool", wantCode: "invalid_input"},
		{name: "safe tool lookup code", err: &subagent.SafeFailureError{Code: "not_found"}, wantClass: "tool", wantCode: "not_found"},
		{name: "safe tool permission code", err: &subagent.SafeFailureError{Code: "permission_denied"}, wantClass: "tool", wantCode: "permission_denied"},
		{name: "safe patch validation code", err: &subagent.SafeFailureError{Code: "patch_validation_failed"}, wantClass: "tool", wantCode: "patch_validation_failed"},
		{name: "safe tool input code", err: &subagent.SafeFailureError{Code: "invalid_input"}, wantClass: "tool", wantCode: "invalid_input"},
		{name: "safe tool lookup code", err: &subagent.SafeFailureError{Code: "not_found"}, wantClass: "tool", wantCode: "not_found"},
		{name: "safe tool permission code", err: &subagent.SafeFailureError{Code: "permission_denied"}, wantClass: "tool", wantCode: "permission_denied"},
		{name: "safe patch validation code", err: &subagent.SafeFailureError{Code: "patch_validation_failed"}, wantClass: "tool", wantCode: "patch_validation_failed"},
		{name: "safe governance code", err: &subagent.SafeFailureError{Code: "invalid_proposal_context"}, wantClass: "governance", wantCode: "invalid_proposal_context"},
		{name: "governance kind", err: &GovernanceError{Kind: "workspace_scope", Reason: "PRIVATE_REASON_CANARY"}, wantClass: "governance", wantCode: "workspace_scope"},
		{name: "cancellation", err: context.Canceled, wantClass: "cancellation", wantCode: "cancelled"},
		{name: "unknown typed code", err: &subagent.SafeFailureError{Code: "PRIVATE_UNKNOWN_CANARY"}, wantClass: "unknown", wantCode: "unknown"},
		{name: "raw error", err: errors.New("PRIVATE_RAW_ERROR_CANARY"), wantClass: "unknown", wantCode: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failure := ClassifyRoleExecutionFailure(test.err)
			if failure.Retryable || failure.Class != test.wantClass || failure.Code != test.wantCode {
				t.Fatalf("failure=%#v, want nonretryable %s/%s", failure, test.wantClass, test.wantCode)
			}
			encoded, err := json.Marshal(failure)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "PRIVATE_") {
				t.Fatalf("failure payload leaked private detail: %s", encoded)
			}
		})
	}
}

func TestDurableRetryBackoffIsBoundedDeterministicAndHonorsRetryAfter(t *testing.T) {
	first := roleRetryDelay("visit", 1, 0)
	if first < 5*time.Second || first > 5*time.Second+500*time.Millisecond {
		t.Fatalf("first delay=%v", first)
	}
	if got := roleRetryDelay("visit", 1, 0); got != first {
		t.Fatalf("jitter is not deterministic: %v != %v", got, first)
	}
	last := roleRetryDelay("visit", 10, 0)
	if last < 60*time.Second || last > 60*time.Second+500*time.Millisecond {
		t.Fatalf("capped delay=%v", last)
	}
	if got := roleRetryDelay("visit", 1, first+time.Second); got != first+time.Second {
		t.Fatalf("Retry-After not honored: %v", got)
	}
}

func TestDurableRetryExhaustionIsInitialPlusTenAndTerminalIsStable(t *testing.T) {
	env := newDurableTestEnvironment(t)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	steps := make([]scriptedStep, 11)
	for i := range steps {
		steps[i] = scriptedStep{result: RoleRunResult{LocalIterations: 1, TokenUsage: cost.TokenUsage{PromptTokens: 1, TotalTokens: 1}}, err: retry.NewRetryableError(errors.New("503 unavailable"))}
	}
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{RolePlanner: steps}}
	runtime := newMutableClockRuntime(t, validDefinition(), runner, env, &now)
	request := testRunRequest()
	request.RunID = "run-retry-budget-exhaustion"
	_, err := runtime.Run(context.Background(), request)
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) {
		t.Fatalf("initial retry schedule err=%v", err)
	}
	for recovery := 1; recovery <= 10; recovery++ {
		record, loadErr := env.store.Load(context.Background(), request.RunID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		now = record.Retry.NextAt.Add(time.Millisecond)
		state, resumeErr := runtime.Resume(context.Background(), request.RunID)
		if recovery < 10 {
			if !errors.As(resumeErr, &waiting) || state.Status != RunStatusPaused {
				t.Fatalf("recovery %d state=%s err=%v", recovery, state.Status, resumeErr)
			}
		} else if state.Status != RunStatusFailed || resumeErr == nil {
			t.Fatalf("exhaustion state=%s err=%v", state.Status, resumeErr)
		}
	}
	record, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.RetryHistory) != 1 || record.RetryHistory[0].RecoveryNumber != 10 || record.RetryHistory[0].Number != 11 {
		t.Fatalf("persisted retry history=%#v", record.RetryHistory)
	}
	if record.Failure == nil || record.Failure.Message != "retry_exhausted" || record.State.RoleStates[RolePlanner].LastError != "retry_exhausted" {
		t.Fatalf("terminal retry reason=%#v role=%#v", record.Failure, record.State.RoleStates[RolePlanner])
	}
	role := record.State.RoleStates[RolePlanner]
	if role.LocalIterations != 11 || role.Retries != 10 || record.State.BudgetUsage.Retries != 10 {
		t.Fatalf("retry metrics role=%#v budget=%#v", role, record.State.BudgetUsage)
	}
	before := runnerCalls(runner)
	if _, err := runtime.Resume(context.Background(), request.RunID); err != nil {
		t.Fatalf("terminal Resume: %v", err)
	}
	if len(runnerCalls(runner)) != len(before) {
		t.Fatal("terminal recovery reran the role")
	}
	events := queryEventsForTest(t, env.events, request.RunID, event.EventMultiAgentRunFailed)
	if len(events) != 1 {
		t.Fatalf("terminal failure event count=%d", len(events))
	}
	failed := events[0].Payload
	if failed["failure_reason"] != "retry_exhausted" || failed["retry_exhausted"] != true || failed["retryable"] != true || fmt.Sprint(failed["max_recoveries"]) != "10" {
		t.Fatalf("terminal exhaustion classification=%#v", failed)
	}
	if failed["attempt_id"] == "" || fmt.Sprint(failed["attempt"]) != "11" || fmt.Sprint(failed["recovery_number"]) != "10" {
		t.Fatalf("terminal attempt identity=%#v", failed)
	}
}

func TestDurableRetryTerminalNonretryableFailureRetainsAttemptClassification(t *testing.T) {
	env := newDurableTestEnvironment(t)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner: {
			{result: RoleRunResult{LocalIterations: 1}, err: retry.NewRetryableError(errors.New("transient transport failure"))},
			{result: RoleRunResult{LocalIterations: 1}, err: &subagent.SafeFailureError{Code: "invalid_input", Tool: "read_file"}},
		},
	}}
	runtime := newMutableClockRuntime(t, validDefinition(), runner, env, &now)
	request := testRunRequest()
	request.RunID = "run-retry-nonretryable-terminal"
	_, err := runtime.Run(context.Background(), request)
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) {
		t.Fatalf("first attempt err=%v, want retry wait", err)
	}
	record, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Retry == nil || record.Retry.MaxRecoveries != 10 || record.Retry.RecoveryNumber != 1 {
		t.Fatalf("first recovery policy=%#v, want one of ten", record.Retry)
	}
	now = record.Retry.NextAt.Add(time.Millisecond)
	state, err := runtime.Resume(context.Background(), request.RunID)
	if err == nil || state.Status != RunStatusFailed {
		t.Fatalf("terminal state=%s err=%v", state.Status, err)
	}
	record, err = env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Failure == nil || record.Failure.FailureReason != "nonretryable" || record.Failure.FailureClass != "nonretryable" || record.Failure.FailureCode != "invalid_input" || record.Failure.Retryable || record.Failure.RetryExhausted {
		t.Fatalf("persisted failure classification=%#v", record.Failure)
	}
	if record.Failure.Attempt != 2 || record.Failure.RecoveryNumber != 1 || record.Failure.MaxRecoveries != 10 || record.Failure.AttemptID == "" {
		t.Fatalf("persisted failure attempt=%#v", record.Failure)
	}
	started := queryEventsForTest(t, env.events, request.RunID, event.EventMultiAgentRetryAttemptStarted)
	failed := queryEventsForTest(t, env.events, request.RunID, event.EventMultiAgentRunFailed)
	if len(started) != 1 || len(failed) != 1 {
		t.Fatalf("retry started=%d terminal failures=%d", len(started), len(failed))
	}
	if started[0].Payload["attempt_id"] != record.Failure.AttemptID || fmt.Sprint(started[0].Payload["attempt"]) != "2" {
		t.Fatalf("retry start identity=%#v terminal=%#v", started[0].Payload, record.Failure)
	}
	if failed[0].Payload["failure_reason"] != "nonretryable" || failed[0].Payload["failure_code"] != "invalid_input" || failed[0].Payload["retryable"] != false || failed[0].Payload["retry_exhausted"] != false {
		t.Fatalf("terminal event classification=%#v", failed[0].Payload)
	}
	if strings.Contains(failed[0].Payload["reason"].(string), "transient transport") {
		t.Fatal("terminal event exposed original provider error text")
	}
}

func TestExplicitRoleDeadlineBeatsLongRetryAfter(t *testing.T) {
	env := newDurableTestEnvironment(t)
	definition := validDefinition()
	definition.Roles[0].TimeBudget = time.Second
	if roleConfig, ok := mustAdaptGraph(t, definition).RoleConfig(RolePlanner); !ok || roleConfig.TimeBudget != time.Second {
		t.Fatalf("planner time budget=%v", roleConfig.TimeBudget)
	}
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner: {{result: RoleRunResult{LocalIterations: 1}, err: retry.NewRetryableErrorAfter(errors.New("429 limited"), time.Minute)}},
	}}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	runtime := newMutableClockRuntime(t, definition, runner, env, &now)
	request := testRunRequest()
	request.RunID = "run-deadline-before-retry-after"
	state, err := runtime.Run(context.Background(), request)
	if err == nil || state.Status != RunStatusFailed {
		t.Fatalf("deadline state=%s err=%v", state.Status, err)
	}
	record, loadErr := env.store.Load(context.Background(), request.RunID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if record.Waiting != nil || len(record.RetryHistory) != 1 || record.RetryHistory[0].FailureCode != "retry_deadline_exceeded" || record.RetryHistory[0].RecoveryNumber != 0 {
		t.Fatalf("deadline retry was scheduled instead of failing safely: %#v", record)
	}
}

func TestSchemaCorrectionRetryAndStaleCallbackUseSharedBudget(t *testing.T) {
	env := newDurableTestEnvironment(t)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	base := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner:   {{result: scriptedResult(OutcomePlanReady)}},
		RoleDeveloper: {{result: scriptedResult(OutcomeImplementationReady)}},
		RoleTester:    {{result: scriptedResult(OutcomeTestsPassed)}},
		RoleReviewer:  {{result: scriptedResult(OutcomeReviewApproved)}},
	}}
	schemaErr := structuredError(RolePlanner, errors.New("understanding is required"))
	var schemaCorrection *SchemaCorrectionError
	if !errors.As(schemaErr, &schemaCorrection) {
		t.Fatalf("structured error type=%T", schemaErr)
	}
	feedback := schemaCorrection.Feedback
	runner := &consumeThenFailRunner{inner: base, err: schemaErr}
	runtime := newMutableClockRuntime(t, validDefinition(), runner, env, &now)
	request := testRunRequest()
	request.RunID = "run-shared-recovery-budget"
	state, err := runtime.Run(context.Background(), request)
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) || state.Status != RunStatusPaused {
		t.Fatalf("retry schedule state=%s err=%v", state.Status, err)
	}
	stored, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Retry.RecoveryNumber != 2 || stored.Retry.Number != 3 || runner.staleErr == nil || !errors.Is(runner.staleErr, ErrStaleRetryAttempt) {
		t.Fatalf("shared retry state=%#v stale callback=%v", stored.Retry, runner.staleErr)
	}
	if stored.Retry.RecoveryFeedback != feedback || strings.Contains(stored.Retry.RecoveryFeedback, "bad shape") {
		t.Fatalf("safe schema feedback was not persisted: %q", stored.Retry.RecoveryFeedback)
	}
	prompt, err := BuildRolePrompt(RoleRunRequest{Run: RunView{CurrentRole: RolePlanner}, RecoveryFeedback: stored.Retry.RecoveryFeedback})
	if err != nil || !strings.Contains(prompt, "RECOVERY CORRECTION: "+feedback) || strings.Contains(prompt, "bad shape") {
		t.Fatalf("recovery prompt did not include only actionable safe feedback: %q err=%v", prompt, err)
	}
	now = stored.Retry.NextAt.Add(time.Millisecond)
	completed, err := runtime.Resume(context.Background(), request.RunID)
	if err != nil || completed.Status != RunStatusCompleted {
		t.Fatalf("schema correction recovery state=%s err=%v", completed.Status, err)
	}
	if runner.recoveryFeedback != feedback {
		t.Fatalf("recovery prompt feedback=%q want %q", runner.recoveryFeedback, feedback)
	}
	if completed.RoleStates[RolePlanner].Retries != 2 || len(stored.Retry.Corrections) != 2 || stored.Retry.FailureClass != "schema_correction" {
		t.Fatalf("shared allowance/correction was not preserved: state=%#v", stored.Retry)
	}
	final, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.RetryHistory) != 1 || final.RetryHistory[0].RecoveryNumber != 2 || final.RetryHistory[0].Corrections[0].Code != "invalid_schema" {
		t.Fatalf("final retry summary=%#v", final.RetryHistory)
	}
}

func TestNoProgressObservationLedgerSurvivesRestartAndResetsOnChange(t *testing.T) {
	env := newDurableTestEnvironment(t)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	base := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner:   {{result: scriptedResult(OutcomePlanReady)}},
		RoleDeveloper: {{result: scriptedResult(OutcomeImplementationReady)}},
		RoleTester:    {{result: scriptedResult(OutcomeTestsPassed)}},
		RoleReviewer:  {{result: scriptedResult(OutcomeReviewApproved)}},
	}}
	runner := &durableObservationRunner{inner: base, firstError: retry.NewRetryableError(errors.New("temporary"))}
	definition := validDefinition()
	first := newMutableClockRuntime(t, definition, runner, env, &now)
	request := testRunRequest()
	request.RunID = "run-no-progress-restart"
	state, err := first.Run(context.Background(), request)
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) || state.Status != RunStatusPaused || runner.counts[0] != 1 {
		t.Fatalf("first visit did not persist its observation count: state=%s count=%v err=%v", state.Status, runner.counts, err)
	}
	record, err := env.store.Load(context.Background(), request.RunID)
	if err != nil || record.Retry == nil || record.Retry.ConsecutiveNoProgress != 1 {
		t.Fatalf("observation ledger was not checkpointed: retry=%#v err=%v", record.Retry, err)
	}
	now = record.Retry.NextAt.Add(time.Millisecond)
	restarted := newMutableClockRuntime(t, definition, runner, env, &now)
	completed, err := restarted.Resume(context.Background(), request.RunID)
	if err != nil || completed.Status != RunStatusCompleted || runner.counts[1] != 2 {
		t.Fatalf("restart reset consecutive count: state=%s counts=%v err=%v", completed.Status, runner.counts, err)
	}
	last, err := env.store.Load(context.Background(), request.RunID)
	if err != nil || len(last.RetryHistory) != 1 || last.RetryHistory[0].ConsecutiveNoProgress != 2 {
		t.Fatalf("final observation summary=%#v err=%v", last.RetryHistory, err)
	}
}

func TestCancellationArchivesPartialAttemptMetricsWithoutRetry(t *testing.T) {
	env := newDurableTestEnvironment(t)
	cancelCtx, cancel := context.WithCancel(context.Background())
	runner := &cancelingPartialRunner{cancel: cancel}
	runtime, err := NewDurableRuntime(mustAdaptGraph(t, validDefinition()), runner, env.store, env.claimer, env.events,
		DurableRuntimeOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	request := testRunRequest()
	request.RunID = "run-cancelled-partial-metrics"
	state, runErr := runtime.Run(cancelCtx, request)
	if !errors.Is(runErr, context.Canceled) || state.Status != RunStatusCancelled {
		t.Fatalf("cancellation state=%s err=%v", state.Status, runErr)
	}
	record, err := env.store.Load(context.Background(), request.RunID)
	if err != nil {
		t.Fatal(err)
	}
	role := record.State.RoleStates[RolePlanner]
	if role.Status != RoleStatusCancelled || role.LocalIterations != 3 || role.TokenUsage.TotalTokens != 7 || record.State.BudgetUsage.TotalLocalIterations != 3 || record.State.BudgetUsage.Tokens.TotalTokens != 7 {
		t.Fatalf("partial metrics lost on cancellation: role=%#v budget=%#v", role, record.State.BudgetUsage)
	}
	if record.Retry != nil || len(record.RetryHistory) != 1 || record.RetryHistory[0].RecoveryNumber != 0 || record.RetryHistory[0].FailureCode != "cancelled" {
		t.Fatalf("cancelled attempt was retried or not archived: active=%#v history=%#v", record.Retry, record.RetryHistory)
	}
}

func TestProvenNoEffectsClearsOnlyMatchingMutationIntent(t *testing.T) {
	for _, priorUnsafe := range []bool{false, true} {
		name := "only action"
		suffix := "only"
		if priorUnsafe {
			name = "prior unsafe action remains"
			suffix = "prior"
		}
		t.Run(name, func(t *testing.T) {
			env := newDurableTestEnvironment(t)
			runner := noEffectsCorrectionRunner{priorUnsafe: priorUnsafe}
			runtime := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events)
			request := testRunRequest()
			request.RunID = "run-proven-no-effects-" + suffix
			state, err := runtime.Run(context.Background(), request)
			stored, loadErr := env.store.Load(context.Background(), request.RunID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if priorUnsafe {
				var waiting *RunWaitingError
				if !errors.As(err, &waiting) || state.Status != RunStatusPaused || stored.Waiting == nil || stored.Waiting.Kind != "execution_reconciliation" || !stored.Retry.MutationPossible {
					t.Fatalf("prior unsafe action was cleared: state=%s waiting=%#v retry=%#v err=%v", state.Status, stored.Waiting, stored.Retry, err)
				}
			} else {
				var waiting *RunWaitingError
				if !errors.As(err, &waiting) || state.Status != RunStatusPaused || stored.Waiting == nil || stored.Waiting.Kind != "role_retry" || stored.Retry.MutationPossible || !stored.Retry.ReplaySafe {
					t.Fatalf("proven no-effect action remained unsafe: state=%s waiting=%#v retry=%#v err=%v", state.Status, stored.Waiting, stored.Retry, err)
				}
			}
		})
	}
}

func TestSuccessfulRoleResultCannotClearAmbiguousFailedMutation(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runner := &ambiguousMutationThenSuccessRunner{}
	runtime := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events)
	request := testRunRequest()
	request.RunID = "run-ambiguous-failed-mutation"
	state, err := runtime.Run(context.Background(), request)
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) || waiting.Kind != "execution_reconciliation" || state.Status != RunStatusPaused {
		t.Fatalf("ambiguous mutation did not pause for reconciliation: state=%s err=%v", state.Status, err)
	}
	record, loadErr := env.store.Load(context.Background(), request.RunID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if record.Phase != CheckpointWaiting || record.Waiting == nil || record.Waiting.Kind != "execution_reconciliation" || record.Retry == nil || !record.Retry.UnresolvedMutationFailure || record.State.RoleStates[RolePlanner].Status == RoleStatusCompleted {
		t.Fatalf("ambiguous mutation was treated as successful: phase=%s waiting=%#v retry=%#v role=%#v", record.Phase, record.Waiting, record.Retry, record.State.RoleStates[RolePlanner])
	}
	if len(runner.calls) != 1 || runner.calls[0] != RolePlanner {
		t.Fatalf("runtime advanced after ambiguous mutation: calls=%v", runner.calls)
	}
}

type proposalThenMalformedFinalRunner struct{ calls int }

func (r *proposalThenMalformedFinalRunner) RunRole(_ context.Context, request RoleRunRequest) (RoleRunResult, error) {
	r.calls++
	if request.Run.CurrentRole != RolePlanner {
		return scriptedResult(OutcomePlanReady), nil
	}
	attempt := request.RetryHandle.CurrentAttempt()
	mutationID := strings.Repeat("d", 64)
	for _, status := range []string{"started", "completed"} {
		if err := request.ToolEventSink(ToolDiagnostic{AttemptID: attempt.AttemptID, Tool: "apply_patch_proposal",
			Status: status, MutationID: mutationID, MutationPossible: true}); err != nil {
			return RoleRunResult{}, err
		}
	}
	// The proposal-like mutation completed and is durable, but the worker's
	// final structured output is malformed. Retrying the worker could create a
	// second proposal because no authoritative receipt identity is available.
	return RoleRunResult{}, &StructuredOutputError{Role: RolePlanner, Cause: errors.New("malformed final output")}
}

func TestSuccessfulProposalLikeMutationThenMalformedFinalFailsClosed(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runner := &proposalThenMalformedFinalRunner{}
	runtime := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events)
	request := testRunRequest()
	request.RunID = "run-proposal-mutation-malformed-final"
	state, err := runtime.Run(context.Background(), request)
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) || waiting.Kind != "execution_reconciliation" || state.Status != RunStatusPaused {
		t.Fatalf("malformed final output replayed after proposal-like mutation: status=%s err=%v", state.Status, err)
	}
	record, err := env.store.Load(context.Background(), request.RunID)
	if err != nil || record.Retry == nil || !record.Retry.MutationPossible || record.Retry.RecoveryNumber != 0 || record.Waiting == nil || record.Waiting.SafeToRetry {
		t.Fatalf("known mutation was not retained for reconciliation: retry=%#v waiting=%#v err=%v", record.Retry, record.Waiting, err)
	}
	if runner.calls != 1 {
		t.Fatalf("worker was retried after an unbound proposal receipt: calls=%d", runner.calls)
	}
	if scheduled := queryEventsForTest(t, env.events, request.RunID, event.EventMultiAgentRetryScheduled); len(scheduled) != 0 {
		t.Fatalf("unsafe proposal created recovery retries: %d", len(scheduled))
	}
	paused := queryEventsForTest(t, env.events, request.RunID, event.EventMultiAgentRunPaused)
	recovery := queryEventsForTest(t, env.events, request.RunID, event.EventMultiAgentRecoveryFailed)
	if len(paused) != 1 || len(recovery) != 1 {
		t.Fatalf("reconciliation notices paused=%d recovery=%d; want one each", len(paused), len(recovery))
	}
}

func TestPersistedPauseStopsDueRetryUntilExplicitResume(t *testing.T) {
	env := newDurableTestEnvironment(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
		RolePlanner:   {{err: retry.NewRetryableError(errors.New("temporary provider failure"))}, {result: scriptedResult(OutcomePlanReady)}},
		RoleDeveloper: {{result: scriptedResult(OutcomeImplementationReady)}},
		RoleTester:    {{result: scriptedResult(OutcomeTestsPassed)}},
		RoleReviewer:  {{result: scriptedResult(OutcomeReviewApproved)}},
	}}
	runtime := newMutableClockRuntime(t, validDefinition(), runner, env, &now)
	request := testRunRequest()
	request.RunID = "run-pause-at-due-retry"
	state, err := runtime.Run(context.Background(), request)
	var waiting *RunWaitingError
	if !errors.As(err, &waiting) || state.Status != RunStatusPaused || waiting.Kind != "role_retry" {
		t.Fatalf("initial retry state=%s err=%v", state.Status, err)
	}
	if _, err := runtime.Pause(context.Background(), request.RunID, "operator requested safe pause"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	state, err = runtime.Resume(context.Background(), request.RunID)
	if !errors.As(err, &waiting) || waiting.Kind != "operator_pause" || state.Status != RunStatusPaused || len(runnerCalls(runner)) != 1 {
		t.Fatalf("due retry ignored persisted pause: state=%s calls=%v err=%v", state.Status, runnerCalls(runner), err)
	}
	state, err = runtime.Resume(context.Background(), request.RunID)
	if err != nil || state.Status != RunStatusCompleted || len(runnerCalls(runner)) != 5 {
		t.Fatalf("explicit resume did not consume preserved retry: state=%s calls=%v err=%v", state.Status, runnerCalls(runner), err)
	}
}

type ambiguousMutationThenSuccessRunner struct{ calls []Role }

func (r *ambiguousMutationThenSuccessRunner) RunRole(_ context.Context, request RoleRunRequest) (RoleRunResult, error) {
	r.calls = append(r.calls, request.Run.CurrentRole)
	if request.Run.CurrentRole != RolePlanner {
		return scriptedResult(OutcomePlanReady), nil
	}
	attempt := request.RetryHandle.CurrentAttempt()
	if err := request.ToolEventSink(ToolDiagnostic{AttemptID: attempt.AttemptID, Tool: "write_file_proposal", Status: "failed", MutationID: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", MutationPossible: true, ErrorCode: "execution_error"}); err != nil {
		return RoleRunResult{}, err
	}
	// The fake tool may have saved its approval before reporting the error.
	// Return a valid later role result to prove the runtime still fails closed.
	return scriptedResult(OutcomePlanReady), nil
}

type noEffectsCorrectionRunner struct{ priorUnsafe bool }

func (r noEffectsCorrectionRunner) RunRole(_ context.Context, request RoleRunRequest) (RoleRunResult, error) {
	if request.Run.CurrentRole != RolePlanner {
		return scriptedResult(OutcomePlanReady), nil
	}
	attempt := request.RetryHandle.CurrentAttempt()
	if r.priorUnsafe {
		if err := request.ToolEventSink(ToolDiagnostic{AttemptID: attempt.AttemptID, Tool: "write_file", Status: "started", MutationID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", MutationPossible: true}); err != nil {
			return RoleRunResult{}, err
		}
	}
	if err := request.ToolEventSink(ToolDiagnostic{AttemptID: attempt.AttemptID, Tool: "apply_patch_proposal", Status: "started", MutationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MutationPossible: true}); err != nil {
		return RoleRunResult{}, err
	}
	if err := request.RetryHandle.ConfirmNoEffects(attempt.AttemptID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		return RoleRunResult{}, errors.New("confirmation without persisted proof was accepted")
	}
	if err := request.ToolEventSink(ToolDiagnostic{AttemptID: attempt.AttemptID, Tool: "apply_patch_proposal", Status: "failed", MutationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MutationPossible: true, NoEffectsProven: true, ErrorCode: "patch_validation_failed"}); err != nil {
		return RoleRunResult{}, err
	}
	if err := request.RetryHandle.ConfirmNoEffects(attempt.AttemptID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		return RoleRunResult{}, err
	}
	return RoleRunResult{}, retry.NewRetryableError(errors.New("provider unavailable"))
}

func TestSafeInterruptedRoleAttemptsRestartWithFreshChargedAttempt(t *testing.T) {
	t.Run("crash before runner call", func(t *testing.T) {
		env := newDurableTestEnvironment(t)
		runner := happyDurableRunner()
		failingStore := &persistThenFailCheckpointStore{DurableRunStore: env.store, failCheckpoint: 2}
		runtime := newDurableRuntimeForTest(t, validDefinition(), runner, failingStore, env.claimer, env.events)
		request := testRunRequest()
		request.RunID = "run-crash-before-role-call"
		_, runErr := runtime.Run(context.Background(), request)
		if !errors.Is(runErr, errInjectedCheckpoint) || len(runnerCalls(runner)) != 0 {
			t.Fatalf("expected persisted role-running crash before invocation; calls=%v err=%v", runnerCalls(runner), runErr)
		}
		before, err := env.store.Load(context.Background(), request.RunID)
		if err != nil || before.Phase != CheckpointRoleRunning || before.Retry == nil || !before.Retry.ReplaySafe {
			t.Fatalf("safe prepared attempt checkpoint=%#v err=%v", before, err)
		}
		resumer := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events)
		state, err := resumer.Resume(context.Background(), request.RunID)
		if err != nil || state.Status != RunStatusCompleted || !reflect.DeepEqual(runnerCalls(runner), Phase1Roles()) {
			t.Fatalf("safe crash recovery state=%s calls=%v err=%v", state.Status, runnerCalls(runner), err)
		}
		after, err := env.store.Load(context.Background(), request.RunID)
		if err != nil || len(after.RetryHistory) == 0 || after.RetryHistory[0].RecoveryNumber != 1 || after.RetryHistory[0].Number != 2 || after.RetryHistory[0].FailureCode != "safe_attempt_interrupted" {
			t.Fatalf("safe crash was not charged with a fresh attempt: history=%#v err=%v", after.RetryHistory, err)
		}
	})

	t.Run("legacy checkpoint without safety marker remains conservative", func(t *testing.T) {
		env := newDurableTestEnvironment(t)
		runner := happyDurableRunner()
		failingStore := &persistThenFailCheckpointStore{DurableRunStore: env.store, failCheckpoint: 2}
		runtime := newDurableRuntimeForTest(t, validDefinition(), runner, failingStore, env.claimer, env.events)
		request := testRunRequest()
		request.RunID = "run-legacy-replay-safety-absent"
		_, runErr := runtime.Run(context.Background(), request)
		if !errors.Is(runErr, errInjectedCheckpoint) {
			t.Fatalf("expected role-running checkpoint interruption, got %v", runErr)
		}
		legacy, err := env.store.Load(context.Background(), request.RunID)
		if err != nil || legacy.Retry == nil || !legacy.Retry.ReplaySafe {
			t.Fatalf("prepared checkpoint=%#v err=%v", legacy.Retry, err)
		}
		legacy.Retry.ReplaySafe = false // matches pre-marker durable JSON (omitempty bool)
		legacy, err = env.store.Checkpoint(context.Background(), legacy.Revision, legacy, nil)
		if err != nil {
			t.Fatal(err)
		}
		resumer := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events)
		state, err := resumer.Resume(context.Background(), request.RunID)
		var waiting *RunWaitingError
		if !errors.As(err, &waiting) || waiting.Kind != "execution_reconciliation" || state.Status != RunStatusPaused || len(runnerCalls(runner)) != 0 {
			t.Fatalf("legacy checkpoint was replayed: state=%s calls=%v err=%v", state.Status, runnerCalls(runner), err)
		}
	})

	t.Run("readonly diagnostic remains replayable", func(t *testing.T) {
		env := newDurableTestEnvironment(t)
		base := happyDurableRunner()
		runner := &readonlyCrashOnceRunner{inner: base}
		runtime := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events)
		request := testRunRequest()
		request.RunID = "run-crash-after-readonly-tool"
		func() {
			defer func() {
				if recovered := recover(); recovered != errSimulatedProcessCrash {
					t.Fatalf("expected simulated process crash, got %v", recovered)
				}
			}()
			_, _ = runtime.Run(context.Background(), request)
		}()
		before, err := env.store.Load(context.Background(), request.RunID)
		if err != nil || before.Phase != CheckpointRoleRunning || before.Retry == nil || !before.Retry.ReplaySafe || before.Retry.MutationPossible {
			t.Fatalf("readonly execution was not replay-safe: retry=%#v err=%v", before.Retry, err)
		}
		resumer := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events)
		state, err := resumer.Resume(context.Background(), request.RunID)
		if err != nil || state.Status != RunStatusCompleted || runner.oldAttemptID == "" || runner.newAttemptID == "" || runner.oldAttemptID == runner.newAttemptID || !errors.Is(runner.staleCallbackErr, ErrStaleRetryAttempt) {
			t.Fatalf("readonly restart did not fence old attempt: state=%s old=%s new=%s stale=%v err=%v", state.Status, runner.oldAttemptID, runner.newAttemptID, runner.staleCallbackErr, err)
		}
	})
}

var errSimulatedProcessCrash = errors.New("simulated process crash")

type persistThenFailCheckpointStore struct {
	DurableRunStore
	checkpoints    int
	failCheckpoint int
	failed         bool
}

func (s *persistThenFailCheckpointStore) Checkpoint(ctx context.Context, expectedRevision int64, record DurableRun, events []event.Event) (DurableRun, error) {
	s.checkpoints++
	saved, err := s.DurableRunStore.Checkpoint(ctx, expectedRevision, record, events)
	if err != nil {
		return saved, err
	}
	if !s.failed && s.checkpoints == s.failCheckpoint {
		s.failed = true
		return saved, errInjectedCheckpoint
	}
	return saved, nil
}

type readonlyCrashOnceRunner struct {
	inner            RoleRunner
	crashed          bool
	oldAttemptID     string
	newAttemptID     string
	staleCallbackErr error
}

func (r *readonlyCrashOnceRunner) RunRole(ctx context.Context, request RoleRunRequest) (RoleRunResult, error) {
	if request.Run.CurrentRole == RolePlanner && !r.crashed {
		r.crashed = true
		attempt := request.RetryHandle.CurrentAttempt()
		r.oldAttemptID = attempt.AttemptID
		if err := request.ToolEventSink(ToolDiagnostic{AttemptID: attempt.AttemptID, Tool: "read_file", Status: "completed", Sequence: 1, ObservationHash: "readonly-result", NoProgressEligible: true}); err != nil {
			return RoleRunResult{}, err
		}
		panic(errSimulatedProcessCrash)
	}
	if request.Run.CurrentRole == RolePlanner {
		attempt := request.RetryHandle.CurrentAttempt()
		r.newAttemptID = attempt.AttemptID
		r.staleCallbackErr = request.RetryHandle.RecordProgress(r.oldAttemptID, "evidence", "read_file", "readonly-result")
	}
	return r.inner.RunRole(ctx, request)
}

type cancelingPartialRunner struct{ cancel context.CancelFunc }

func (r *cancelingPartialRunner) RunRole(context.Context, RoleRunRequest) (RoleRunResult, error) {
	r.cancel()
	return RoleRunResult{LocalIterations: 3, TokenUsage: cost.TokenUsage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7}}, context.Canceled
}

func TestReflectionReplanArchivesRetryAndCannotBypassMutationReconciliation(t *testing.T) {
	definition := baseDef()
	definition.Spec.Nodes = append(definition.Spec.Nodes, roleNode("reflector", "done"))
	definition.Spec.Edges = append(definition.Spec.Edges, edge("reflector-end", "reflector", "end", "done"))
	definition.Spec.Reflection = &SchemaReflectionPolicy{Enabled: true, Role: "reflector", ReplanRole: "start", MaxReplans: 1, Triggers: []string{string(ReflectionFailure)}}
	graph, _, err := Compile(definition, nil, CompileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("safe replan archives visit ledger", func(t *testing.T) {
		env := newDurableTestEnvironment(t)
		calls := 0
		reflection := ReflectionRunnerFunc(func(context.Context, ReflectionRequest) (ReflectionResult, error) {
			calls++
			return ReflectionResult{Verdict: "partial", Confidence: .5, ReplanRequested: calls == 1, ReplanReason: "new plan"}, nil
		})
		runner := &scriptedRunner{scripts: map[Role][]scriptedStep{
			Role("start"): {{err: &subagent.NoProgressError{RepeatedObservations: 6}}, {result: RoleRunResult{Outcome: "ok", LocalIterations: 1}}},
		}}
		runtime, err := NewDurableRuntime(graph, runner, env.store, env.claimer, env.events, DurableRuntimeOptions{Reflection: reflection})
		if err != nil {
			t.Fatal(err)
		}
		state, err := runtime.Run(context.Background(), RunRequest{RunID: "run-reflection-retry-archive", Task: TaskReference{ID: "task", Description: "reflection"}})
		stored, loadErr := env.store.Load(context.Background(), "run-reflection-retry-archive")
		if err != nil || loadErr != nil || state.Status != RunStatusCompleted || calls != 1 || stored.Retry != nil || len(stored.RetryHistory) != 1 || stored.RetryHistory[0].FailureCode != "no_progress" {
			t.Fatalf("replan state=%s calls=%d retry=%#v history=%#v err=%v load=%v", state.Status, calls, stored.Retry, stored.RetryHistory, err, loadErr)
		}
	})
	t.Run("ambiguous mutation pauses before reflection", func(t *testing.T) {
		env := newDurableTestEnvironment(t)
		calls := 0
		reflection := ReflectionRunnerFunc(func(context.Context, ReflectionRequest) (ReflectionResult, error) {
			calls++
			return ReflectionResult{Verdict: "partial", Confidence: .5, ReplanRequested: true, ReplanReason: "unsafe"}, nil
		})
		runner := &mutationThenNoProgressRunner{}
		runtime, err := NewDurableRuntime(graph, runner, env.store, env.claimer, env.events, DurableRuntimeOptions{Reflection: reflection})
		if err != nil {
			t.Fatal(err)
		}
		state, err := runtime.Run(context.Background(), RunRequest{RunID: "run-reflection-ambiguous-mutation", Task: TaskReference{ID: "task", Description: "reconcile"}})
		var waiting *RunWaitingError
		stored, loadErr := env.store.Load(context.Background(), "run-reflection-ambiguous-mutation")
		if !errors.As(err, &waiting) || state.Status != RunStatusPaused || calls != 0 || loadErr != nil || stored.Waiting == nil || stored.Waiting.Kind != "execution_reconciliation" || stored.Retry == nil || !stored.Retry.MutationPossible {
			t.Fatalf("ambiguous state=%s calls=%d wait=%#v retry=%#v err=%v load=%v", state.Status, calls, stored.Waiting, stored.Retry, err, loadErr)
		}
	})
}

type mutationThenNoProgressRunner struct{}

func (*mutationThenNoProgressRunner) RunRole(_ context.Context, request RoleRunRequest) (RoleRunResult, error) {
	attempt := request.RetryHandle.CurrentAttempt()
	err := request.ToolEventSink(ToolDiagnostic{AttemptID: attempt.AttemptID, Tool: "write_file", Status: "started", MutationID: "mutation-stable", MutationPossible: true})
	if err != nil {
		return RoleRunResult{}, err
	}
	return RoleRunResult{}, &subagent.NoProgressError{RepeatedObservations: 6}
}

type durableObservationRunner struct {
	inner      RoleRunner
	firstError error
	counts     []int
}

func (r *durableObservationRunner) RunRole(ctx context.Context, request RoleRunRequest) (RoleRunResult, error) {
	if request.Run.CurrentRole == RolePlanner {
		attempt := request.RetryHandle.CurrentAttempt()
		if err := request.ToolEventSink(ToolDiagnostic{AttemptID: attempt.AttemptID, Tool: "read_file", Status: "completed", Sequence: 1, ObservationHash: "obs-a", NoProgressEligible: true}); err != nil {
			return RoleRunResult{}, err
		}
		count, err := request.RetryHandle.NoProgressCount(attempt.AttemptID, "obs-a")
		if err != nil {
			return RoleRunResult{}, err
		}
		r.counts = append(r.counts, count)
		if len(r.counts) == 1 {
			return RoleRunResult{}, r.firstError
		}
		return scriptedResult(OutcomePlanReady), nil
	}
	return r.inner.RunRole(ctx, request)
}

type consumeThenFailRunner struct {
	inner            RoleRunner
	err              error
	staleErr         error
	recoveryFeedback string
	consumed         bool
}

func (r *consumeThenFailRunner) RunRole(ctx context.Context, request RoleRunRequest) (RoleRunResult, error) {
	if request.Run.CurrentRole == RolePlanner && !r.consumed {
		r.consumed = true
		attempt := request.RetryHandle.CurrentAttempt()
		if err := request.RetryHandle.RecordCorrection(attempt.AttemptID, "schema_correction", "invalid_schema"); err != nil {
			return RoleRunResult{}, err
		}
		if _, err := request.RetryHandle.ConsumeRecovery(attempt.AttemptID, "invalid_schema"); err != nil {
			return RoleRunResult{}, err
		}
		r.staleErr = request.RetryHandle.RecordProgress(attempt.AttemptID, "evidence", "tool:read_file", "hash")
		return RoleRunResult{LocalIterations: 2, TokenUsage: cost.TokenUsage{PromptTokens: 3, TotalTokens: 3}}, r.err
	}
	if request.Run.CurrentRole == RolePlanner && r.consumed {
		r.recoveryFeedback = request.RecoveryFeedback
	}
	return r.inner.RunRole(ctx, request)
}

func newMutableClockRuntime(t *testing.T, definition Definition, runner RoleRunner, env durableTestEnvironment, now *time.Time) *DurableRuntime {
	t.Helper()
	runtime, err := NewDurableRuntime(mustAdaptGraph(t, definition), runner, env.store, env.claimer, env.events,
		DurableRuntimeOptions{Clock: func() time.Time { return *now }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}
