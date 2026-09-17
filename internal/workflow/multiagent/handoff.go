package multiagent

import (
	"fmt"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/validation"
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

// Validate enforces handoff invariants against a validated definition.
func (h Handoff) Validate(definition Definition) error {
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
	if _, exists := definition.RoleConfig(h.SourceRole); !exists {
		problems = append(problems, fmt.Sprintf("handoff references unknown source role %q", h.SourceRole))
	}
	if _, exists := definition.RoleConfig(h.DestinationRole); !exists {
		problems = append(problems, fmt.Sprintf("handoff references unknown destination role %q", h.DestinationRole))
	}
	if h.SourceRole == h.DestinationRole && !definition.AllowSelfTransitions {
		problems = append(problems, fmt.Sprintf("handoff self-transition for role %q is not allowed", h.SourceRole))
	}
	if err := definition.ValidateTransition(h.SourceRole, h.Outcome, h.DestinationRole, ""); err != nil {
		problems = append(problems, err.Error())
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
