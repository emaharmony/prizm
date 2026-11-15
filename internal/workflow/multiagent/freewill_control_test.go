package multiagent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/freewill"
)

type haltBlockingRoleRunner struct {
	started chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

type haltRaceRoleRunner struct {
	inner              *scriptedRunner
	started            chan struct{}
	release            chan struct{}
	once               sync.Once
	failure            error
	ignoreCancellation bool
	gateAnyRole        bool
}

func (r *haltRaceRoleRunner) RunRole(ctx context.Context, request RoleRunRequest) (RoleRunResult, error) {
	if r.gateAnyRole || request.Run.CurrentRole == RolePlanner {
		r.once.Do(func() { close(r.started) })
		if r.ignoreCancellation {
			<-r.release
		} else {
			select {
			case <-r.release:
			case <-ctx.Done():
				return RoleRunResult{}, ctx.Err()
			}
		}
		if r.failure != nil {
			return RoleRunResult{}, r.failure
		}
	}
	return r.inner.RunRole(ctx, request)
}

func (r *haltRaceRoleRunner) ValidateApprovedRole(ctx context.Context, request RoleRunRequest, result RoleRunResult) (RoleRunResult, error) {
	return r.inner.ValidateApprovedRole(ctx, request, result)
}

func (r *haltBlockingRoleRunner) RunRole(ctx context.Context, _ RoleRunRequest) (RoleRunResult, error) {
	r.calls.Add(1)
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	return RoleRunResult{}, ctx.Err()
}

func TestFreewillHaltCancelsActiveChildAndPreventsResume(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runner := &haltBlockingRoleRunner{started: make(chan struct{})}
	runtime := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events)
	control, err := NewFreewillOperatorControl(env.store, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := freewill.Scope{ProjectID: "project-halt", Workspace: "workspace-halt"}
	identity := FreewillOperatorControlState{AgentID: "agent-halt", Scope: scope}
	request := testRunRequest()
	request.RunID = "run-freewill-active-halt"
	runCtx, cancelRun := context.WithCancel(context.Background())
	done := make(chan struct {
		state RunState
		err   error
	}, 1)
	go func() {
		state, runErr := runtime.Run(runCtx, request)
		done <- struct {
			state RunState
			err   error
		}{state, runErr}
	}()
	defer cancelRun()
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("child role did not start")
	}
	var durableCancelErr error
	unregister, err := control.RegisterActiveExecution(identity.AgentID, identity.Scope, 0, func() {
		_, durableCancelErr = runtime.Cancel(context.Background(), request.RunID, "freewill operator emergency halt")
		cancelRun()
	})
	if err != nil {
		t.Fatalf("register active child: %v", err)
	}
	defer unregister()
	if _, err := control.SetHalted(context.Background(), identity.AgentID, identity.Scope, true, "owner"); err != nil {
		t.Fatal(err)
	}
	if durableCancelErr != nil {
		t.Fatalf("persist child cancellation: %v", durableCancelErr)
	}
	select {
	case result := <-done:
		if result.state.Status != RunStatusCancelled {
			t.Fatalf("child status after emergency halt = %s, want cancelled (err=%v)", result.state.Status, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight child did not stop promptly after halt")
	}
	stored, err := env.store.Load(context.Background(), request.RunID)
	if err != nil || stored.State.Status != RunStatusCancelled || stored.Phase != CheckpointTerminal {
		t.Fatalf("child cancellation was not durable: status=%s phase=%s err=%v", stored.State.Status, stored.Phase, err)
	}
	if _, requested, err := env.store.CancellationRequest(context.Background(), request.RunID); err != nil || !requested {
		t.Fatalf("durable cancellation request missing: requested=%v err=%v", requested, err)
	}
	if _, err := control.SetHalted(context.Background(), identity.AgentID, identity.Scope, false, "owner"); err != nil {
		t.Fatal(err)
	}
	resumed, err := runtime.Resume(context.Background(), request.RunID)
	if err != nil && !errors.Is(err, ErrRunAlreadyTerminal) {
		t.Fatalf("resume canceled child after unhalt: %v", err)
	}
	if resumed.Status != RunStatusCancelled || runner.calls.Load() != 1 {
		t.Fatalf("unhalt resurrected canceled child: status=%s calls=%d", resumed.Status, runner.calls.Load())
	}
}

func TestFreewillCompletionWinsHaltAfterTerminalCheckpoint(t *testing.T) {
	env := newDurableTestEnvironment(t)
	runner := happyDurableRunner()
	runtime := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events)
	control, err := NewFreewillOperatorControl(env.store, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := freewill.Scope{ProjectID: "project-complete", Workspace: "workspace-complete"}
	request := testRunRequest()
	request.RunID = "run-freewill-completion-halt-race"
	state, err := runtime.Run(context.Background(), request)
	if err != nil || state.Status != RunStatusCompleted {
		t.Fatalf("complete child fixture: status=%s err=%v", state.Status, err)
	}
	unregister, err := control.RegisterActiveExecution("agent-complete", scope, 0, func() {
		_, _ = runtime.Cancel(context.Background(), request.RunID, "freewill operator emergency halt")
	})
	if err != nil {
		t.Fatalf("register terminal child callback: %v", err)
	}
	defer unregister()
	if _, err := control.SetHalted(context.Background(), "agent-complete", scope, true, "owner"); err != nil {
		t.Fatal(err)
	}
	resumed, err := runtime.Resume(context.Background(), request.RunID)
	if err != nil || resumed.Status != RunStatusCompleted {
		t.Fatalf("halt relabeled terminal success: status=%s err=%v", resumed.Status, err)
	}
	if got, want := runnerCalls(runner), []Role{RolePlanner, RoleDeveloper, RoleTester, RoleReviewer}; !reflect.DeepEqual(got, want) {
		t.Fatalf("completed child changed after halt: calls=%v want=%v", got, want)
	}
}

func TestFreewillHaltRacesRoleCompletionWithoutResurrection(t *testing.T) {
	env := newDurableTestEnvironment(t)
	inner := happyDurableRunner()
	runner := &haltRaceRoleRunner{inner: inner, started: make(chan struct{}), release: make(chan struct{})}
	runtime := newDurableRuntimeForTest(t, validDefinition(), runner, env.store, env.claimer, env.events)
	control, err := NewFreewillOperatorControl(env.store, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := freewill.Scope{ProjectID: "project-halt-race", Workspace: "workspace-halt-race"}
	request := testRunRequest()
	request.RunID = "run-freewill-halt-completion-race"
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	var runtimeCancelErr error
	unregister, err := control.RegisterActiveExecution("agent-halt-race", scope, 0, func() {
		_, runtimeCancelErr = runtime.Cancel(context.Background(), request.RunID, "freewill operator emergency halt")
		cancelRun()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	done := make(chan struct {
		state RunState
		err   error
	}, 1)
	go func() {
		state, runErr := runtime.Run(runCtx, request)
		done <- struct {
			state RunState
			err   error
		}{state, runErr}
	}()
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("child role did not reach completion race")
	}
	race := make(chan struct{})
	haltDone := make(chan error, 1)
	go func() {
		<-race
		_, err := control.SetHalted(context.Background(), "agent-halt-race", scope, true, "owner")
		haltDone <- err
	}()
	go func() {
		<-race
		close(runner.release)
	}()
	close(race)
	if err := <-haltDone; err != nil {
		t.Fatal(err)
	}
	var final RunState
	var runErr error
	select {
	case result := <-done:
		final, runErr = result.state, result.err
	case <-time.After(2 * time.Second):
		t.Fatal("halt/completion race did not reach a terminal checkpoint")
	}
	if final.Status != RunStatusCompleted && final.Status != RunStatusCancelled {
		// If cancellation lands after the role checkpoint but before its next
		// transition checkpoint, the durable request is consumed by recovery.
		// Resume must observe that request before starting another role.
		final, runErr = runtime.Resume(context.Background(), request.RunID)
	}
	if final.Status != RunStatusCompleted && final.Status != RunStatusCancelled {
		persisted, loadErr := env.store.Load(context.Background(), request.RunID)
		requestedReason, requested, requestErr := env.store.CancellationRequest(context.Background(), request.RunID)
		t.Fatalf("halt/completion race ended in nonterminal or unrelated state %s (durable=%s phase=%s load=%v cancel=%v requested=%v reason=%q requestErr=%v runErr=%v)",
			final.Status, persisted.State.Status, persisted.Phase, loadErr, runtimeCancelErr, requested, requestedReason, requestErr, runErr)
	}
	calls := runnerCalls(inner)
	resumed, resumeErr := runtime.Resume(context.Background(), request.RunID)
	if final.Status == RunStatusCompleted && resumeErr != nil {
		t.Fatalf("completed child resume: %v", resumeErr)
	}
	if resumed.Status != final.Status || !reflect.DeepEqual(runnerCalls(inner), calls) {
		t.Fatalf("terminal race outcome changed on replay: before=%s after=%s calls=%v want=%v", final.Status, resumed.Status, runnerCalls(inner), calls)
	}
}

func TestFreewillHaltRacesRoleFailureCheckpoint(t *testing.T) {
	env := newDurableTestEnvironment(t)
	definition := baseDef()
	definition.Spec.Nodes = append(definition.Spec.Nodes, roleNode("reflector", "done"))
	definition.Spec.Edges = append(definition.Spec.Edges, edge("reflector-end", "reflector", "end", "done"))
	definition.Spec.Reflection = &SchemaReflectionPolicy{Enabled: true, Role: "reflector", ReplanRole: "start", MaxReplans: 1, Triggers: []string{string(ReflectionFailure)}}
	graph, _, err := Compile(definition, nil, CompileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	runner := &haltRaceRoleRunner{inner: happyDurableRunner(), started: make(chan struct{}), release: make(chan struct{}), failure: errors.New("bounded role failure"), ignoreCancellation: true, gateAnyRole: true}
	reflectionStarted := make(chan struct{})
	runtime, err := NewDurableRuntime(graph, runner, env.store, env.claimer, env.events, DurableRuntimeOptions{
		Reflection: ReflectionRunnerFunc(func(ctx context.Context, _ ReflectionRequest) (ReflectionResult, error) {
			close(reflectionStarted)
			<-ctx.Done()
			return ReflectionResult{}, ctx.Err()
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewFreewillOperatorControl(env.store, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := freewill.Scope{ProjectID: "project-failure-halt-race", Workspace: "workspace-failure-halt-race"}
	request := testRunRequest()
	request.RunID = "run-freewill-failure-halt-race"
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	var runtimeCancelErr error
	unregister, err := control.RegisterActiveExecution("agent-failure-halt-race", scope, 0, func() {
		_, runtimeCancelErr = runtime.Cancel(context.Background(), request.RunID, "freewill operator emergency halt")
		cancelRun()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	done := make(chan struct {
		state RunState
		err   error
	}, 1)
	go func() {
		state, runErr := runtime.Run(runCtx, request)
		done <- struct {
			state RunState
			err   error
		}{state, runErr}
	}()
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("child role did not reach failure checkpoint race")
	}
	close(runner.release)
	select {
	case <-reflectionStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("failure reflection did not start after the post-role context check")
	}
	haltDone := make(chan error, 1)
	go func() {
		_, haltErr := control.SetHalted(context.Background(), "agent-failure-halt-race", scope, true, "owner")
		haltDone <- haltErr
	}()
	if err := <-haltDone; err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.state.Status != RunStatusFailed {
			t.Fatalf("terminal role outcome did not win exact checkpoint race: status=%s err=%v cancelErr=%v", result.state.Status, result.err, runtimeCancelErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("halt/failure race did not reach terminal checkpoint")
	}
	persisted, err := env.store.Load(context.Background(), request.RunID)
	if err != nil || persisted.Phase != CheckpointTerminal || persisted.State.Status != RunStatusFailed {
		t.Fatalf("halt/failure outcome not durable: status=%s phase=%s err=%v", persisted.State.Status, persisted.Phase, err)
	}
	callsBefore := len(runnerCalls(runner.inner))
	if _, err := runtime.Resume(context.Background(), request.RunID); err != nil && !errors.Is(err, ErrRunAlreadyTerminal) {
		t.Fatalf("resume after halt/failure: %v", err)
	}
	callsAfter := len(runnerCalls(runner.inner))
	if callsAfter != callsBefore {
		t.Fatalf("canceled role replayed after unhalt: calls before=%d after=%d", callsBefore, callsAfter)
	}
}
