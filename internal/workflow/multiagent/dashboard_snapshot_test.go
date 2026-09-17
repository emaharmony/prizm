package multiagent

import (
	"reflect"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
)

func TestBuildDashboardSnapshotComposesReferenceSnapshotWithoutDrift(t *testing.T) {
	definition := validDefinition()
	state := validRunState()
	events := []event.Event{
		transitionSelectedEvent(RolePlanner, OutcomePlanReady, RoleDeveloper, 1),
	}
	record := DurableRun{
		SchemaVersion: DurableRunSchemaVersion,
		Revision:      3,
		State:         state,
		Phase:         CheckpointRoleRunning,
		Waiting:       nil,
	}

	got := BuildDashboardSnapshot(definition, record, events)
	want := BuildReferenceRunSnapshot(state, events)

	if !reflect.DeepEqual(got.ReferenceRunSnapshot, want) {
		t.Fatalf("embedded ReferenceRunSnapshot drifted from BuildReferenceRunSnapshot:\ngot:  %+v\nwant: %+v",
			got.ReferenceRunSnapshot, want)
	}
}

func TestBuildDashboardSnapshotFields(t *testing.T) {
	definition := validDefinition()
	state := validRunState()
	events := []event.Event{
		transitionSelectedEvent(RolePlanner, OutcomePlanReady, RoleDeveloper, 1),
		transitionSelectedEvent(RoleDeveloper, OutcomeImplementationReady, RoleTester, 2),
	}
	waiting := &WaitingState{Kind: "approval", Reason: "developer write requires approval", SafeToRetry: true}
	record := DurableRun{
		SchemaVersion: DurableRunSchemaVersion,
		Revision:      5,
		State:         state,
		Phase:         CheckpointWaiting,
		Waiting:       waiting,
	}

	snapshot := BuildDashboardSnapshot(definition, record, events)

	if snapshot.SchemaVersion != DashboardSnapshotSchemaVersion {
		t.Errorf("schema version = %d, want %d", snapshot.SchemaVersion, DashboardSnapshotSchemaVersion)
	}
	if snapshot.WorkflowVersion != state.WorkflowVersion {
		t.Errorf("workflow version = %d, want %d", snapshot.WorkflowVersion, state.WorkflowVersion)
	}
	if snapshot.CancellationReason != state.CancellationReason {
		t.Errorf("cancellation reason = %q, want %q", snapshot.CancellationReason, state.CancellationReason)
	}
	if snapshot.PauseReason != "" {
		t.Errorf("pause reason = %q, want empty in this milestone", snapshot.PauseReason)
	}
	wantCursor := events[len(events)-1].ID
	if snapshot.EventCursor != wantCursor {
		t.Errorf("event cursor = %q, want %q (last event id)", snapshot.EventCursor, wantCursor)
	}
	if snapshot.Waiting == nil || *snapshot.Waiting != *waiting {
		t.Errorf("waiting = %+v, want a clone of %+v", snapshot.Waiting, waiting)
	}
	if snapshot.Waiting == waiting {
		t.Error("Waiting must be a defensive clone, not an alias of record.Waiting")
	}

	wantGraph := BuildRunGraph(definition, state, events)
	if !reflect.DeepEqual(snapshot.Graph, wantGraph) {
		t.Errorf("graph does not match BuildRunGraph applied directly:\ngot:  %+v\nwant: %+v", snapshot.Graph, wantGraph)
	}
}

func TestBuildDashboardSnapshotEventCursorEmptyWithoutEvents(t *testing.T) {
	definition := validDefinition()
	record := DurableRun{
		SchemaVersion: DurableRunSchemaVersion,
		State:         validRunState(),
		Phase:         CheckpointRoleRunning,
	}

	snapshot := BuildDashboardSnapshot(definition, record, nil)

	if snapshot.EventCursor != "" {
		t.Errorf("event cursor = %q, want empty when there are no events", snapshot.EventCursor)
	}
	if snapshot.Waiting != nil {
		t.Errorf("waiting = %+v, want nil when record.Waiting is nil", snapshot.Waiting)
	}
}
