package multiagent

import (
	"fmt"
	"strings"
	"time"

	"github.com/emaharmony/prism/internal/validation"
)

// ArtifactKind identifies a structured artifact reference carried by a
// handoff. The artifact remains stored by its owning subsystem.
type ArtifactKind string

const (
	ArtifactFile        ArtifactKind = "file"
	ArtifactCommit      ArtifactKind = "commit"
	ArtifactPullRequest ArtifactKind = "pull_request"
	ArtifactReport      ArtifactKind = "report"
	ArtifactValidation  ArtifactKind = "validation"
)

// Valid reports whether the artifact kind is recognized.
func (k ArtifactKind) Valid() bool {
	switch k {
	case ArtifactFile, ArtifactCommit, ArtifactPullRequest, ArtifactReport, ArtifactValidation:
		return true
	default:
		return false
	}
}

// ArtifactRef identifies evidence or output without embedding its contents in
// the workflow state.
type ArtifactRef struct {
	Kind   ArtifactKind `json:"kind"`
	URI    string       `json:"uri"`
	Digest string       `json:"digest,omitempty"`
}

// Issue is unresolved work that the receiving role must account for.
type Issue struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	Blocking bool   `json:"blocking"`
}

// Handoff is the structured contract passed between logical roles.
// Notes are supplemental; routing depends on typed roles and Outcome.
type Handoff struct {
	ID                string              `json:"id"`
	RunID             string              `json:"run_id"`
	SourceRole        Role                `json:"source_role"`
	DestinationRole   Role                `json:"destination_role"`
	TaskRef           string              `json:"task_ref"`
	Objective         string              `json:"objective"`
	Artifacts         []ArtifactRef       `json:"artifacts,omitempty"`
	Evidence          []ArtifactRef       `json:"evidence,omitempty"`
	Outcome           TransitionOutcome   `json:"outcome"`
	Reason            string              `json:"reason"`
	ValidationResults []validation.Result `json:"validation_results,omitempty"`
	UnresolvedIssues  []Issue             `json:"unresolved_issues,omitempty"`
	Notes             string              `json:"notes,omitempty"`
	CreatedAt         time.Time           `json:"created_at"`
}

// Validate enforces handoff invariants against a compiled graph. Before
// Phase 3 this took the legacy Definition directly and delegated
// role/outcome/route legitimacy to Definition.ValidateTransition; it now
// takes the CompiledGraph every path (authored-YAML-compiled or
// CompatAdaptDefinition-adapted) converges on, and reimplements the
// equivalent checks generically against the graph's own accessors.
func (h Handoff) Validate(graph *CompiledGraph) error {
	var problems []string
	if strings.TrimSpace(h.ID) == "" {
		problems = append(problems, "handoff id is required")
	}
	if strings.TrimSpace(h.RunID) == "" {
		problems = append(problems, "handoff run_id is required")
	}
	if strings.TrimSpace(h.TaskRef) == "" {
		problems = append(problems, "handoff task_ref is required")
	}
	if strings.TrimSpace(h.Objective) == "" {
		problems = append(problems, "handoff objective is required")
	}
	if strings.TrimSpace(h.Reason) == "" {
		problems = append(problems, "handoff transition reason is required")
	}
	if h.CreatedAt.IsZero() {
		problems = append(problems, "handoff created_at is required")
	}
	if _, exists := graph.RoleConfig(h.SourceRole); !exists {
		problems = append(problems, fmt.Sprintf("handoff references unknown source role %q", h.SourceRole))
	}
	if _, exists := graph.RoleConfig(h.DestinationRole); !exists {
		problems = append(problems, fmt.Sprintf("handoff references unknown destination role %q", h.DestinationRole))
	}

	// Role/outcome/route legitimacy, generalized: the legacy path checked
	// outcome.Valid()/outcome.ValidFor(from) (Phase-1-hardcoded vocabulary)
	// then a linear scan of definition.Transitions for an exact match. The
	// graph-based equivalent uses the compiled graph's own (per-definition)
	// vocabulary — HasOutcome/OutcomeValidFor — and its precomputed routing
	// table (Resolve) instead. A legitimately compiled self-transition edge
	// (transition.To == h.SourceRole == h.DestinationRole) is therefore
	// correctly accepted here with no special case: CompiledGraph carries no
	// AllowSelfTransitions flag because a self-loop edge only ever appears in
	// the graph if it was already accepted as legitimate at compile/adapt
	// time. The "self-transition" wording is kept as a friendlier message in
	// the one case the old code specifically named it — source==destination
	// but the resolved route disagrees — not as an independent rule.
	switch {
	case !graph.HasOutcome(h.Outcome):
		problems = append(problems, fmt.Sprintf("handoff has unsupported outcome %q", h.Outcome))
	case !graph.OutcomeValidFor(h.SourceRole, h.Outcome):
		problems = append(problems, fmt.Sprintf("handoff outcome %q is invalid for role %q", h.Outcome, h.SourceRole))
	default:
		if transition, err := graph.Resolve(h.SourceRole, h.Outcome); err != nil {
			problems = append(problems, err.Error())
		} else if transition.To != h.DestinationRole {
			if h.SourceRole == h.DestinationRole {
				problems = append(problems, fmt.Sprintf(
					"handoff self-transition for role %q is not allowed", h.SourceRole))
			} else {
				problems = append(problems, fmt.Sprintf(
					"handoff destination role %q does not match declared transition destination %q for role %q outcome %q",
					h.DestinationRole, transition.To, h.SourceRole, h.Outcome))
			}
		}
	}
	validateArtifactRefs("handoff artifacts", h.Artifacts, &problems)
	validateArtifactRefs("handoff evidence", h.Evidence, &problems)
	for i, result := range h.ValidationResults {
		if strings.TrimSpace(result.Profile) == "" {
			problems = append(problems, fmt.Sprintf("handoff validation_results[%d].profile is required", i))
		}
		switch result.Status {
		case "passed", "failed", "timeout", "error":
		default:
			problems = append(problems, fmt.Sprintf("handoff validation_results[%d] has unknown status %q", i, result.Status))
		}
	}
	for i, issue := range h.UnresolvedIssues {
		if strings.TrimSpace(issue.ID) == "" {
			problems = append(problems, fmt.Sprintf("handoff unresolved_issues[%d].id is required", i))
		}
		if strings.TrimSpace(issue.Summary) == "" {
			problems = append(problems, fmt.Sprintf("handoff unresolved_issues[%d].summary is required", i))
		}
	}
	if len(problems) > 0 {
		return &ContractError{Problems: problems}
	}
	return nil
}

func validateArtifactRefs(prefix string, refs []ArtifactRef, problems *[]string) {
	for i, ref := range refs {
		if !ref.Kind.Valid() {
			*problems = append(*problems, fmt.Sprintf("%s[%d] has unknown kind %q", prefix, i, ref.Kind))
		}
		if strings.TrimSpace(ref.URI) == "" {
			*problems = append(*problems, fmt.Sprintf("%s[%d].uri is required", prefix, i))
		}
	}
}
