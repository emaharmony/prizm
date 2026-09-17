package multiagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const roleOutputSchemaVersion = 1

// OutputHandoff is role-produced context without routing fields.
type OutputHandoff struct {
	Objective        string        `json:"objective"`
	Reason           string        `json:"reason"`
	Evidence         []ArtifactRef `json:"evidence,omitempty"`
	UnresolvedIssues []Issue       `json:"unresolved_issues,omitempty"`
	Notes            string        `json:"notes,omitempty"`
}

// PlannerOutput is the strict planner result contract.
type PlannerOutput struct {
	SchemaVersion      int           `json:"schema_version"`
	Understanding      string        `json:"understanding"`
	ImplementationPlan []string      `json:"implementation_plan"`
	TaskBreakdown      []string      `json:"task_breakdown"`
	AcceptanceCriteria []string      `json:"acceptance_criteria"`
	Risks              []string      `json:"risks,omitempty"`
	Assumptions        []string      `json:"assumptions,omitempty"`
	Handoff            OutputHandoff `json:"handoff"`
}

// DeveloperOutput is the strict developer result contract.
type DeveloperOutput struct {
	SchemaVersion    int           `json:"schema_version"`
	Summary          string        `json:"summary"`
	ChangedArtifacts []ArtifactRef `json:"changed_artifacts"`
	CommandsExecuted []string      `json:"commands_executed,omitempty"`
	KnownLimitations []string      `json:"known_limitations,omitempty"`
	Handoff          OutputHandoff `json:"handoff"`
}

// TestExecution is one tester-reported check.
type TestExecution struct {
	Name     string        `json:"name"`
	Status   string        `json:"status"`
	Evidence []ArtifactRef `json:"evidence,omitempty"`
}

// TesterOutput is the strict tester result contract.
type TesterOutput struct {
	SchemaVersion   int             `json:"schema_version"`
	Result          string          `json:"result"`
	TestsExecuted   []TestExecution `json:"tests_executed"`
	FailureEvidence []ArtifactRef   `json:"failure_evidence,omitempty"`
	Reproduction    []string        `json:"reproduction,omitempty"`
	Handoff         OutputHandoff   `json:"handoff"`
}

// ReviewFinding is one reviewer finding.
type ReviewFinding struct {
	Severity string        `json:"severity"`
	Summary  string        `json:"summary"`
	Evidence []ArtifactRef `json:"evidence,omitempty"`
}

// ReviewerOutput is the strict reviewer result contract.
type ReviewerOutput struct {
	SchemaVersion       int             `json:"schema_version"`
	Decision            string          `json:"decision"`
	Findings            []ReviewFinding `json:"findings,omitempty"`
	RequiredCorrections []string        `json:"required_corrections,omitempty"`
	Evidence            []ArtifactRef   `json:"evidence,omitempty"`
	Handoff             *OutputHandoff  `json:"handoff,omitempty"`
}

type decodedRoleOutput struct {
	Outcome TransitionOutcome
	Handoff *HandoffDraft
}

func decodeRoleOutput(role Role, raw string) (decodedRoleOutput, error) {
	switch role {
	case RolePlanner:
		var output PlannerOutput
		if err := decodeStrictJSON(raw, &output); err != nil {
			return decodedRoleOutput{}, structuredError(role, err)
		}
		if err := validatePlannerOutput(output); err != nil {
			return decodedRoleOutput{}, structuredError(role, err)
		}
		return decodedRoleOutput{
			Outcome: OutcomePlanReady,
			Handoff: handoffFromOutput(output.Handoff, nil),
		}, nil
	case RoleDeveloper:
		var output DeveloperOutput
		if err := decodeStrictJSON(raw, &output); err != nil {
			return decodedRoleOutput{}, structuredError(role, err)
		}
		if err := validateDeveloperOutput(output); err != nil {
			return decodedRoleOutput{}, structuredError(role, err)
		}
		return decodedRoleOutput{
			Outcome: OutcomeImplementationReady,
			Handoff: handoffFromOutput(output.Handoff, output.ChangedArtifacts),
		}, nil
	case RoleTester:
		var output TesterOutput
		if err := decodeStrictJSON(raw, &output); err != nil {
			return decodedRoleOutput{}, structuredError(role, err)
		}
		if err := validateTesterOutput(output); err != nil {
			return decodedRoleOutput{}, structuredError(role, err)
		}
		outcome := OutcomeTestsPassed
		if output.Result == "failed" {
			outcome = OutcomeTestsFailed
		}
		handoff := handoffFromOutput(output.Handoff, nil)
		handoff.Evidence = append(handoff.Evidence, output.FailureEvidence...)
		return decodedRoleOutput{Outcome: outcome, Handoff: handoff}, nil
	case RoleReviewer:
		var output ReviewerOutput
		if err := decodeStrictJSON(raw, &output); err != nil {
			return decodedRoleOutput{}, structuredError(role, err)
		}
		if err := validateReviewerOutput(output); err != nil {
			return decodedRoleOutput{}, structuredError(role, err)
		}
		if output.Decision == "approved" {
			return decodedRoleOutput{Outcome: OutcomeReviewApproved}, nil
		}
		handoff := handoffFromOutput(*output.Handoff, nil)
		handoff.Evidence = append(handoff.Evidence, output.Evidence...)
		for i, correction := range output.RequiredCorrections {
			handoff.UnresolvedIssues = append(handoff.UnresolvedIssues, Issue{
				ID:       fmt.Sprintf("review-%d", i+1),
				Summary:  correction,
				Blocking: true,
			})
		}
		return decodedRoleOutput{Outcome: OutcomeChangesRequested, Handoff: handoff}, nil
	default:
		return decodedRoleOutput{}, structuredError(role, errors.New("unsupported role"))
	}
}

func decodeStrictJSON(raw string, target any) error {
	decoder := json.NewDecoder(bytes.NewBufferString(strings.TrimSpace(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func validatePlannerOutput(output PlannerOutput) error {
	if err := validateSchemaVersion(output.SchemaVersion); err != nil {
		return err
	}
	if strings.TrimSpace(output.Understanding) == "" {
		return errors.New("understanding is required")
	}
	if len(output.ImplementationPlan) == 0 ||
		len(output.TaskBreakdown) == 0 ||
		len(output.AcceptanceCriteria) == 0 {
		return errors.New("implementation_plan, task_breakdown, and acceptance_criteria are required")
	}
	if err := validateRequiredStrings("implementation_plan", output.ImplementationPlan); err != nil {
		return err
	}
	if err := validateRequiredStrings("task_breakdown", output.TaskBreakdown); err != nil {
		return err
	}
	if err := validateRequiredStrings("acceptance_criteria", output.AcceptanceCriteria); err != nil {
		return err
	}
	return validateOutputHandoff(output.Handoff)
}

func validateDeveloperOutput(output DeveloperOutput) error {
	if err := validateSchemaVersion(output.SchemaVersion); err != nil {
		return err
	}
	if strings.TrimSpace(output.Summary) == "" {
		return errors.New("summary is required")
	}
	if len(output.ChangedArtifacts) == 0 {
		return errors.New("changed_artifacts is required")
	}
	if err := validateOutputArtifacts(output.ChangedArtifacts); err != nil {
		return err
	}
	return validateOutputHandoff(output.Handoff)
}

func validateTesterOutput(output TesterOutput) error {
	if err := validateSchemaVersion(output.SchemaVersion); err != nil {
		return err
	}
	if output.Result != "passed" && output.Result != "failed" {
		return errors.New(`result must be "passed" or "failed"`)
	}
	if len(output.TestsExecuted) == 0 {
		return errors.New("tests_executed is required")
	}
	nonPassing := 0
	for i, test := range output.TestsExecuted {
		if strings.TrimSpace(test.Name) == "" {
			return fmt.Errorf("tests_executed[%d].name is required", i)
		}
		if test.Status != "passed" && test.Status != "failed" &&
			test.Status != "timeout" && test.Status != "error" {
			return fmt.Errorf("tests_executed[%d].status is invalid", i)
		}
		if test.Status != "passed" {
			nonPassing++
		}
		if err := validateOutputArtifacts(test.Evidence); err != nil {
			return fmt.Errorf("tests_executed[%d].evidence: %w", i, err)
		}
	}
	if output.Result == "passed" && nonPassing != 0 {
		return errors.New("passed result cannot contain a non-passing test")
	}
	if output.Result == "failed" && nonPassing == 0 {
		return errors.New("failed result requires a non-passing test")
	}
	if output.Result == "failed" && len(output.FailureEvidence) == 0 &&
		len(output.Reproduction) == 0 {
		return errors.New("failed result requires failure_evidence or reproduction")
	}
	if err := validateOutputArtifacts(output.FailureEvidence); err != nil {
		return fmt.Errorf("failure_evidence: %w", err)
	}
	return validateOutputHandoff(output.Handoff)
}

func validateReviewerOutput(output ReviewerOutput) error {
	if err := validateSchemaVersion(output.SchemaVersion); err != nil {
		return err
	}
	if output.Decision != "approved" && output.Decision != "changes_requested" {
		return errors.New(`decision must be "approved" or "changes_requested"`)
	}
	for i, finding := range output.Findings {
		if strings.TrimSpace(finding.Summary) == "" {
			return fmt.Errorf("findings[%d].summary is required", i)
		}
		switch finding.Severity {
		case "info", "low", "medium", "high", "critical":
		default:
			return fmt.Errorf("findings[%d].severity is invalid", i)
		}
		if err := validateOutputArtifacts(finding.Evidence); err != nil {
			return fmt.Errorf("findings[%d].evidence: %w", i, err)
		}
	}
	if err := validateOutputArtifacts(output.Evidence); err != nil {
		return fmt.Errorf("evidence: %w", err)
	}
	if output.Decision == "approved" {
		if output.Handoff != nil {
			return errors.New("approved review must not include a handoff")
		}
		if len(output.RequiredCorrections) != 0 {
			return errors.New("approved review must not include required_corrections")
		}
		return nil
	}
	if output.Handoff == nil {
		return errors.New("changes_requested review requires a handoff")
	}
	if len(output.RequiredCorrections) == 0 {
		return errors.New("changes_requested review requires required_corrections")
	}
	if err := validateRequiredStrings("required_corrections", output.RequiredCorrections); err != nil {
		return err
	}
	return validateOutputHandoff(*output.Handoff)
}

func validateSchemaVersion(version int) error {
	if version != roleOutputSchemaVersion {
		return fmt.Errorf("schema_version must be %d", roleOutputSchemaVersion)
	}
	return nil
}

func validateOutputHandoff(handoff OutputHandoff) error {
	if strings.TrimSpace(handoff.Objective) == "" {
		return errors.New("handoff.objective is required")
	}
	if strings.TrimSpace(handoff.Reason) == "" {
		return errors.New("handoff.reason is required")
	}
	if err := validateOutputArtifacts(handoff.Evidence); err != nil {
		return err
	}
	for i, issue := range handoff.UnresolvedIssues {
		if strings.TrimSpace(issue.ID) == "" || strings.TrimSpace(issue.Summary) == "" {
			return fmt.Errorf("handoff.unresolved_issues[%d] requires id and summary", i)
		}
	}
	return nil
}

func validateOutputArtifacts(artifacts []ArtifactRef) error {
	for i, artifact := range artifacts {
		if !artifact.Kind.Valid() || strings.TrimSpace(artifact.URI) == "" {
			return fmt.Errorf("artifact[%d] requires a valid kind and uri", i)
		}
	}
	return nil
}

func validateRequiredStrings(field string, values []string) error {
	for i, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s[%d] must not be empty", field, i)
		}
	}
	return nil
}

func handoffFromOutput(output OutputHandoff, artifacts []ArtifactRef) *HandoffDraft {
	return &HandoffDraft{
		Objective:        output.Objective,
		Reason:           output.Reason,
		Artifacts:        cloneArtifactRefs(artifacts),
		Evidence:         cloneArtifactRefs(output.Evidence),
		UnresolvedIssues: cloneIssues(output.UnresolvedIssues),
		Notes:            output.Notes,
	}
}

func structuredError(role Role, err error) error {
	return &StructuredOutputError{Role: role, Cause: err}
}
