package multiagent

import (
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prism/internal/validation"
)

func TestHandoffValidate(t *testing.T) {
	handoff := validHandoff()
	if err := handoff.Validate(mustAdaptGraph(t, validDefinition())); err != nil {
		t.Fatalf("valid handoff rejected: %v", err)
	}
}

func TestHandoffRejectsInvalidContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Handoff)
		want   string
	}{
		{
			name: "unknown role",
			mutate: func(h *Handoff) {
				h.DestinationRole = Role("operator")
			},
			want: "unknown destination role",
		},
		{
			name: "self transition",
			mutate: func(h *Handoff) {
				h.DestinationRole = h.SourceRole
			},
			want: "self-transition",
		},
		{
			name: "unsupported outcome",
			mutate: func(h *Handoff) {
				h.Outcome = TransitionOutcome("ship_it")
			},
			want: "unsupported outcome",
		},
		{
			name: "unstructured objective",
			mutate: func(h *Handoff) {
				h.Objective = ""
			},
			want: "objective is required",
		},
		{
			name: "invalid artifact",
			mutate: func(h *Handoff) {
				h.Artifacts[0].URI = ""
			},
			want: "uri is required",
		},
		{
			name: "invalid validation result",
			mutate: func(h *Handoff) {
				h.ValidationResults[0].Status = "maybe"
			},
			want: "unknown status",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handoff := validHandoff()
			test.mutate(&handoff)
			err := handoff.Validate(mustAdaptGraph(t, validDefinition()))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func validHandoff() Handoff {
	return Handoff{
		ID:              "handoff-001",
		RunID:           "run-001",
		SourceRole:      RolePlanner,
		DestinationRole: RoleDeveloper,
		TaskRef:         "task-001",
		Objective:       "Implement the approved plan.",
		Artifacts: []ArtifactRef{
			{Kind: ArtifactFile, URI: "artifacts/plan.md", Digest: "sha256:plan"},
		},
		Evidence: []ArtifactRef{
			{Kind: ArtifactReport, URI: "artifacts/architecture-review.md"},
		},
		Outcome: OutcomePlanReady,
		Reason:  "The plan meets the workflow entry criteria.",
		ValidationResults: []validation.Result{
			{Profile: "plan_schema", Status: "passed", ExitCode: 0},
		},
		UnresolvedIssues: []Issue{
			{ID: "risk-1", Summary: "Confirm provider availability.", Blocking: false},
		},
		Notes:     "Use the structured plan artifact as the source of truth.",
		CreatedAt: time.Date(2026, 7, 23, 18, 0, 0, 0, time.UTC),
	}
}
