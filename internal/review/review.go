// Package review provides the deterministic review pipeline for Prizm V5.
// Reviews are generated from validation results and mutation info —
// no LLM is used. The reviewer CANNOT approve or apply mutations.
package review

import (
	"fmt"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

// Recommendation is the reviewer's verdict.
type Recommendation string

const (
	// RecommendationApproved means all validations passed and the change is ready for human review.
	RecommendationApproved Recommendation = "approved_for_human_review"
	// RecommendationNeedsFix means validation passed but the reviewer has concerns.
	RecommendationNeedsFix Recommendation = "needs_fix"
	// RecommendationValidationFailed means one or more validations did not pass.
	RecommendationValidationFailed Recommendation = "validation_failed"
	// RecommendationNoMutation means no mutation was detected to review.
	RecommendationNoMutation Recommendation = "no_mutation_detected"
)

// Review is the output of the deterministic review pipeline.
type Review struct {
	ReviewID          string           `json:"review_id"`
	RunID             string           `json:"run_id"`
	CorrelationID     string           `json:"correlation_id"`
	Reviewer          string           `json:"reviewer"`
	MutationStatus    string           `json:"mutation_status"`
	ValidationStatus  string           `json:"validation_status"`
	Summary           string           `json:"summary"`
	FilesChanged      []string         `json:"files_changed,omitempty"`
	ValidationResults []ValidationInfo `json:"validation_results,omitempty"`
	ReviewerNotes     string           `json:"reviewer_notes,omitempty"`
	Recommendation    Recommendation   `json:"recommendation"`
	CreatedAt         time.Time        `json:"created_at"`
}

// ValidationInfo holds the result of a single validation profile run.
type ValidationInfo struct {
	Profile    string `json:"profile"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
}

// ReviewEventTypes holds V5 review event type constants.
var ReviewEventTypes = struct {
	ReviewRequested string
	ReviewStarted   string
	ReviewCompleted string
	ReviewFailed    string
}{
	ReviewRequested: "prizm.review.requested",
	ReviewStarted:   "prizm.review.started",
	ReviewCompleted: "prizm.review.completed",
	ReviewFailed:    "prizm.review.failed",
}

// EventEmitter is a callback for emitting Prizm events.
type EventEmitter func(eventType, source string, payload map[string]any)

// Reviewer generates deterministic reviews from validation results.
type Reviewer struct {
	name    string
	emitter EventEmitter
}

// NewReviewer creates a new deterministic reviewer.
func NewReviewer(name string) *Reviewer {
	if name == "" {
		name = "lumi-deterministic"
	}
	return &Reviewer{name: name}
}

// SetEmitter sets the event emitter callback.
func (r *Reviewer) SetEmitter(emitter EventEmitter) {
	r.emitter = emitter
}

// emit sends an event through the emitter if one is set.
func (r *Reviewer) emit(eventType, source string, payload map[string]any) {
	if r.emitter != nil {
		r.emitter(eventType, source, payload)
	}
}

// Generate produces a review from validation results and mutation info.
// The reviewer CANNOT approve or apply mutations — it only reviews.
func (r *Reviewer) Generate(runID, correlationID string, mutationStatus string, filesChanged []string, validationResults []ValidationInfo, mutationSummaries []event.MutationSummary) (*Review, error) {
	// Emit review.requested
	r.emit(ReviewEventTypes.ReviewRequested, "prizm-review", map[string]any{
		"run_id":          runID,
		"correlation_id":  correlationID,
		"reviewer":        r.name,
		"mutation_status": mutationStatus,
	})

	// Emit review.started
	r.emit(ReviewEventTypes.ReviewStarted, "prizm-review", map[string]any{
		"run_id":         runID,
		"correlation_id": correlationID,
		"reviewer":       r.name,
	})

	// Determine overall validation status
	validationStatus := "none"
	if len(validationResults) > 0 {
		allPassed := true
		for _, vr := range validationResults {
			if vr.Status != "passed" {
				allPassed = false
				break
			}
		}
		if allPassed {
			validationStatus = "passed"
		} else {
			validationStatus = "failed"
		}
	}

	// Determine recommendation
	var recommendation Recommendation
	summary := ""
	reviewerNotes := ""

	switch {
	case mutationStatus == "none" || mutationStatus == "":
		recommendation = RecommendationNoMutation
		summary = "No mutations were detected in this run. Nothing to review."
		reviewerNotes = "The run did not produce any file changes. If mutations were expected, check the agent's output for tool request formatting issues."

	case validationStatus == "failed":
		recommendation = RecommendationValidationFailed
		summary = fmt.Sprintf("Validation failed. %d validations did not pass.", countFailedValidations(validationResults))
		reviewerNotes = buildValidationFailureNotes(validationResults)

	default:
		// Validation passed (or no validations run)
		if validationStatus == "passed" && mutationStatus == "applied" {
			recommendation = RecommendationApproved
			summary = fmt.Sprintf("All %d validations passed and the mutation was applied successfully.", len(validationResults))
			reviewerNotes = "The change was applied cleanly and all validation checks passed. This is ready for human review. The deterministic reviewer does not replace human judgment — please review the changes yourself before merging."
		} else if mutationStatus == "proposed" || mutationStatus == "pending" {
			recommendation = RecommendationApproved
			summary = "Mutation was proposed but not yet applied. Validation checks were skipped or not applicable."
			reviewerNotes = "The mutation has not been applied yet. Review the proposed changes in the approval artifact. Once approved and applied, re-run validation to confirm correctness."
			if validationStatus == "passed" {
				summary = "Validation passed. The mutation was proposed but not yet applied."
				reviewerNotes = "All validation checks passed. Approve the mutation to apply it, then re-run validation to confirm. The deterministic reviewer does not replace human judgment — please review the changes yourself before merging."
			}
		} else {
			recommendation = RecommendationApproved
			summary = "Validation passed and mutation status is clear."
			reviewerNotes = "The change looks good from a validation perspective. This is ready for human review. The deterministic reviewer does not replace human judgment — please review the changes yourself before merging."
		}
	}

	reviewID := event.NewID()

	review := &Review{
		ReviewID:          reviewID,
		RunID:             runID,
		CorrelationID:     correlationID,
		Reviewer:          r.name,
		MutationStatus:    mutationStatus,
		ValidationStatus:  validationStatus,
		Summary:           summary,
		FilesChanged:      filesChanged,
		ValidationResults: validationResults,
		ReviewerNotes:     reviewerNotes,
		Recommendation:    recommendation,
		CreatedAt:         time.Now().UTC(),
	}

	// Emit review.completed
	r.emit(ReviewEventTypes.ReviewCompleted, "prizm-review", map[string]any{
		"run_id":         runID,
		"correlation_id": correlationID,
		"reviewer":       r.name,
		"recommendation": string(recommendation),
		"review_id":      reviewID,
	})

	return review, nil
}

// countFailedValidations returns the number of non-passed validations.
func countFailedValidations(results []ValidationInfo) int {
	count := 0
	for _, vr := range results {
		if vr.Status != "passed" {
			count++
		}
	}
	return count
}

// buildValidationFailureNotes creates human-readable notes about validation failures.
func buildValidationFailureNotes(results []ValidationInfo) string {
	var notes string
	for _, vr := range results {
		if vr.Status != "passed" {
			notes += fmt.Sprintf("- %s: exited with code %d (status: %s, %dms)\n", vr.Profile, vr.ExitCode, vr.Status, vr.DurationMs)
		}
	}
	if notes == "" {
		return "No specific validation failures detected, but overall status was not 'passed'."
	}
	return "Validation failures:\n" + notes + "\nPlease fix the issues above and re-run the validation pipeline."
}
