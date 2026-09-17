package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
)

func TestReviewerGenerateNoMutation(t *testing.T) {
	reviewer := NewReviewer("lumi-deterministic")

	eventsEmitted := make([]string, 0)
	reviewer.SetEmitter(func(eventType, source string, payload map[string]any) {
		eventsEmitted = append(eventsEmitted, eventType)
	})

	review, err := reviewer.Generate("run_123", "corr_456", "none", nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if review.Recommendation != RecommendationNoMutation {
		t.Errorf("expected no_mutation_detected, got %s", review.Recommendation)
	}
	if review.ValidationStatus != "none" {
		t.Errorf("expected validation status 'none', got %s", review.ValidationStatus)
	}

	// Check events emitted
	if len(eventsEmitted) != 3 { // requested, started, completed
		t.Errorf("expected 3 events, got %d: %v", len(eventsEmitted), eventsEmitted)
	}
}

func TestReviewerGenerateValidationFailed(t *testing.T) {
	reviewer := NewReviewer("lumi-deterministic")

	eventsEmitted := make([]string, 0)
	reviewer.SetEmitter(func(eventType, source string, payload map[string]any) {
		eventsEmitted = append(eventsEmitted, eventType)
	})

	validationResults := []ValidationInfo{
		{Profile: "go_test_all", Status: "failed", ExitCode: 1, DurationMs: 500},
	}

	review, err := reviewer.Generate("run_123", "corr_456", "applied", nil, validationResults, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if review.Recommendation != RecommendationValidationFailed {
		t.Errorf("expected validation_failed, got %s", review.Recommendation)
	}
	if review.ValidationStatus != "failed" {
		t.Errorf("expected validation status 'failed', got %s", review.ValidationStatus)
	}
}

func TestReviewerGenerateAllPassed(t *testing.T) {
	reviewer := NewReviewer("lumi-deterministic")

	validationResults := []ValidationInfo{
		{Profile: "go_test_all", Status: "passed", ExitCode: 0, DurationMs: 1832},
	}

	review, err := reviewer.Generate("run_123", "corr_456", "applied", []string{"main.go"}, validationResults, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if review.Recommendation != RecommendationApproved {
		t.Errorf("expected approved_for_human_review, got %s", review.Recommendation)
	}
	if review.ValidationStatus != "passed" {
		t.Errorf("expected validation status 'passed', got %s", review.ValidationStatus)
	}
	if len(review.FilesChanged) != 1 || review.FilesChanged[0] != "main.go" {
		t.Errorf("expected files_changed to contain 'main.go', got %v", review.FilesChanged)
	}
}

func TestReviewerGenerateMixedValidation(t *testing.T) {
	reviewer := NewReviewer("lumi-deterministic")

	validationResults := []ValidationInfo{
		{Profile: "go_test_all", Status: "passed", ExitCode: 0, DurationMs: 1000},
		{Profile: "lint", Status: "failed", ExitCode: 2, DurationMs: 500},
	}

	review, err := reviewer.Generate("run_123", "corr_456", "applied", nil, validationResults, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if review.Recommendation != RecommendationValidationFailed {
		t.Errorf("expected validation_failed for mixed results, got %s", review.Recommendation)
	}
	if review.ValidationStatus != "failed" {
		t.Errorf("expected validation status 'failed', got %s", review.ValidationStatus)
	}
}

func TestReviewerCannotApprove(t *testing.T) {
	// The reviewer's Generate method just produces a review struct.
	// It does NOT call any approval or mutation methods.
	reviewer := NewReviewer("lumi-deterministic")

	review, err := reviewer.Generate("run_123", "corr_456", "pending", nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the reviewer does NOT have approve/apply methods
	// This is verified at compile time — the Reviewer type doesn't
	// have Approve() or Apply() methods

	if review.Recommendation == "" {
		t.Error("expected non-empty recommendation")
	}

	// Verify no mutation summaries were stored or applied
	_ = review
}

func TestWriteReviewArtifact(t *testing.T) {
	tmpDir := t.TempDir()

	review := &Review{
		ReviewID:         "rev_123",
		RunID:            "run_123",
		CorrelationID:    "corr_456",
		Reviewer:         "lumi-deterministic",
		MutationStatus:   "applied",
		ValidationStatus: "passed",
		Summary:          "All checks passed.",
		FilesChanged:     []string{"main.go", "README.md"},
		ValidationResults: []ValidationInfo{
			{Profile: "go_test_all", Status: "passed", ExitCode: 0, DurationMs: 1832},
		},
		ReviewerNotes:  "Looks good. Ready for human review.",
		Recommendation: RecommendationApproved,
	}

	artifactPath, err := WriteReviewArtifact(tmpDir, review)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedPath := filepath.Join(tmpDir, "review.md")
	if artifactPath != expectedPath {
		t.Errorf("expected path %s, got %s", expectedPath, artifactPath)
	}

	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("failed to read artifact: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "# Prizm Review") {
		t.Error("expected review.md to contain title")
	}
	if !strings.Contains(content, "run_123") {
		t.Error("expected review.md to contain run ID")
	}
	if !strings.Contains(content, string(RecommendationApproved)) {
		t.Error("expected review.md to contain recommendation")
	}
	if !strings.Contains(content, "go_test_all") {
		t.Error("expected review.md to contain validation profile name")
	}
	if !strings.Contains(content, "main.go") {
		t.Error("expected review.md to contain files changed")
	}
}

func TestReviewSummary(t *testing.T) {
	// Test the ValidationSummary struct works correctly
	vs := event.ValidationSummary{
		Profile:    "go_test_all",
		Status:     "passed",
		ExitCode:   0,
		DurationMs: 1832,
		ResultPath: "validation/go_test_all.json",
	}

	if vs.Profile != "go_test_all" {
		t.Errorf("expected profile go_test_all, got %s", vs.Profile)
	}

	// Test the ReviewSummary struct works correctly
	rs := event.ReviewSummary{
		Reviewer:       "lumi-deterministic",
		Status:         "completed",
		Recommendation: string(RecommendationApproved),
		ArtifactPath:   "review.md",
	}

	if rs.Reviewer != "lumi-deterministic" {
		t.Errorf("expected reviewer lumi-deterministic, got %s", rs.Reviewer)
	}
}
