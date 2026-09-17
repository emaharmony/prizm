package multiagent

import "github.com/emaharmony/prizm/internal/event"

// DashboardSnapshotSchemaVersion is the dashboard contract version for
// DashboardRunSnapshot. It is independent of RunStateSchemaVersion and
// DurableRunSchemaVersion: it versions the read-model shape exposed to the
// dashboard, not the persisted run envelope.
const DashboardSnapshotSchemaVersion = 1

// DashboardRunSnapshot is the complete, dashboard-facing view of one durable
// run: the existing CLI inspection fields (embedded, unchanged), plus the
// execution graph, budget visualization, and operator-relevant status this
// package's Phase 2 read model adds.
type DashboardRunSnapshot struct {
	ReferenceRunSnapshot

	SchemaVersion      int                 `json:"schema_version"`
	WorkflowVersion    int                 `json:"workflow_version"`
	Graph              RunGraph            `json:"graph"`
	Budgets            BudgetVisualization `json:"budgets"`
	Waiting            *WaitingState       `json:"waiting,omitempty"`
	CancellationReason string              `json:"cancellation_reason,omitempty"`
	// PauseReason is left empty in this milestone; operator-initiated pause
	// is wired in Milestone 9.
	PauseReason string `json:"pause_reason,omitempty"`
	// EventCursor is the last event ID (ULID) observed at snapshot build
	// time, i.e. allEvents[len(allEvents)-1].ID. Empty when allEvents is
	// empty.
	EventCursor string `json:"event_cursor,omitempty"`
}

// BuildDashboardSnapshot derives the complete dashboard read model for one
// durable run.
//
// Deviation from the originally sketched signature (record, allEvents): the
// graph and budget projections require the workflow Definition (roles,
// transitions, budget ceilings), which DurableRun does not carry — it
// persists only RunState and checkpoint/recovery metadata, not the
// definition that produced it (the CLI's manifest file owns that mapping
// today; see run_locator.go). definition is added as an explicit leading
// parameter.
//
// Unlike the CLI's executeReferenceWorkflowStatus, which truncates to the
// last 20 events for terminal display, allEvents must NOT be truncated
// before calling this function: BuildRunGraph needs the full history to
// derive per-edge traversal counts for edges that are not one of the two
// budget-tracked correction loops.
func BuildDashboardSnapshot(definition Definition, record DurableRun, allEvents []event.Event) DashboardRunSnapshot {
	base := BuildReferenceRunSnapshot(record.State, allEvents)

	roleVisits := make(map[Role]int, len(record.State.RoleStates))
	for role, roleState := range record.State.RoleStates {
		roleVisits[role] = roleState.Visits
	}

	snapshot := DashboardRunSnapshot{
		ReferenceRunSnapshot: base,
		SchemaVersion:        DashboardSnapshotSchemaVersion,
		WorkflowVersion:      record.State.WorkflowVersion,
		Graph:                BuildRunGraph(definition, record.State, allEvents),
		Budgets: BuildBudgetVisualization(
			record.State.BudgetUsage,
			definition.Budgets,
			record.State.LoopTraversals,
			record.State.BudgetUsage.Elapsed,
			record.State.TransitionCount,
			roleVisits,
		),
		Waiting:            cloneWaitingState(record.Waiting),
		CancellationReason: record.State.CancellationReason,
		EventCursor:        lastEventID(allEvents),
	}
	return snapshot
}

func cloneWaitingState(waiting *WaitingState) *WaitingState {
	if waiting == nil {
		return nil
	}
	cloned := *waiting
	return &cloned
}

func lastEventID(events []event.Event) string {
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1].ID
}
