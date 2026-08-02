package multiagent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/emaharmony/prism/internal/cost"
	"github.com/emaharmony/prism/internal/event"
	"github.com/emaharmony/prism/internal/validation"
)

// LoopKind identifies a bounded correction edge. For a compiled graph
// (compiler.go), a LoopKind equals its owning edge's own stable ID (see
// CompiledGraph.LoopFor) — an open-ended identity that scales to any number
// of developer-defined loops, not just the two Phase 1 built-ins below.
type LoopKind string

// LoopTesterToDeveloper and LoopReviewerToDeveloper identify the two Phase 1
// built-in correction-loop edges. Before Phase 3 these were untyped string
// constants ("tester_to_developer" / "reviewer_to_developer"); Go does not
// allow a function call in a const initializer, so — to make them agree with
// every CompiledGraph's edgeID-based loop-kind keying (both the new
// authored-YAML path in compiler.go and the legacy-Definition path in
// compat.go) — they are now package-level vars computed via the same
// edgeID(...) stable-ID scheme every compiled graph uses. The exported
// identifiers are unchanged, so every existing reference to
// LoopTesterToDeveloper/LoopReviewerToDeveloper by name keeps compiling and
// keeps meaning "the tester->developer / reviewer->developer correction
// loop" — only the underlying string value changed, from the old bare slug
// to "edge:tester:tests_failed" / "edge:reviewer:changes_requested".
//
// Nothing else in this package requires these to be typed constants: LoopKind
// is a plain string type, and they are only ever used as map keys,
// switch-case values, or in string/equality comparisons, all of which work
// identically for a var of a string-based type.
var (
	LoopTesterToDeveloper   = LoopKind(edgeID(RoleTester, OutcomeTestsFailed))
	LoopReviewerToDeveloper = LoopKind(edgeID(RoleReviewer, OutcomeChangesRequested))
)

// loopBudgetSlug returns the human-readable, backward-compatible slug used in
// budget-error names and budget-warning payloads for kind. Before Phase 3,
// these strings were derived directly from LoopKind's own (bare-slug) value;
// now that LoopKind is edgeID-shaped ("edge:tester:tests_failed"), deriving
// them directly would change every persisted/observed budget name and event
// payload for the two Phase 1 loops — a real behavioral regression existing
// callers (CLI output, dashboard, and multiple tests asserting the exact
// strings "max_tester_to_developer_loops" / "tester_to_developer_loop")
// depend on. This mapping keeps those exact strings stable for the two
// known Phase 1 loops; any other (future, developer-defined) loop kind falls
// back to its own LoopKind value, which is the best available generic slug.
func loopBudgetSlug(kind LoopKind) string {
	switch kind {
	case LoopTesterToDeveloper:
		return "tester_to_developer"
	case LoopReviewerToDeveloper:
		return "reviewer_to_developer"
	default:
		return string(kind)
	}
}

// loopBudgetName returns the BudgetExceededError.Budget name for a loop
// budget check on kind, e.g. "max_tester_to_developer_loops". See
// loopBudgetSlug for why this is not simply "max_" + string(kind) + "_loops".
func loopBudgetName(kind LoopKind) string {
	return "max_" + loopBudgetSlug(kind) + "_loops"
}

// LoopTraversalCounts records completed correction-loop traversals, open to
// any number of developer-defined loop kinds. Before Phase 3 this was a
// fixed two-field struct ({TesterToDeveloper, ReviewerToDeveloper int}); it
// is now a map so a developer-authored graph with any number of loop edges
// can be tracked uniformly, using the same LoopKind identity
// CompiledGraph.LoopFor already produces. UnmarshalJSON below detects and
// migrates the OLD two-field wire shape — every DurableRun JSON blob
// persisted before this change has exactly that shape — onto the new
// map-based shape, at the correct edgeID-based keys. See
// loop_traversal_legacy_json_test.go for the end-to-end proof this migration
// is correct.
type LoopTraversalCounts struct {
	Counts map[LoopKind]int `json:"counts"`
}

// Get returns the recorded traversals for kind. A nil/absent entry is 0.
func (c LoopTraversalCounts) Get(kind LoopKind) int {
	return c.Counts[kind]
}

func (c *LoopTraversalCounts) increment(kind LoopKind) {
	if c.Counts == nil {
		c.Counts = make(map[LoopKind]int)
	}
	c.Counts[kind]++
}

// legacyLoopTraversalCounts is the exact pre-Phase-3 wire shape. The field
// tags below are fixed historical wire literals — every LoopTraversalCounts
// ever persisted before this change has exactly these two flat integer
// fields — not tied to LoopTesterToDeveloper/LoopReviewerToDeveloper's
// current (edgeID-based) Go value.
type legacyLoopTraversalCounts struct {
	TesterToDeveloper   int `json:"tester_to_developer"`
	ReviewerToDeveloper int `json:"reviewer_to_developer"`
}

// UnmarshalJSON accepts the current {"counts": {...}} shape and falls back
// to decoding the legacy flat two-field shape, remapping it onto
// Counts[LoopTesterToDeveloper]/Counts[LoopReviewerToDeveloper] (the
// edgeID-valued vars above) so a resumed historical run's migrated counts
// live at exactly the keys CompiledGraph.LoopFor produces today.
//
// Detection is based on whether the raw JSON object has a "counts" key at
// all (probed via map[string]json.RawMessage), not on whether the decoded
// value is nil/empty: a NEW-shape record with Counts == nil marshals to
// {"counts":null}, which still has a "counts" key and must decode back to a
// nil map (round-trip fidelity — see TestRunStateValidateAndJSONRoundTrip in
// state_test.go) rather than being misdetected as the legacy shape.
func (c *LoopTraversalCounts) UnmarshalJSON(data []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if raw, ok := probe["counts"]; ok {
		var counts map[LoopKind]int
		if err := json.Unmarshal(raw, &counts); err != nil {
			return err
		}
		c.Counts = counts
		return nil
	}

	var legacy legacyLoopTraversalCounts
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}
	counts := make(map[LoopKind]int, 2)
	if legacy.TesterToDeveloper != 0 {
		counts[LoopTesterToDeveloper] = legacy.TesterToDeveloper
	}
	if legacy.ReviewerToDeveloper != 0 {
		counts[LoopReviewerToDeveloper] = legacy.ReviewerToDeveloper
	}
	c.Counts = counts
	return nil
}

// RunView is the intentionally limited state visible to a role runner.
// It contains no mutation methods and no references to supervisor-owned maps.
type RunView struct {
	RunID           string
	WorkflowID      string
	Task            TaskReference
	WorkspaceID     string
	CurrentRole     Role
	ExecutionKey    string
	Visit           int
	TransitionCount int
	LoopTraversals  LoopTraversalCounts
	BudgetUsage     BudgetUsage
	IncomingHandoff *Handoff
}

// RoleRunRequest is the complete, read-only input for one role visit.
type RoleRunRequest struct {
	Run        RunView
	RoleConfig RoleConfig
}

// HandoffDraft contains role-produced context without routing authority.
// The supervisor assigns source, destination, outcome, run, and timestamp.
type HandoffDraft struct {
	ID                string
	TaskRef           string
	Objective         string
	Artifacts         []ArtifactRef
	Evidence          []ArtifactRef
	Reason            string
	ValidationResults []validation.Result
	UnresolvedIssues  []Issue
	Notes             string
}

// ExecutionMetadata is observational data returned by a runner.
type ExecutionMetadata struct {
	AgentRef         string
	Provider         string
	Model            string
	WorkspaceID      string
	ToolCalls        int
	DeniedToolCalls  int
	ValidationStatus string
	ApprovalStatus   string
	Attempt          int
	StartedAt        time.Time
	FinishedAt       time.Time
}

// RoleRunResult is the bounded result of one role visit.
type RoleRunResult struct {
	Outcome         TransitionOutcome
	OutgoingHandoff *HandoffDraft
	TokenUsage      cost.TokenUsage
	LocalIterations int
	Retries         int
	Metadata        ExecutionMetadata
}

// RoleRunner executes bounded local work for one configured role. It cannot
// mutate global state or choose a destination role.
type RoleRunner interface {
	RunRole(context.Context, RoleRunRequest) (RoleRunResult, error)
}

// EventSink consumes canonical Prism events emitted by the supervisor.
type EventSink interface {
	Emit(event.Event)
}

// EventSinkFunc adapts a function to EventSink.
type EventSinkFunc func(event.Event)

// Emit implements EventSink.
func (f EventSinkFunc) Emit(evt event.Event) {
	if f != nil {
		f(evt)
	}
}

// RunRequest supplies identity and task context for a new in-memory run.
type RunRequest struct {
	RunID string
	Task  TaskReference
}

// ResolvedTransition is a supervisor-selected edge or terminal result.
type ResolvedTransition struct {
	From     Role              `json:"from"`
	Outcome  TransitionOutcome `json:"outcome"`
	To       Role              `json:"to,omitempty"`
	Terminal TerminalCondition `json:"terminal,omitempty"`
}

// InvalidTransitionError reports a role/outcome pair with no declared route.
type InvalidTransitionError struct {
	Role    Role
	Outcome TransitionOutcome
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("multiagent: no transition from role %q for outcome %q", e.Role, e.Outcome)
}

// RoleExecutionError reports a failed or invalid runner execution.
type RoleExecutionError struct {
	Role  Role
	Cause error
}

func (e *RoleExecutionError) Error() string {
	return fmt.Sprintf("multiagent: role %q execution failed: %v", e.Role, e.Cause)
}

// Unwrap exposes the underlying runner or contract error.
func (e *RoleExecutionError) Unwrap() error {
	return e.Cause
}

// BudgetExceededError identifies the deterministic ceiling that stopped a run.
type BudgetExceededError struct {
	Budget string
	Role   Role
	Used   int
	Limit  int
}

func (e *BudgetExceededError) Error() string {
	if e.Role != "" {
		return fmt.Sprintf(
			"multiagent: budget %q exhausted for role %q: used %d, limit %d",
			e.Budget,
			e.Role,
			e.Used,
			e.Limit,
		)
	}
	return fmt.Sprintf("multiagent: budget %q exhausted: used %d, limit %d", e.Budget, e.Used, e.Limit)
}
