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
	Summary          string        `json:"summary,omitempty"`
	Evidence         []ArtifactRef `json:"evidence,omitempty"`
	UnresolvedIssues []Issue       `json:"unresolved_issues,omitempty"`
	Notes            string        `json:"notes,omitempty"`
}

// UnmarshalJSON accepts a concise summary string as well as the strict object
// form. A summary supplies descriptive handoff text only; routing remains
// derived from the compiled graph. Object fields stay closed to unknown keys.
func (h *OutputHandoff) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var summary string
		if err := json.Unmarshal(trimmed, &summary); err != nil {
			return err
		}
		*h = OutputHandoff{Summary: summary, Objective: summary, Reason: "role handoff summary"}
		return nil
	}
	type handoffAlias OutputHandoff
	var decoded handoffAlias
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	if strings.TrimSpace(decoded.Objective) == "" && strings.TrimSpace(decoded.Summary) != "" {
		decoded.Objective = decoded.Summary
	}
	if strings.TrimSpace(decoded.Reason) == "" && strings.TrimSpace(decoded.Summary) != "" {
		decoded.Reason = "role handoff summary"
	}
	*h = OutputHandoff(decoded)
	return nil
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
	FanOut             *FanOutPlan   `json:"fan_out,omitempty"`
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

// UnmarshalJSON accepts the documented artifact objects and the string-only
// evidence form emitted by some text providers. Reviewer evidence is
// descriptive rather than an execution or mutation authority; normalizing a
// string to a file reference keeps the review record inspectable without
// weakening the strict contract for unknown fields or governed artifacts.
func (f *ReviewFinding) UnmarshalJSON(data []byte) error {
	var wire struct {
		Severity string          `json:"severity"`
		Summary  string          `json:"summary"`
		Evidence json.RawMessage `json:"evidence"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	*f = ReviewFinding{Severity: wire.Severity, Summary: wire.Summary}
	if len(wire.Evidence) == 0 || string(wire.Evidence) == "null" {
		return nil
	}
	if err := json.Unmarshal(wire.Evidence, &f.Evidence); err == nil {
		return nil
	}
	var uris []string
	if err := json.Unmarshal(wire.Evidence, &uris); err != nil {
		return fmt.Errorf("evidence must be artifact objects or strings: %w", err)
	}
	f.Evidence = make([]ArtifactRef, 0, len(uris))
	for _, uri := range uris {
		f.Evidence = append(f.Evidence, ArtifactRef{Kind: ArtifactFile, URI: uri})
	}
	return nil
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

type ReflectionOutput struct {
	SchemaVersion   int                    `json:"schema_version"`
	Verdict         string                 `json:"verdict"`
	Confidence      float64                `json:"confidence"`
	FailureClass    string                 `json:"failure_class"`
	Evidence        []ArtifactRef          `json:"evidence,omitempty"`
	LessonCandidate *MemoryCandidateOutput `json:"lesson_candidate,omitempty"`
	ReplanRequested bool                   `json:"replan_requested"`
	ReplanReason    string                 `json:"replan_reason,omitempty"`
}

type MemoryCandidateOutput struct {
	Summary   string   `json:"summary"`
	Content   string   `json:"content"`
	Category  string   `json:"category"`
	KeyTopics []string `json:"topics,omitempty"`
}

type decodedRoleOutput struct {
	Outcome TransitionOutcome
	Handoff *HandoffDraft
	FanOut  *FanOutPlan
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
			FanOut:  cloneFanOutPlan(output.FanOut),
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
		return decodeTesterRoleOutput(raw, false)
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

func decodeRoleOutputForRequest(request RoleRunRequest, raw string) (decodedRoleOutput, error) {
	if request.Run.CurrentRole == RoleTester && request.RoleConfig.ValidationProfilesExplicit && len(request.RoleConfig.ValidationProfiles) == 0 {
		return decodeTesterRoleOutput(raw, true)
	}
	return decodeRoleOutput(request.Run.CurrentRole, raw)
}

func decodeTesterRoleOutput(raw string, allowSkipped bool) (decodedRoleOutput, error) {
	var output TesterOutput
	if err := decodeStrictJSON(raw, &output); err != nil {
		return decodedRoleOutput{}, structuredError(RoleTester, err)
	}
	if err := validateTesterOutputWithSkipped(output, allowSkipped); err != nil {
		return decodedRoleOutput{}, structuredError(RoleTester, err)
	}
	outcome := OutcomeTestsPassed
	switch output.Result {
	case "failed":
		outcome = OutcomeTestsFailed
	case "skipped":
		outcome = OutcomeTestsSkipped
	}
	handoff := handoffFromOutput(output.Handoff, nil)
	handoff.Evidence = append(handoff.Evidence, output.FailureEvidence...)
	return decodedRoleOutput{Outcome: outcome, Handoff: handoff}, nil
}

// decodeDeveloperOutputForProposals preserves the strict developer schema but
// replaces changed_artifacts with exactly one persisted proposal artifact.
// The proposal artifact is a durable workspace fact, so model-provided claims
// are advisory output only and never become a second source of mutation truth.
func decodeDeveloperOutputForProposals(raw string, proposals []ProposalReference) (decodedRoleOutput, error) {
	var output DeveloperOutput
	if err := decodeStrictJSON(raw, &output); err != nil {
		return decodedRoleOutput{}, structuredError(RoleDeveloper, err)
	}
	artifacts, err := canonicalProposalArtifacts(proposals)
	if err != nil {
		return decodedRoleOutput{}, structuredError(RoleDeveloper, err)
	}
	output.ChangedArtifacts = artifacts
	if err := validateDeveloperOutput(output); err != nil {
		return decodedRoleOutput{}, structuredError(RoleDeveloper, err)
	}
	return decodedRoleOutput{
		Outcome: OutcomeImplementationReady,
		Handoff: handoffFromOutput(output.Handoff, output.ChangedArtifacts),
	}, nil
}

func canonicalProposalArtifacts(proposals []ProposalReference) ([]ArtifactRef, error) {
	if len(proposals) != 1 {
		return nil, errors.New("exactly one persisted proposal is required to derive changed_artifacts")
	}
	if len(proposals[0].Artifacts) == 0 {
		return nil, errors.New("persisted proposal has no canonical workspace artifact")
	}
	if err := validateOutputArtifacts(proposals[0].Artifacts); err != nil {
		return nil, fmt.Errorf("persisted proposal artifact: %w", err)
	}
	return cloneArtifactRefs(proposals[0].Artifacts), nil
}

func sameArtifactRefs(left, right []ArtifactRef) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Kind != right[index].Kind || left[index].URI != right[index].URI || left[index].Digest != right[index].Digest {
			return false
		}
	}
	return true
}

func decodeReflectionOutput(raw string) (ReflectionResult, error) {
	var output ReflectionOutput
	if err := decodeStrictJSON(raw, &output); err != nil {
		return ReflectionResult{}, err
	}
	if output.SchemaVersion != roleOutputSchemaVersion {
		return ReflectionResult{}, fmt.Errorf("schema_version must be %d", roleOutputSchemaVersion)
	}
	validVerdicts := map[string]bool{"success": true, "partial": true, "failure": true, "uncertain": true}
	if !validVerdicts[output.Verdict] {
		return ReflectionResult{}, fmt.Errorf("invalid verdict %q", output.Verdict)
	}
	if output.Confidence < 0 || output.Confidence > 1 {
		return ReflectionResult{}, errors.New("confidence must be between 0 and 1")
	}
	validFailures := map[string]bool{"none": true, "policy": true, "tool": true, "environment": true, "model": true, "verification": true, "unknown": true}
	if !validFailures[output.FailureClass] {
		return ReflectionResult{}, fmt.Errorf("invalid failure_class %q", output.FailureClass)
	}
	if output.ReplanRequested && strings.TrimSpace(output.ReplanReason) == "" {
		return ReflectionResult{}, errors.New("replan_reason is required when replan_requested is true")
	}
	result := ReflectionResult{Verdict: output.Verdict, Confidence: output.Confidence, FailureClass: output.FailureClass, Evidence: cloneArtifactRefs(output.Evidence), ReplanRequested: output.ReplanRequested, ReplanReason: output.ReplanReason}
	if output.LessonCandidate != nil {
		candidate := output.LessonCandidate
		if strings.TrimSpace(candidate.Summary) == "" || strings.TrimSpace(candidate.Content) == "" || strings.TrimSpace(candidate.Category) == "" {
			return ReflectionResult{}, errors.New("lesson_candidate summary, content, and category are required")
		}
		result.LessonCandidate = &MemoryCandidate{Summary: candidate.Summary, Content: candidate.Content, Category: candidate.Category, KeyTopics: append([]string(nil), candidate.KeyTopics...)}
	}
	return result, nil
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
	if output.FanOut != nil {
		if err := output.FanOut.Validate(); err != nil {
			return err
		}
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

func validateTesterOutputWithSkipped(output TesterOutput, allowSkipped bool) error {
	if err := validateSchemaVersion(output.SchemaVersion); err != nil {
		return err
	}
	if output.Result != "passed" && output.Result != "failed" && !(allowSkipped && output.Result == "skipped") {
		return schemaEnumValueError{field: "result"}
	}
	if output.Result == "skipped" {
		if len(output.TestsExecuted) != 0 {
			return errors.New("skipped result cannot claim executed tests")
		}
		return validateOutputHandoff(output.Handoff)
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
			return schemaEnumValueError{field: "tests_executed.status"}
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
		return schemaEnumValueError{field: "decision"}
	}
	for i, finding := range output.Findings {
		if strings.TrimSpace(finding.Summary) == "" {
			return fmt.Errorf("findings[%d].summary is required", i)
		}
		switch finding.Severity {
		case "info", "low", "medium", "high", "critical":
		default:
			return schemaEnumValueError{field: "findings.severity"}
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
	notes := output.Notes
	if strings.TrimSpace(notes) == "" {
		notes = output.Summary
	}
	return &HandoffDraft{
		Objective:        output.Objective,
		Reason:           output.Reason,
		Artifacts:        cloneArtifactRefs(artifacts),
		Evidence:         cloneArtifactRefs(output.Evidence),
		UnresolvedIssues: cloneIssues(output.UnresolvedIssues),
		Notes:            notes,
	}
}

func structuredError(role Role, err error) error {
	code, feedback, fields := safeSchemaCorrection(role, err)
	var allowedValues []string
	if values, ok := safeSchemaEnumCorrection(role, err); ok {
		code = "invalid_field_value"
		fields = []string{values.field}
		allowedValues = values.allowed
		feedback = canonicalEnumFeedback(role, values.field, values.allowed)
	}
	return &SchemaCorrectionError{
		Role: role, Code: code, Fields: fields, AllowedValues: allowedValues, Feedback: feedback,
		Cause: &StructuredOutputError{Role: role, Cause: err},
	}
}

type schemaEnumValueError struct{ field string }

func (schemaEnumValueError) Error() string { return "invalid enum value" }

type safeSchemaEnum struct {
	field   string
	allowed []string
}

func safeSchemaEnumCorrection(role Role, err error) (safeSchemaEnum, bool) {
	var enumErr schemaEnumValueError
	if !errors.As(err, &enumErr) {
		return safeSchemaEnum{}, false
	}
	allowed := schemaFieldAllowedValues(role, enumErr.field)
	if len(allowed) == 0 {
		return safeSchemaEnum{}, false
	}
	return safeSchemaEnum{field: enumErr.field, allowed: allowed}, true
}

func safeSchemaCorrection(role Role, err error) (string, string, []string) {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "invalid_json_syntax", canonicalSchemaFeedback(role, "invalid_json_syntax", nil), nil
	}
	message := strings.ToLower(err.Error())
	fields := schemaFields(role)
	missing := make([]string, 0, 4)
	for _, field := range fields {
		if strings.Contains(message, field) && strings.Contains(message, "required") {
			missing = append(missing, field)
		}
	}
	if len(missing) > 0 {
		return "missing_required_fields", canonicalSchemaFeedback(role, "missing_required_fields", missing), missing
	}
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		field := strings.ToLower(typeError.Field)
		field = strings.Split(field, "[")[0]
		field = strings.Split(field, ".")[0]
		for _, allowed := range fields {
			if field == allowed {
				problemFields := []string{allowed}
				return "invalid_field_type", canonicalSchemaFeedback(role, "invalid_field_type", problemFields), problemFields
			}
		}
		return "invalid_field_type", canonicalSchemaFeedback(role, "invalid_field_type", nil), nil
	}
	if strings.Contains(message, "invalid") || strings.Contains(message, "must be") || strings.Contains(message, "cannot") {
		return "invalid_field_value", canonicalSchemaFeedback(role, "invalid_field_value", nil), nil
	}
	return "invalid_structured_output", canonicalSchemaFeedback(role, "invalid_structured_output", nil), nil
}

func canonicalSchemaFeedback(role Role, code string, fields []string) string {
	switch code {
	case "invalid_json_syntax":
		return "The previous response was not valid JSON syntax. Return exactly one JSON object matching the required role schema."
	case "missing_required_fields":
		allowed := schemaFields(role)
		if len(fields) == 0 {
			return "The previous JSON omitted required fields. Add the missing fields listed by the required role schema and return exactly one JSON object."
		}
		for _, field := range fields {
			found := false
			for _, candidate := range allowed {
				if field == candidate {
					found = true
					break
				}
			}
			if !found {
				return "The previous JSON omitted required fields. Add the missing fields listed by the required role schema and return exactly one JSON object."
			}
		}
		return "Missing required field(s): " + strings.Join(fields, ", ") + ". Add them to the next response. Follow the required role schema shown in the task instructions and return exactly one JSON object."
	case "invalid_field_type":
		if len(fields) == 1 {
			if expected := schemaFieldExpectedType(role, fields[0]); expected != "" {
				return "Field " + fields[0] + " must be " + expected + ". Match its type and structure to the required role schema, then return exactly one JSON object."
			}
		}
		return "A field has the wrong JSON type. Check field types in the required role schema and return exactly one JSON object."
	case "invalid_field_value", "invalid_structured_output":
		return "The previous response did not match the required JSON structure. Check field types and allowed values in the required role schema, then return exactly one valid response."
	default:
		return ""
	}
}

func canonicalEnumFeedback(role Role, field string, allowed []string) string {
	if len(schemaFieldAllowedValues(role, field)) == 0 || len(allowed) == 0 {
		return canonicalSchemaFeedback(role, "invalid_field_value", nil)
	}
	return "Field " + field + " must be one of: " + strings.Join(allowed, ", ") + ". Match the allowed values in the required role schema, then return exactly one valid response."
}

// safeSchemaCorrectionDetails returns only schema-owned identifiers. It never
// carries a provider value or error string across the worker boundary.
func safeSchemaCorrectionDetails(role Role, code string, fields, allowedValues []string) (string, string, []string) {
	if len(fields) != 1 {
		return "", "", nil
	}
	field := fields[0]
	if code == "invalid_field_type" {
		expected := schemaFieldExpectedType(role, field)
		if expected != "" {
			return field, expected, nil
		}
	}
	if code == "invalid_field_value" {
		allowed := schemaFieldAllowedValues(role, field)
		if len(allowed) > 0 && equalStrings(allowed, allowedValues) {
			return field, "", allowed
		}
	}
	return "", "", nil
}

// roleFailureFeedback rebuilds specific feedback from a role's local schema.
// The worker-provided field/type pair is accepted only when it exactly matches
// that schema; all unknown values fall back to the existing generic feedback.
func roleFailureFeedback(role Role, code, field, expectedType string, allowedValues ...[]string) string {
	if code == "invalid_field_type" {
		if expected := schemaFieldExpectedType(role, field); expected != "" && expected == expectedType {
			return canonicalSchemaFeedback(role, code, []string{field})
		}
	}
	if code == "invalid_field_value" {
		allowed := schemaFieldAllowedValues(role, field)
		provided := []string(nil)
		if len(allowedValues) == 1 {
			provided = allowedValues[0]
		}
		if len(allowed) > 0 && equalStrings(allowed, provided) {
			return canonicalEnumFeedback(role, field, allowed)
		}
	}
	return correctionFeedback(code)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func schemaFieldAllowedValues(role Role, field string) []string {
	switch role {
	case RoleTester:
		switch field {
		case "result":
			return []string{"passed", "failed"}
		case "tests_executed.status":
			return []string{"passed", "failed", "timeout", "error"}
		}
	case RoleReviewer:
		switch field {
		case "decision":
			return []string{"approved", "changes_requested"}
		case "findings.severity":
			return []string{"info", "low", "medium", "high", "critical"}
		}
	}
	return nil
}

func schemaFieldExpectedType(role Role, field string) string {
	switch role {
	case RolePlanner:
		switch field {
		case "schema_version":
			return "a JSON number"
		case "understanding":
			return "a JSON string"
		case "implementation_plan", "task_breakdown", "acceptance_criteria", "risks", "assumptions":
			return "a JSON array of strings"
		case "handoff":
			return "a JSON object or a concise JSON string summary"
		}
	case RoleDeveloper:
		switch field {
		case "schema_version":
			return "a JSON number"
		case "summary":
			return "a JSON string"
		case "commands_executed", "known_limitations":
			return "a JSON array of strings"
		case "changed_artifacts":
			return "a JSON array of objects"
		case "handoff":
			return "a JSON object or a concise JSON string summary"
		}
	case RoleTester:
		switch field {
		case "schema_version":
			return "a JSON number"
		case "result":
			return "a JSON string"
		case "tests_executed", "failure_evidence":
			return "a JSON array of objects"
		case "reproduction":
			return "a JSON array of strings"
		case "handoff":
			return "a JSON object or a concise JSON string summary"
		}
	case RoleReviewer:
		switch field {
		case "schema_version":
			return "a JSON number"
		case "decision":
			return "a JSON string"
		case "findings", "evidence":
			return "a JSON array of objects"
		case "required_corrections":
			return "a JSON array of strings"
		case "handoff":
			return `for changes_requested, a non-null JSON object with non-empty "objective" and "reason" strings or a non-empty JSON string summary (for example {"objective":"fix","reason":"finding"} or "Fix the finding"); for approved, omit "handoff" or set it to null`
		}
	case RoleReflector:
		switch field {
		case "schema_version":
			return "a JSON number"
		case "confidence":
			return "a JSON number"
		case "replan_requested":
			return "a JSON boolean"
		case "verdict", "failure_class", "replan_reason":
			return "a JSON string"
		case "evidence":
			return "a JSON array of objects"
		case "lesson_candidate":
			return "a JSON object or null"
		}
	}
	return ""
}

func schemaFields(role Role) []string {
	switch role {
	case RolePlanner:
		return []string{"schema_version", "understanding", "implementation_plan", "task_breakdown", "acceptance_criteria", "risks", "assumptions", "handoff"}
	case RoleDeveloper:
		return []string{"schema_version", "summary", "commands_executed", "known_limitations", "changed_artifacts", "handoff"}
	case RoleTester:
		return []string{"schema_version", "result", "tests_executed", "failure_evidence", "reproduction", "handoff"}
	case RoleReviewer:
		return []string{"schema_version", "decision", "findings", "required_corrections", "evidence", "handoff"}
	case RoleReflector:
		return []string{"schema_version", "verdict", "confidence", "failure_class", "evidence", "lesson_candidate", "replan_requested", "replan_reason"}
	default:
		return nil
	}
}
